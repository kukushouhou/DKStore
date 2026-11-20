package synology

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/kukushouhou/DKStore/internal/platform"
	"github.com/kukushouhou/DKStore/internal/platform/docker"
	"github.com/kukushouhou/DKStore/internal/transport"
)

type dsmRegistryItem struct {
	Name                 string   `json:"name"`
	URL                  string   `json:"url"`
	Syno                 bool     `json:"syno"`
	EnableRegistryMirror bool     `json:"enable_registry_mirror"`
	EnableTrustSSC       bool     `json:"enable_trust_SSC"`
	MirrorURLs           []string `json:"mirror_urls"`
	Username             string   `json:"username"`
}

func (c *dsmClient) getRegistries(ctx context.Context) ([]dsmRegistryItem, string, error) {
	var data struct {
		Registries []dsmRegistryItem `json:"registries"`
		Using      string            `json:"using"`
	}
	if err := c.call(ctx, "SYNO.Docker.Registry", "1", "get", map[string]string{
		"limit":  "-1",
		"offset": "0",
	}, &data); err != nil {
		return nil, "", err
	}
	return data.Registries, data.Using, nil
}

func findDockerHubRegistry(items []dsmRegistryItem) *dsmRegistryItem {
	for i := range items {
		if items[i].Syno || strings.EqualFold(strings.TrimSpace(items[i].Name), "Docker Hub") {
			return &items[i]
		}
	}
	return nil
}

