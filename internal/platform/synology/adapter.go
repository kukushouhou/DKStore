// Package synology 实现群晖 DSM / Container Manager（及旧版 Docker 套件）的只读平台适配器（实施文档 §4.2）。
package synology

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/kukushouhou/DKStore/internal/platform"
	"github.com/kukushouhou/DKStore/internal/platform/docker"
	"github.com/kukushouhou/DKStore/internal/transport"
)

// DSMConfig 是可选的群晖 DSM WebAPI 连接配置（当未启用或 2FA 拦截时，优先走 SSH synowebapi）。
type DSMConfig struct {
	URL           string
	Username      string
	Secret        string
	SkipTLSVerify bool
}

// dsmProject 对应 SYNO.Docker.Project method=list 返回的单个项目项。
type dsmProject struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Path         string   `json:"path"`
	SharePath    string   `json:"share_path"`
	Status       string   `json:"status"`
	IsPackage    bool     `json:"is_package"`
	ContainerIDs []string `json:"containerIds"`
}

type synoWebAPIExecutor interface {
	ExecSynoWebAPIProjectList(ctx context.Context) ([]byte, error)
}

// Adapter 实现群晖 DSM 平台适配器。
type Adapter struct {
	hostID string
	tr     transport.Transport
	dock   *docker.Adapter
	dsm    DSMConfig
}

// New 创建群晖 DSM 平台适配器。
func New(hostID string, tr transport.Transport, dsm DSMConfig) *Adapter {
	return &Adapter{
		hostID: hostID,
		tr:     tr,
		dock:   docker.New(hostID, tr),
		dsm:    dsm,
	}
}

func (a *Adapter) Kind() string { return "synology" }

func (a *Adapter) Close() error { return a.dock.Close() }

// HasLocalImage 检查群晖本地 Docker 是否仍保留指定镜像。
func (a *Adapter) HasLocalImage(ctx context.Context, imageIDOrRef string) bool {
	return a.dock.HasLocalImage(ctx, imageIDOrRef)
}

// UpdateService 在群晖主机上执行单服务更新（保持 DSM Project 原生路径、YAML 与 Container Manager 状态同步，立项文档 §9.2 / A13 / A14）。
func (a *Adapter) UpdateService(
	ctx context.Context,
	req platform.UpdateRequest,
	onProgress func(platform.PullProgress),
) (*platform.UpdateResult, error) {
	return a.dock.UpdateService(ctx, req, onProgress)
}

// Probe 探测群晖 DSM 版本、Docker 引擎信息及原生 Project 发现能力。
func (a *Adapter) Probe(ctx context.Context) (*platform.HostInfo, error) {
	info, err := a.dock.Probe(ctx)
	if err != nil {
		return nil, err
	}
	if ver := a.readDSMVersion(ctx); ver != "" {
		info.PlatformVersion = ver
	}
	if projs, err := a.listDSMProjects(ctx); err == nil && projs != nil {
		info.Capabilities.NativeProjects = true
		info.Capabilities.NativeManagerSync = true
	}
	return info, nil
}

// ListApps 列出群晖主机上的所有容器、Compose 项目，并叠加 Container Manager 原生项目与套件归属信息。
func (a *Adapter) ListApps(ctx context.Context) ([]platform.App, error) {
	apps, err := a.dock.ListApps(ctx)
	if err != nil {
		return nil, err
	}

	projs, _ := a.listDSMProjects(ctx)
	byPath := make(map[string]dsmProject, len(projs))
	byName := make(map[string]dsmProject, len(projs))
	for _, p := range projs {
		if p.Path != "" {
			byPath[strings.TrimRight(p.Path, "/")] = p
		}
		if p.Name != "" {
			byName[strings.ToLower(p.Name)] = p
		}
	}

	for i := range apps {
		app := &apps[i]
		var matched *dsmProject
		if app.Project != nil {
			cleanDir := strings.TrimRight(app.Project.WorkingDir, "/")
			if p, ok := byPath[cleanDir]; ok {
				matched = &p
			} else if p, ok := byName[strings.ToLower(app.Project.Name)]; ok {
				matched = &p
			}
		} else if p, ok := byName[strings.ToLower(app.Name)]; ok {
			matched = &p
		}

		if matched != nil {
			if matched.IsPackage {
				app.Source = platform.SourceVendorManaged
			} else {
				app.Source = platform.SourceDSMProject
			}
			if app.Project == nil {
				app.Project = &platform.ProjectInfo{
					Name:       matched.Name,
					WorkingDir: matched.Path,
				}
			}
			app.Project.DSMProjectID = matched.ID
			if app.Project.WorkingDir == "" {
				app.Project.WorkingDir = matched.Path
			}
			// 将 app.Key 升级为 dsm_project 前缀，与实施文档 §2.2 保持一致
			if app.Source == platform.SourceDSMProject {
				oldKey := app.Key
				newKey := fmt.Sprintf("%s/dsm_project/%s", a.hostID, app.Name)
				app.Key = newKey
				for j := range app.Services {
					app.Services[j].Key = strings.Replace(app.Services[j].Key, oldKey+"/", newKey+"/", 1)
				}
			}
		}

		// 兜底识别群晖套件中心（/var/packages/...）托管的容器
		if app.Project != nil && strings.HasPrefix(app.Project.WorkingDir, "/var/packages/") {
			app.Source = platform.SourceVendorManaged
		}
	}

	return apps, nil
}

