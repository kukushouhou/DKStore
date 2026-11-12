// Package docker 实现原生 Docker Engine 与 Docker Compose 的只读平台适配器（实施文档 §4.1）。
package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kukushouhou/DKStore/internal/i18n"
	"github.com/kukushouhou/DKStore/internal/platform"
	"github.com/kukushouhou/DKStore/internal/transport"
	"gopkg.in/yaml.v3"
)

type cpuSample struct {
	stats dockerCPUStats
	at    time.Time
}

// Adapter 实现 platform.Adapter。
type Adapter struct {
	hostID  string
	tr      transport.Transport
	statsMu sync.Mutex
	prevCPU map[string]cpuSample
}

// New 创建原生 Docker 适配器。
func New(hostID string, tr transport.Transport) *Adapter {
	return &Adapter{
		hostID:  hostID,
		tr:      tr,
		prevCPU: make(map[string]cpuSample),
	}
}

func (a *Adapter) Kind() string { return "docker" }

func (a *Adapter) Close() error { return a.tr.Close() }

type dockerInfoResp struct {
	OSType          string `json:"OSType"`
	Architecture    string `json:"Architecture"`
	OperatingSystem string `json:"OperatingSystem"`
	KernelVersion   string `json:"KernelVersion"`
	ServerVersion   string `json:"ServerVersion"`
}

// Probe 探测主机系统架构、Docker 版本与能力声明。
func (a *Adapter) Probe(ctx context.Context) (*platform.HostInfo, error) {
	var info dockerInfoResp
	if err := a.getJSON(ctx, "/info", &info); err != nil {
		return nil, i18n.NewEngineError(i18n.MsgDockerProbeFailed, i18n.DetailOf(err))
	}
	osName, arch, variant := normalizePlatform(info.OSType, info.Architecture)
	platVer := a.detectPlatformVersion(ctx, info)
	return &platform.HostInfo{
		OS:              osName,
		Arch:            arch,
		Variant:         variant,
		DockerVersion:   info.ServerVersion,
		PlatformVersion: platVer,
		Capabilities: platform.Capabilities{
			Discover:            true,
			CheckUpdate:         true,
			NativeProjects:      false,
			PullImage:           true,
			UpdateSingleService: true,
			UpdateWholeProject:  true,
			Rollback:            true,
		},
	}, nil
}

func (a *Adapter) detectPlatformVersion(ctx context.Context, info dockerInfoResp) string {
	platVer := strings.TrimSpace(info.OperatingSystem)
	kvLower := strings.ToLower(strings.TrimSpace(info.KernelVersion))
	osLower := strings.ToLower(platVer)
	if strings.Contains(kvLower, "-trim") || strings.Contains(osLower, "fnos") || strings.Contains(osLower, "trim") {
		for _, verFile := range []string{"/usr/trim/etc/version", "/etc/trim_version"} {
			if b, err := a.tr.ReadFile(ctx, verFile); err == nil {
				if tv := strings.TrimSpace(string(b)); tv != "" {
					if strings.HasPrefix(strings.ToLower(tv), "fnos") {
						return tv
					}
					return "fnOS " + tv
				}
			}
		}
		if !strings.Contains(osLower, "fnos") {
			if platVer != "" {
				return "fnOS (" + platVer + ")"
			}
			return "fnOS"
		}
	}
	return platVer
}