// GetDockerHubMirrors 通过群晖官方 SYNO.Docker.Registry 接口读取当前生效的 Docker Hub 镜像加速地址。
func (c *dsmClient) GetDockerHubMirrors(ctx context.Context) ([]string, error) {
	items, _, err := c.getRegistries(ctx)
	if err != nil {
		return nil, err
	}
	hub := findDockerHubRegistry(items)
	if hub == nil || !hub.EnableRegistryMirror {
		return nil, nil
	}
	var out []string
	seen := make(map[string]bool)
	for _, raw := range hub.MirrorURLs {
		u := docker.NormalizeMirrorURL(raw)
		if u != "" && !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	return out, nil
}

// SetDockerHubMirrors 通过群晖官方 SYNO.Docker.Registry set 接口更新 Docker Hub 注册表镜像地址。
// 若新旧列表完全一致则直接跳过，避免无意义地触发群晖容器重启。
func (c *dsmClient) SetDockerHubMirrors(ctx context.Context, urls []string) (bool, error) {
	items, _, err := c.getRegistries(ctx)
	if err != nil {
		return false, err
	}
	hub := findDockerHubRegistry(items)
	if hub == nil {
		return false, fmt.Errorf("Docker Hub registry entry not found in Synology Container Manager")
	}

	clean := make([]string, 0, len(urls))
	seen := make(map[string]bool, len(urls))
	for _, raw := range urls {
		u := docker.NormalizeMirrorURL(raw)
		if u != "" && !seen[u] {
			seen[u] = true
			clean = append(clean, u)
		}
	}

	var curURLs []string
	if hub.EnableRegistryMirror {
		for _, raw := range hub.MirrorURLs {
			if u := docker.NormalizeMirrorURL(raw); u != "" {
				curURLs = append(curURLs, u)
			}
		}
	}
	if equalStrSlices(curURLs, clean) {
		return false, nil
	}

	hubURL := strings.TrimRight(strings.TrimSpace(hub.URL), "/")
	if hubURL == "" {
		hubURL = "https://registry.hub.docker.com"
	}
	enableMirror := len(clean) > 0
	mirrorArg := clean
	if !enableMirror {
		mirrorArg = []string{}
	}

	params := map[string]string{
		"oldname":                jsonParam(hub.Name),
		"name":                   jsonParam(hub.Name),
		"url":                    jsonParam(hubURL),
		"enable_trust_SSC":       jsonParam(hub.EnableTrustSSC),
		"enable_registry_mirror": jsonParam(enableMirror),
		"mirror_urls":            jsonParam(mirrorArg),
		"handle_mirror":          jsonParam(true),
		"username":               jsonParam(hub.Username),
		"password":               jsonParam(""),
	}
	if err := c.call(ctx, "SYNO.Docker.Registry", "1", "set", params, nil); err != nil {
		return false, err
	}
	return true, nil
}

func equalStrSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

var synoDockerdJSONCandidates = []string{
	"/var/packages/ContainerManager/etc/dockerd.json",
	"/var/packages/Docker/etc/dockerd.json",
}

var synoRegistryJSONCandidates = []string{
	"/var/packages/ContainerManager/etc/registry.json",
	"/var/packages/Docker/etc/registry.json",
}

// GetDockerHubMirrors 读取群晖设备上当前生效的 Docker Hub 镜像加速地址。
func (a *Adapter) GetDockerHubMirrors(ctx context.Context) ([]string, error) {
	if a.dsmCli != nil {
		if urls, err := a.dsmCli.GetDockerHubMirrors(ctx); err == nil {
			return urls, nil
		} else if a.tr == nil {
			return nil, err
		}
	}
	if a.tr != nil {
		for _, p := range synoDockerdJSONCandidates {
			if b, err := a.tr.ReadFile(ctx, p); err == nil && len(bytes.TrimSpace(b)) > 0 {
				if urls, ok := docker.ExtractMirrorsFromDaemonJSON(b); ok {
					return urls, nil
				}
			}
		}
		if a.dock != nil {
			return a.dock.GetDockerHubMirrors(ctx)
		}
	}
	return nil, nil
}

// ApplyMirrorPairs 将镜像源配对写入群晖设备：
//   - 若处于 SSH/Local 传输模式，直接原子更新 dockerd.json 与 registry.json 并发送 SIGHUP 热重载（不重启运行中容器）；
//   - 若处于纯 DSM WebAPI 模式，调用群晖官方 SYNO.Docker.Registry set 接口生效。
func (a *Adapter) ApplyMirrorPairs(ctx context.Context, pairs []platform.MirrorPair) (bool, error) {
	normalized := docker.NormalizeMirrorPairs(pairs)
	var dockerHubURLs []string
	for _, p := range normalized {
		if p.Source == "docker.io" {
			dockerHubURLs = p.URLs
			break
		}
	}

	if a.tr != nil {
		if execTr, ok := a.tr.(transport.ExecutableTransport); ok {
			var targetDockerdPath string
			var existingBytes []byte
			var readAccessErr error
			for _, p := range synoDockerdJSONCandidates {
				b, err := a.tr.ReadFile(ctx, p)
				if err == nil && len(bytes.TrimSpace(b)) > 0 {
					targetDockerdPath = p
					existingBytes = b
					break
				}
				if err != nil && !transport.IsNotExistError(err) && readAccessErr == nil {
					readAccessErr = err
				}
			}
			if targetDockerdPath != "" {
				prevURLs, prevValid := docker.ExtractMirrorsFromDaemonJSON(existingBytes)
				if !prevValid || !equalStrSlices(prevURLs, dockerHubURLs) {
					newBytes, _, err := docker.UpdateDaemonJSONMirrors(existingBytes, dockerHubURLs)
					if err != nil {
						return false, err
					}
					if err := execTr.WriteFileAtomic(ctx, targetDockerdPath, newBytes, ""); err != nil {
						return false, err
					}
					// 同步更新群晖 registry.json 中的展示列表，使群晖后台 UI 显示一致
					for _, rp := range synoRegistryJSONCandidates {
						if rb, rErr := a.tr.ReadFile(ctx, rp); rErr == nil && len(bytes.TrimSpace(rb)) > 0 {
							if updatedReg, uOk := updateSynoRegistryJSONMirrors(rb, dockerHubURLs); uOk {
								_ = execTr.WriteFileAtomic(ctx, rp, updatedReg, "")
							}
							break
						}
					}
					if mcTr, ok := a.tr.(transport.MirrorConfigTransport); ok {
						if err := mcTr.ReloadDockerDaemon(ctx); err != nil {
							return false, err
						}
					}
				}
				return false, nil
			}
			if readAccessErr != nil {
				if a.dsmCli != nil {
					return a.dsmCli.SetDockerHubMirrors(ctx, dockerHubURLs)
				}
				return false, readAccessErr
			}
			if a.dock != nil {
				return a.dock.ApplyMirrorPairs(ctx, normalized)
			}
		}
	}

	if a.dsmCli != nil {
		return a.dsmCli.SetDockerHubMirrors(ctx, dockerHubURLs)
	}
	return false, fmt.Errorf("no writable channel available for Synology mirror configuration")
}

func updateSynoRegistryJSONMirrors(raw []byte, urls []string) ([]byte, bool) {
	var root map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(raw), &root); err != nil {
		return nil, false
	}
	regs, ok := root["registries"].([]any)
	if !ok {
		return nil, false
	}
	clean := make([]string, 0, len(urls))
	for _, u := range urls {
		if cu := docker.NormalizeMirrorURL(u); cu != "" {
			clean = append(clean, cu)
		}
	}
	changed := false
	for _, item := range regs {
		m, isMap := item.(map[string]any)
		if !isMap {
			continue
		}
		syno, _ := m["syno"].(bool)
		name, _ := m["name"].(string)
		if syno || strings.EqualFold(strings.TrimSpace(name), "Docker Hub") {
			m["enable_registry_mirror"] = len(clean) > 0
			m["mirror_urls"] = clean
			changed = true
			break
		}
	}
	if !changed {
		return nil, false
	}
	out, err := json.Marshal(root)
	if err != nil {
		return nil, false
	}
	return out, true
}

// NewHostHTTPClient 返回经由当前群晖主机底层传输发起请求的 HTTP 客户端。
func (a *Adapter) NewHostHTTPClient(ctx context.Context) (*http.Client, func()) {
	if a != nil && a.dock != nil {
		return a.dock.NewHostHTTPClient(ctx)
	}
	if a != nil && a.tr != nil {
		if mcTr, ok := a.tr.(transport.MirrorConfigTransport); ok {
			return mcTr.NewHostHTTPClient(ctx)
		}
	}
	return transport.DefaultMirrorHTTPClient()
}

// TestMirrorURLs 并发测试候选加速镜像网址的有效性、拉取能力与延迟。
func (a *Adapter) TestMirrorURLs(ctx context.Context, req platform.MirrorProbeRequest) ([]platform.MirrorTestItem, error) {
	if a.dock != nil {
		return a.dock.TestMirrorURLs(ctx, req)
	}
	hc, cleanup := a.NewHostHTTPClient(ctx)
	defer cleanup()
	return docker.RunMirrorProbeBatch(ctx, hc, req), nil
}