func (a *Adapter) readDSMVersion(ctx context.Context) string {
	b, err := a.tr.ReadFile(ctx, "/etc.defaults/VERSION")
	if err != nil {
		return ""
	}
	kv := make(map[string]string)
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		kv[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	prod := kv["productversion"]
	if prod == "" {
		return ""
	}
	build := kv["buildnumber"]
	small := kv["smallfixnumber"]
	ver := "DSM " + prod
	if build != "" {
		ver += "-" + build
	}
	if small != "" && small != "0" {
		ver += " Update " + small
	}
	return ver
}

func (a *Adapter) listDSMProjects(ctx context.Context) ([]dsmProject, error) {
	// 1. 优先通过已认证的 SSH 传输层或本机执行只读 synowebapi（无需二次处理 DSM 2FA OTP）
	if execTr, ok := a.tr.(synoWebAPIExecutor); ok {
		if raw, err := execTr.ExecSynoWebAPIProjectList(ctx); err == nil {
			if list, err := parseSynoWebAPIProjects(raw); err == nil {
				return list, nil
			}
		}
	} else if a.tr.Mode() == transport.ModeLocal {
		cmd := exec.CommandContext(ctx, "/usr/syno/bin/synowebapi", "--exec", "api=SYNO.Docker.Project", "version=1", "method=list")
		if raw, err := cmd.Output(); err == nil {
			if list, err := parseSynoWebAPIProjects(raw); err == nil {
				return list, nil
			}
		}
	}

	// 2. 若配置了 DSM HTTP WebAPI 地址与凭据，尝试通过 HTTP WebAPI 获取
	if a.dsm.URL != "" && a.dsm.Username != "" && a.dsm.Secret != "" {
		return a.listDSMProjectsViaHTTP(ctx)
	}
	return nil, fmt.Errorf("未配置或无法访问群晖 DSM Project 接口")
}

func parseSynoWebAPIProjects(raw []byte) ([]dsmProject, error) {
	s := strings.TrimSpace(string(raw))
	// synowebapi 首行会输出形如 "[Line 295] Exec WebAPI: ... param={}" 的诊断行，取换行后的顶层 JSON 对象
	if idx := strings.Index(s, "\n{"); idx >= 0 {
		s = s[idx+1:]
	} else if idx := strings.Index(s, "{"); idx >= 0 {
		s = s[idx:]
	}
	var resp struct {
		Success bool                  `json:"success"`
		Data    map[string]dsmProject `json:"data"`
	}
	if err := json.Unmarshal([]byte(s), &resp); err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("synowebapi 返回 success=false")
	}
	out := make([]dsmProject, 0, len(resp.Data))
	for k, p := range resp.Data {
		if p.ID == "" {
			p.ID = k
		}
		out = append(out, p)
	}
	return out, nil
}

func (a *Adapter) listDSMProjectsViaHTTP(ctx context.Context) ([]dsmProject, error) {
	base := strings.TrimRight(a.dsm.URL, "/")
	hc := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: a.dsm.SkipTLSVerify}, //nolint:gosec
		},
	}

	loginVals := url.Values{
		"api":     {"SYNO.API.Auth"},
		"version": {"6"},
		"method":  {"login"},
		"account": {a.dsm.Username},
		"passwd":  {a.dsm.Secret},
		"session": {"ContainerManager"},
		"format":  {"sid"},
	}
	loginReq, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/webapi/entry.cgi?"+loginVals.Encode(), nil)
	if err != nil {
		return nil, err
	}
	loginResp, err := hc.Do(loginReq)
	if err != nil {
		return nil, err
	}
	loginBody, _ := io.ReadAll(io.LimitReader(loginResp.Body, 64<<10))
	_ = loginResp.Body.Close()

	var authRes struct {
		Success bool `json:"success"`
		Data    struct {
			SID string `json:"sid"`
		} `json:"data"`
	}
	if err := json.Unmarshal(loginBody, &authRes); err != nil || !authRes.Success || authRes.Data.SID == "" {
		return nil, fmt.Errorf("DSM WebAPI 登录未成功")
	}

	listVals := url.Values{
		"api":     {"SYNO.Docker.Project"},
		"version": {"1"},
		"method":  {"list"},
		"_sid":    {authRes.Data.SID},
	}
	listReq, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/webapi/entry.cgi?"+listVals.Encode(), nil)
	if err != nil {
		return nil, err
	}
	listResp, err := hc.Do(listReq)
	if err != nil {
		return nil, err
	}
	listBody, _ := io.ReadAll(io.LimitReader(listResp.Body, 2<<20))
	_ = listResp.Body.Close()
	return parseSynoWebAPIProjects(listBody)
}