type containerSummary struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Image   string            `json:"Image"`
	State   string            `json:"State"`
	Status  string            `json:"Status"`
	Created int64             `json:"Created"`
	Labels  map[string]string `json:"Labels"`
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
		Env    []string          `json:"Env"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	Mounts []struct {
		Source string `json:"Source"`
	} `json:"Mounts"`
}

var reFnOSAppCenterPath = regexp.MustCompile(`^/vol\d+/@(appcenter|appconf|appdata|apphome|appshare)(/|$)`)

func isVendorManagedPath(p string) bool {
	s := strings.TrimSpace(p)
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, "/var/packages/") || strings.HasPrefix(s, "/var/apps/") {
		return true
	}
	return reFnOSAppCenterPath.MatchString(s)
}

func isVendorManagedContainer(labels map[string]string, env []string, mountSources []string) bool {
	if isVendorManagedPath(labels["com.docker.compose.project.working_dir"]) {
		return true
	}
	for _, cf := range splitFiles(labels["com.docker.compose.project.config_files"]) {
		if isVendorManagedPath(cf) {
			return true
		}
	}
	for _, e := range env {
		if strings.HasPrefix(e, "TRIM_APPDEST=") || strings.HasPrefix(e, "TRIM_APPNAME=") || strings.HasPrefix(e, "TRIM_PKGVAR=") {
			return true
		}
	}
	for _, ms := range mountSources {
		s := strings.TrimSpace(ms)
		if strings.HasPrefix(s, "/var/packages/") {
			return true
		}
		if strings.HasPrefix(s, "/var/apps/") && !strings.Contains(s, "/share/") {
			return true
		}
		if strings.HasPrefix(s, "/vol") && strings.Contains(s, "/@appcenter/") {
			return true
		}
	}
	return false
}

type imageInspect struct {
	ID          string   `json:"Id"`
	RepoDigests []string `json:"RepoDigests"`
}

// ListApps 列出主机上的全部 Compose 项目与独立容器。
func (a *Adapter) ListApps(ctx context.Context) ([]platform.App, error) {
	var list []containerSummary
	if err := a.getJSON(ctx, "/containers/json?all=1", &list); err != nil {
		return nil, i18n.NewEngineError(i18n.MsgDockerListFailed, i18n.DetailOf(err))
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

		var mountSources []string
		for _, m := range detail.Mounts {
			if m.Source != "" {
				mountSources = append(mountSources, m.Source)
			}
		}
		vendorManaged := isVendorManagedContainer(labels, detail.Config.Env, mountSources)

		projName := strings.TrimSpace(labels["com.docker.compose.project"])
		svcName := strings.TrimSpace(labels["com.docker.compose.service"])

		if projName != "" && svcName != "" {
			appKey := fmt.Sprintf("%s/compose/%s", a.hostID, projName)
			app, exists := composeMap[appKey]
			if !exists {
				workDir := labels["com.docker.compose.project.working_dir"]
				cfgFiles := splitFiles(labels["com.docker.compose.project.config_files"])
				envFiles := splitFiles(labels["com.docker.compose.project.environment_file"])
				src := platform.SourceCompose
				if vendorManaged {
					src = platform.SourceVendorManaged
				}
				app = &platform.App{
					Key:    appKey,
					Name:   projName,
					Source: src,
					Project: &platform.ProjectInfo{
						Name:        projName,
						WorkingDir:  workDir,
						ConfigFiles: cfgFiles,
						EnvFiles:    envFiles,
					},
				}
				composeMap[appKey] = app
			} else if vendorManaged {
				app.Source = platform.SourceVendorManaged
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
		standaloneSrc := platform.SourceStandalone
		if vendorManaged {
			standaloneSrc = platform.SourceVendorManaged
		}
		standaloneApps = append(standaloneApps, platform.App{
			Key:    appKey,
			Name:   cName,
			Source: standaloneSrc,
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

type dockerCPUUsage struct {
	TotalUsage  uint64   `json:"total_usage"`
	PercpuUsage []uint64 `json:"percpu_usage"`
}

type dockerCPUStats struct {
	CPUUsage       dockerCPUUsage `json:"cpu_usage"`
	SystemCPUUsage uint64         `json:"system_cpu_usage"`
	OnlineCPUs     int            `json:"online_cpus"`
}

type dockerMemoryStats struct {
	Usage uint64            `json:"usage"`
	Limit uint64            `json:"limit"`
	Stats map[string]uint64 `json:"stats"`
}

type dockerContainerStats struct {
	CPUStats    dockerCPUStats    `json:"cpu_stats"`
	PreCPUStats dockerCPUStats    `json:"precpu_stats"`
	MemoryStats dockerMemoryStats `json:"memory_stats"`
}

func computeStats(st dockerContainerStats) (cpuPct float64, memUsage int64, memLimit int64) {
	if st.PreCPUStats.SystemCPUUsage > 0 && st.CPUStats.SystemCPUUsage > st.PreCPUStats.SystemCPUUsage && st.CPUStats.CPUUsage.TotalUsage > st.PreCPUStats.CPUUsage.TotalUsage {
		cpuDelta := st.CPUStats.CPUUsage.TotalUsage - st.PreCPUStats.CPUUsage.TotalUsage
		sysDelta := st.CPUStats.SystemCPUUsage - st.PreCPUStats.SystemCPUUsage
		onlineCPUs := st.CPUStats.OnlineCPUs
		if onlineCPUs <= 0 {
			onlineCPUs = len(st.CPUStats.CPUUsage.PercpuUsage)
		}
		if onlineCPUs <= 0 {
			onlineCPUs = 1
		}
		pct := (float64(cpuDelta) / float64(sysDelta)) * float64(onlineCPUs) * 100.0
		cpuPct = math.Round(pct*100) / 100
	}
	inactive := st.MemoryStats.Stats["inactive_file"]
	if st.MemoryStats.Usage > inactive {
		memUsage = int64(st.MemoryStats.Usage - inactive)
	}
	memLimit = int64(st.MemoryStats.Limit)
	return
}

func parseHealthFromStatus(status string) string {
	s := strings.ToLower(status)
	switch {
	case strings.Contains(s, "(healthy)"):
		return "healthy"
	case strings.Contains(s, "(unhealthy)"):
		return "unhealthy"
	case strings.Contains(s, "(health: starting)"), strings.Contains(s, "(starting)"):
		return "starting"
	default:
		return ""
	}
}

type statVal struct {
	cpu      float64
	memUsage int64
	memLimit int64
}

func (a *Adapter) fetchOneShotStats(ctx context.Context, ids []string) map[string]dockerContainerStats {
	out := make(map[string]dockerContainerStats, len(ids))
	if len(ids) == 0 {
		return out
	}
	var mu sync.Mutex
	sem := make(chan struct{}, 12)
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(cid string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			sCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			var st dockerContainerStats
			if err := a.getJSON(sCtx, "/containers/"+url.PathEscape(cid)+"/stats?stream=false&one-shot=true", &st); err != nil {
				return
			}
			mu.Lock()
			out[cid] = st
			mu.Unlock()
		}(id)
	}
	wg.Wait()
	return out
}

func (a *Adapter) collectRuntimeStats(ctx context.Context, runningIDs []string) map[string]statVal {
	statsMap := make(map[string]statVal, len(runningIDs))
	if len(runningIDs) == 0 {
		return statsMap
	}

	raw1 := a.fetchOneShotStats(ctx, runningIDs)
	now := time.Now()
	var coldIDs []string

	a.statsMu.Lock()
	if a.prevCPU == nil {
		a.prevCPU = make(map[string]cpuSample)
	}
	for k, v := range a.prevCPU {
		if now.Sub(v.at) > 2*time.Minute {
			delete(a.prevCPU, k)
		}
	}
	for _, id := range runningIDs {
		st, ok := raw1[id]
		if !ok {
			continue
		}
		if st.PreCPUStats.SystemCPUUsage > 0 {
			cpu, memU, memL := computeStats(st)
			statsMap[id] = statVal{cpu: cpu, memUsage: memU, memLimit: memL}
			a.prevCPU[id] = cpuSample{stats: st.CPUStats, at: now}
			continue
		}
		prev, hasPrev := a.prevCPU[id]
		a.prevCPU[id] = cpuSample{stats: st.CPUStats, at: now}
		if hasPrev && now.Sub(prev.at) <= 60*time.Second && prev.stats.SystemCPUUsage > 0 && st.CPUStats.SystemCPUUsage > prev.stats.SystemCPUUsage {
			st.PreCPUStats = prev.stats
			cpu, memU, memL := computeStats(st)
			statsMap[id] = statVal{cpu: cpu, memUsage: memU, memLimit: memL}
		} else {
			_, memU, memL := computeStats(st)
			statsMap[id] = statVal{cpu: 0, memUsage: memU, memLimit: memL}
			coldIDs = append(coldIDs, id)
		}
	}
	a.statsMu.Unlock()

	if len(coldIDs) > 0 && ctx.Err() == nil {
		select {
		case <-ctx.Done():
			return statsMap
		case <-time.After(150 * time.Millisecond):
		}
		raw2 := a.fetchOneShotStats(ctx, coldIDs)
		now2 := time.Now()
		a.statsMu.Lock()
		for _, id := range coldIDs {
			st2, ok := raw2[id]
			if !ok {
				continue
			}
			if st2.PreCPUStats.SystemCPUUsage == 0 {
				if prev, hasPrev := a.prevCPU[id]; hasPrev && prev.stats.SystemCPUUsage > 0 {
					st2.PreCPUStats = prev.stats
				}
			}
			cpu, memU, memL := computeStats(st2)
			statsMap[id] = statVal{cpu: cpu, memUsage: memU, memLimit: memL}
			a.prevCPU[id] = cpuSample{stats: st2.CPUStats, at: now2}
		}
		a.statsMu.Unlock()
	}

	return statsMap
}

// ListRuntimeApps 采集主机上全部容器的树状实时运行状态与资源占用。
func (a *Adapter) ListRuntimeApps(ctx context.Context) ([]platform.AppRuntimeTree, error) {
	return a.ListRuntimeAppsWithOptions(ctx, true)
}

// ListRuntimeAppsWithOptions 按需采集主机容器树（withStats=false 时跳过 stats 采样以实现毫秒级首屏）。
func (a *Adapter) ListRuntimeAppsWithOptions(ctx context.Context, withStats bool) ([]platform.AppRuntimeTree, error) {
	var list []containerSummary
	if err := a.getJSON(ctx, "/containers/json?all=1", &list); err != nil {
		return nil, i18n.NewEngineError(i18n.MsgDockerListFailed, i18n.DetailOf(err))
	}

	var filtered []containerSummary
	var runningIDs []string
	for _, cs := range list {
		if cs.Labels["dkstore.self"] == "true" {
			continue
		}
		filtered = append(filtered, cs)
		if strings.ToLower(cs.State) == "running" {
			runningIDs = append(runningIDs, cs.ID)
		}
	}

	var statsMap map[string]statVal
	if withStats {
		statsMap = a.collectRuntimeStats(ctx, runningIDs)
	}

	composeMap := make(map[string]*platform.AppRuntimeTree)
	var standaloneApps []platform.AppRuntimeTree

	for _, cs := range filtered {
		cName := ""
		if len(cs.Names) > 0 {
			cName = strings.TrimPrefix(cs.Names[0], "/")
		}
		if cName == "" {
			cName = cs.ID[:minLen(len(cs.ID), 12)]
		}
		state := strings.ToLower(strings.TrimSpace(cs.State))
		if state == "" {
			state = "unknown"
		}
		sv := statsMap[cs.ID]
		cStat := platform.ContainerRuntimeStat{
			ID:         cs.ID[:minLen(len(cs.ID), 12)],
			Name:       cName,
			Service:    strings.TrimSpace(cs.Labels["com.docker.compose.service"]),
			Image:      cs.Image,
			State:      state,
			Health:     parseHealthFromStatus(cs.Status),
			Uptime:     cs.Status,
			StartedAt:  cs.Created,
			CPUPercent: sv.cpu,
			MemUsage:   sv.memUsage,
			MemLimit:   sv.memLimit,
		}

		projName := strings.TrimSpace(cs.Labels["com.docker.compose.project"])
		vendorManaged := isVendorManagedContainer(cs.Labels, nil, nil)
		if projName != "" {
			appKey := fmt.Sprintf("%s/compose/%s", a.hostID, projName)
			tree, exists := composeMap[appKey]
			if !exists {
				src := platform.SourceCompose
				if vendorManaged {
					src = platform.SourceVendorManaged
				}
				tree = &platform.AppRuntimeTree{
					Key:        appKey,
					HostID:     a.hostID,
					Name:       projName,
					Source:     src,
					StatsReady: withStats,
				}
				composeMap[appKey] = tree
			} else if vendorManaged {
				tree.Source = platform.SourceVendorManaged
			}
			tree.TotalCount++
			if state == "running" {
				tree.RunningCount++
			}
			tree.CPUPercent = math.Round((tree.CPUPercent+cStat.CPUPercent)*100) / 100
			tree.MemUsage += cStat.MemUsage
			tree.Containers = append(tree.Containers, cStat)
			continue
		}

		src := platform.SourceStandalone
		if vendorManaged {
			src = platform.SourceVendorManaged
		}
		appKey := fmt.Sprintf("%s/container/%s", a.hostID, cName)
		runCnt := 0
		if state == "running" {
			runCnt = 1
		}
		standaloneApps = append(standaloneApps, platform.AppRuntimeTree{
			Key:          appKey,
			HostID:       a.hostID,
			Name:         cName,
			Source:       src,
			RunningCount: runCnt,
			TotalCount:   1,
			CPUPercent:   cStat.CPUPercent,
			MemUsage:     cStat.MemUsage,
			StatsReady:   withStats,
			Containers:   []platform.ContainerRuntimeStat{cStat},
		})
	}

	var result []platform.AppRuntimeTree
	for _, tree := range composeMap {
		sort.Slice(tree.Containers, func(i, j int) bool {
			return tree.Containers[i].Name < tree.Containers[j].Name
		})
		result = append(result, *tree)
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
		if strings.HasPrefix(k, "org.opencontainers.image.") ||
			strings.HasPrefix(k, "org.label-schema.") ||
			strings.HasPrefix(k, "net.unraid.docker.") ||
			strings.HasPrefix(k, "com.docker.compose.") ||
			k == "homepage.icon" {
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
