// Package docker 实现原生 Docker Engine 与 Docker Compose 的只读平台适配器（实施文档 §4.1）。
package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	" net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/kukushouhou/DKStore/internal/platform"
	"github.com/kukushouhou/DKStore/internal/transport"
	"gopkg.in/yaml.v3"
)

// Adapter 实现 platform.Adapter。
type Adapter struct {
	hostID string
	tr     transport.Transport
}

// New 创建原生 Docker 适配器。
func New(hostID string, tr transport.Transport) *Adapter {
	return &Adapter{hostID: hostID, tr: tr}
}

func (a *Adapter) Kind() string { return "docker" }

func (a *Adapter) Close() error { return a.tr.Close() }

type dockerInfoResp struct {
	OSType          string `json:"OSType"`
	Architecture    string `json:"Architecture"`
	OperatingSystem string `json:"OperatingSystem"`
	ServerVersion   string `json:"ServerVersion"`
}

// Probe 探测主机系统架构、Docker 版本与能力声明。
func (a *Adapter) Probe(ctx context.Context) (*platform.HostInfo, error) {
	var info dockerInfoResp
	if err := a.getJSON(ctx, "/info", &info); err != nil {
		return nil, fmt.Errorf("读取 Docker /info 失败: %w", err)
	}
	osName, arch, variant := normalizePlatform(info.OSType, info.Architecture)
	return &platform.HostInfo{
		OS:              osName,
		Arch:            arch,
		Variant:         variant,
		DockerVersion:   info.ServerVersion,
		PlatformVersion: info.OperatingSystem,
		Capabilities: platform.Capabilities{
			Discover:            true,
			CheckUpdate:         true,
			NativeProjects:      false,
			PullImage:           true,
			UpdateSingleService: true,
			UpdateWholeProject:  true,
		},
	}, nil
}

type containerSummary struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Image  string            `json:"Image"`
	State  string            `json:"State"`
	Labels map[string]string `json:"Labels"`
}

type containerInspect struct {
	ID    string `json:"Id"`
	Name  string `json:"Name"`
	State struct {
		Status string `json:"Status"`
	} `json:"State"`
	Image  string `json:"Image"`
	Config struct {
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
}

type imageInspect struct {
	ID          string   `json:"Id"`
	RepoDigests []string `json:"RepoDigests"`
	RepoTags    []string `json:"RepoTags"`
	Os          string   `json:"Os"`
	Architecture string  `json:"Architecture"`
	Variant     string   `json:"Variant"`
}

// ListApps 列出主机上的全部 Compose 项目与独立容器。
func (a *Adapter) ListApps(ctx context.Context) ([]platform.App, error) {
	var list []containerSummary
	if err := a.getJSON(ctx, "/containers/json?all=1", &list); err != nil {
		return nil, fmt.Errorf("列出容器失败: %w", err)
	}

	imageCache := make(map[string]*imageInspect)
	composeMap := make(map[string]*platform.App)
	serviceMap := make(map[string]*platform.Service)
	var standaloneApps []platform.App

	for _, cs := range list {
		if cs.Labels["dkstore.self"] == "true" {
			continue
		}
		var detail containerInspect
		if err := a.getJSON(ctx, "/containers/"+url.PathEscape(cs.ID)+"/json", &detail); err != nil {
			continue
		}
		labels := detail.Config.Labels
		if labels == nil {
			labels = cs.Labels
		}
		if labels["dkstore.self"] == "true" {
			continue
		}

		img, ok := imageCache[detail.Image]
		if !ok {
			var ii imageInspect
			if err := a.getJSON(ctx, "/images/"+url.PathEscape(detail.Image)+"/json", &ii); err == nil {
				img = &ii
				imageCache[detail.Image] = img
			}
		}
		var repoDigests []string
		if img != nil {
			repoDigests = img.RepoDigests
		}

		projName := strings.TrimSpace(labels["com.docker.compose.project"])
		svcName := strings.TrimSpace(labels["com.docker.compose.service"])

		if projName != "" && svcName != "" {
			appKey := fmt.Sprintf("%s/compose/%s", a.hostID, projName)
			app, exists := composeMap[appKey]
			if !exists {
				workDir := labels["com.docker.compose.project.working_dir"]
				cfgFiles := splitFiles(labels["com.docker.compose.project.config_files"])
				envFiles := splitFiles(labels["com.docker.compose.project.environment_file"])
				app = &platform.App{
					Key:    appKey,
					Name:   projName,
					Source: platform.SourceCompose,
					Project: &platform.ProjectInfo{
						Name:        projName,
						WorkingDir:  workDir,
						ConfigFiles: cfgFiles,
						EnvFiles:    envFiles,
					},
				}
				composeMap[appKey] = app
			}
			svcKey := fmt.Sprintf("%s/%s", appKey, svcName)
			svc, sExists := serviceMap[svcKey]
			if !sExists {
				svc = &platform.Service{
					Key:          svcKey,
					Name:         svcName,
					ContainerIDs: []string{detail.ID[:minLen(len(detail.ID), 12)]},
					State:        detail.State.Status,
					ImageRef:     detail.Config.Image,
					ImageID:      detail.Image,
					RepoDigests:  repoDigests,
					Labels:       filterLabels(labels),
				}
				serviceMap[svcKey] = svc
			} else {
				svc.ContainerIDs = append(svc.ContainerIDs, detail.ID[:minLen(len(detail.ID), 12)])
				if detail.State.Status == "running" {
					svc.State = "running"
				}
			}
			continue
		}

		// 独立容器
		cName := strings.TrimPrefix(detail.Name, "/")
		if cName == "" && len(cs.Names) > 0 {
			cName = strings.TrimPrefix(cs.Names[0], "/")
		}
		appKey := fmt.Sprintf("%s/container/%s", a.hostID, cName)
		svcKey := appKey + "/main"
		standaloneApps = append(standaloneApps, platform.App{
			Key:    appKey,
			Name:   cName,
			Source: platform.SourceStandalone,
			Services: []platform.Service{
				{
					Key:          svcKey,
					Name:         cName,
					ContainerIDs: []string{detail.ID[:minLen(len(detail.ID), 12)]},
					State:        detail.State.Status,
					ImageRef:     detail.Config.Image,
					ImageID:      detail.Image,
					RepoDigests:  repoDigests,
					Labels:       filterLabels(labels),
				},
			},
		})
	}

	// 组装 Compose 应用并尝试读取原始 YAML 获取声明镜像
	var result []platform.App
	for appKey, app := range composeMap {
		prefix := appKey + "/"
		for sk, svc := range serviceMap {
			if strings.HasPrefix(sk, prefix) {
				app.Services = append(app.Services, *svc)
			}
		}
		sort.Slice(app.Services, func(i, j int) bool {
			return app.Services[i].Name < app.Services[j].Name
		})
		a.enrichComposeSource(ctx, app)
		result = append(result, *app)
	}
	result = append(result, standaloneApps...)
	sort.Slice(result, func(i, j int) bool {
		if result[i].Source != result[j].Source {
			return result[i].Source < result[j].Source
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
}

// enrichComposeSource 读取 Compose YAML 与 .env，提取声明的 services.<name>.image。
func (a *Adapter) enrichComposeSource(ctx context.Context, app *platform.App) {
	if app.Project == nil || len(app.Project.ConfigFiles) == 0 {
		return
	}
	envVars := make(map[string]string)
	envPaths := app.Project.EnvFiles
	if len(envPaths) == 0 && app.Project.WorkingDir != "" {
		envPaths = []string{filepath.ToSlash(filepath.Join(app.Project.WorkingDir, ".env"))}
	}
	for _, ep := range envPaths {
		if b, err := a.tr.ReadFile(ctx, resolvePath(app.Project.WorkingDir, ep)); err == nil {
			for k, v := range parseDotEnv(b) {
				envVars[k] = v
			}
		}
	}

	declaredImages := make(map[string]string)
	readable := false
	for _, cf := range app.Project.ConfigFiles {
		fullPath := resolvePath(app.Project.WorkingDir, cf)
		b, err := a.tr.ReadFile(ctx, fullPath)
		if err != nil {
			continue
		}
		readable = true
		for svc, img := range parseComposeImages(b, envVars) {
			if img != "" {
				declaredImages[svc] = img
			}
		}
	}
	app.Project.SourceReadable = readable
	for i := range app.Services {
		svc := &app.Services[i]
		if img, ok := declaredImages[svc.Name]; ok && !strings.HasPrefix(svc.ImageRef, "sha256:") {
			svc.ImageRef = img
		}
	}
}

func (a *Adapter) getJSON(ctx context.Context, path string, out any) error {
	resp, err := a.tr.DoDocker(ctx, path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func normalizePlatform(osType, arch string) (osName, normArch, variant string) {
	osName = strings.ToLower(strings.TrimSpace(osType))
	if osName == "" {
		osName = "linux"
	}
	switch strings.ToLower(strings.TrimSpace(arch)) {
	case "x86_64", "amd64":
		normArch = "amd64"
	case "aarch64", "arm64":
		normArch = "arm64"
	case "armv7l", "armv7", "armhf":
		normArch = "arm"
		variant = "v7"
	case "armv6l", "armv6":
		normArch = "arm"
		variant = "v6"
	default:
		normArch = strings.ToLower(arch)
	}
	return
}

func splitFiles(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	var out []string
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func resolvePath(workDir, p string) string {
	if strings.HasPrefix(p, "/") || (len(p) >= 2 && p[1] == ':') || workDir == "" {
		return p
	}
	return strings.TrimRight(workDir, "/") + "/" + p
}

func filterLabels(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	keep := make(map[string]string)
	for k, v := range in {
		if strings.HasPrefix(k, "org.opencontainers.image.") || strings.HasPrefix(k, "com.docker.compose.") {
			keep[k] = v
		}
	}
	return keep
}

func minLen(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// parseComposeImages 解析 Compose YAML 中的 services.<name>.image 并做环境变量插值。
func parseComposeImages(yamlBytes []byte, env map[string]string) map[string]string {
	var doc struct {
		Services map[string]struct {
			Image string `yaml:"image"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(yamlBytes, &doc); err != nil {
		return nil
	}
	out := make(map[string]string, len(doc.Services))
	for name, s := range doc.Services {
		if s.Image != "" {
			out[name] = expandEnv(strings.TrimSpace(s.Image), env)
		}
	}
	return out
}

var reVar = regexp.MustCompile(`\$\{([^}]+)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

func expandEnv(s string, env map[string]string) string {
	return reVar.ReplaceAllStringFunc(s, func(match string) string {
		if strings.HasPrefix(match, "${") {
			inner := match[2 : len(match)-1]
			if k, def, ok := strings.Cut(inner, ":-"); ok {
				if v := env[k]; v != "" {
					return v
				}
				return def
			}
			if k, def, ok := strings.Cut(inner, "-"); ok {
				if v, exists := env[k]; exists {
					return v
				}
				return def
			}
			return env[inner]
		}
		return env[match[1:]]
	})
}

func parseDotEnv(b []byte) map[string]string {
	out := make(map[string]string)
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		v = strings.Trim(v, `"'`)
		out[k] = v
	}
	return out
}
