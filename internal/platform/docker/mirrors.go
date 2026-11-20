package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/kukushouhou/DKStore/internal/platform"
	"github.com/kukushouhou/DKStore/internal/transport"
)

const defaultDaemonJSONPath = "/etc/docker/daemon.json"

type dockerInfoMirrorResp struct {
	DriverStatus   [][2]string `json:"DriverStatus"`
	RegistryConfig struct {
		Mirrors []string `json:"Mirrors"`
	} `json:"RegistryConfig"`
}

// NormalizeRegistrySource 归一化源仓库域名（去除协议头与尾部斜杠，官方仓库统一归为 docker.io）。
func NormalizeRegistrySource(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	if idx := strings.IndexByte(s, '/'); idx >= 0 {
		s = s[:idx]
	}
	s = strings.TrimSpace(s)
	switch s {
	case "index.docker.io", "registry-1.docker.io", "registry.hub.docker.com":
		return "docker.io"
	default:
		return s
	}
}

// NormalizeMirrorURL 归一化加速镜像网址（自动补全 https:// 并去除尾部斜杠）。
func NormalizeMirrorURL(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimRight(s, "/")
	if s == "" {
		return ""
	}
	lower := strings.ToLower(s)
	if !strings.HasPrefix(lower, "https://") && !strings.HasPrefix(lower, "http://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return ""
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/")
}

// NormalizeMirrorPairs 清洗并去重配对列表（保留用户排列顺序）。
func NormalizeMirrorPairs(in []platform.MirrorPair) []platform.MirrorPair {
	var order []string
	bySource := make(map[string][]string)
	seenURL := make(map[string]map[string]bool)

	for _, p := range in {
		src := NormalizeRegistrySource(p.Source)
		if src == "" {
			continue
		}
		if _, exists := bySource[src]; !exists {
			order = append(order, src)
			bySource[src] = nil
			seenURL[src] = make(map[string]bool)
		}
		for _, rawURL := range p.URLs {
			u := NormalizeMirrorURL(rawURL)
			if u == "" || seenURL[src][u] {
				continue
			}
			seenURL[src][u] = true
			bySource[src] = append(bySource[src], u)
		}
	}

	out := make([]platform.MirrorPair, 0, len(order))
	for _, src := range order {
		urls := bySource[src]
		if len(urls) == 0 && src != "docker.io" {
			continue
		}
		out = append(out, platform.MirrorPair{
			Source: src,
			URLs:   urls,
		})
	}
	return out
}

// ExtractMirrorsFromDaemonJSON 从 daemon.json 内容提取 registry-mirrors 列表。
func ExtractMirrorsFromDaemonJSON(raw []byte) ([]string, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, true
	}
	var obj map[string]any
	if err := json.Unmarshal(trimmed, &obj); err != nil {
		return nil, false
	}
	v, ok := obj["registry-mirrors"]
	if !ok || v == nil {
		return nil, true
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, true
	}
	var out []string
	seen := make(map[string]bool)
	for _, item := range arr {
		if s, isStr := item.(string); isStr {
			u := NormalizeMirrorURL(s)
			if u != "" && !seen[u] {
				seen[u] = true
				out = append(out, u)
			}
		}
	}
	return out, true
}

// UpdateDaemonJSONMirrors 在完整保留原有其他字段的前提下更新 daemon.json 中的 registry-mirrors。
func UpdateDaemonJSONMirrors(existing []byte, urls []string) ([]byte, bool, error) {
	obj := make(map[string]any)
	trimmed := bytes.TrimSpace(existing)
	if len(trimmed) > 0 {
		if err := json.Unmarshal(trimmed, &obj); err != nil {
			return nil, false, fmt.Errorf("invalid %s JSON: %w", defaultDaemonJSONPath, err)
		}
	}

	clean := make([]string, 0, len(urls))
	seen := make(map[string]bool, len(urls))
	for _, raw := range urls {
		u := NormalizeMirrorURL(raw)
		if u != "" && !seen[u] {
			seen[u] = true
			clean = append(clean, u)
		}
	}

	if len(clean) > 0 {
		obj["registry-mirrors"] = clean
	} else {
		delete(obj, "registry-mirrors")
	}

	hasContainerdStore := false
	if feat, ok := obj["features"].(map[string]any); ok {
		if b, isBool := feat["containerd-snapshotter"].(bool); isBool && b {
			hasContainerdStore = true
		}
	}

	encoded, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return nil, false, err
	}
	encoded = append(encoded, '\n')
	return encoded, hasContainerdStore, nil
}

// GetDockerHubMirrors 读取目标 Docker 宿主机当前配置的 docker.io 镜像加速源列表。
func (a *Adapter) GetDockerHubMirrors(ctx context.Context) ([]string, error) {
	if b, err := a.tr.ReadFile(ctx, defaultDaemonJSONPath); err == nil && len(bytes.TrimSpace(b)) > 0 {
		if urls, ok := ExtractMirrorsFromDaemonJSON(b); ok {
			return urls, nil
		}
	}
	var info dockerInfoMirrorResp
	if err := a.getJSON(ctx, "/info", &info); err == nil {
		var out []string
		seen := make(map[string]bool)
		for _, raw := range info.RegistryConfig.Mirrors {
			u := NormalizeMirrorURL(raw)
			if u != "" && !seen[u] {
				seen[u] = true
				out = append(out, u)
			}
		}
		return out, nil
	}
	return nil, nil
}

func buildHostsTOML(source string, mirrorURLs []string) []byte {
	var sb strings.Builder
	upstream := "https://" + source
	if source == "docker.io" {
		upstream = "https://registry-1.docker.io"
	}
	sb.WriteString(fmt.Sprintf("server = %q\n", upstream))
	for _, u := range mirrorURLs {
		sb.WriteString(fmt.Sprintf("\n[host.%q]\n  capabilities = [\"pull\", \"resolve\"]\n", u))
	}
	return []byte(sb.String())
}

// ApplyMirrorPairs 将配对列表应用到当前 Docker 宿主机：
// docker.io 写入 /etc/docker/daemon.json 并热重载 dockerd；若宿主机启用了 containerd 镜像存储，同时将自定义源仓库写入 /etc/docker/certs.d/<source>/hosts.toml。
func (a *Adapter) ApplyMirrorPairs(ctx context.Context, pairs []platform.MirrorPair) (bool, error) {
	execTr, ok := a.tr.(transport.ExecutableTransport)
	if !ok {
		return false, fmt.Errorf("transport does not support writing mirror configuration")
	}

	normalized := NormalizeMirrorPairs(pairs)
	var dockerHubURLs []string
	customPairs := make(map[string][]string)
	for _, p := range normalized {
		if p.Source == "docker.io" {
			dockerHubURLs = p.URLs
		} else if len(p.URLs) > 0 {
			customPairs[p.Source] = p.URLs
		}
	}

	existingBytes, _ := a.tr.ReadFile(ctx, defaultDaemonJSONPath)
	prevURLs, prevValid := ExtractMirrorsFromDaemonJSON(existingBytes)
	newBytes, hasContainerdInJSON, err := UpdateDaemonJSONMirrors(existingBytes, dockerHubURLs)
	if err != nil {
		return false, err
	}

	hasContainerd := hasContainerdInJSON
	if !hasContainerd {
		var info dockerInfoMirrorResp
		if a.getJSON(ctx, "/info", &info) == nil {
			for _, kv := range info.DriverStatus {
				if strings.Contains(strings.ToLower(kv[0]), "driver-type") && strings.Contains(strings.ToLower(kv[1]), "containerd") {
					hasContainerd = true
					break
				}
			}
		}
	}

	if !prevValid || !equalStringSlices(prevURLs, dockerHubURLs) {
		if err := execTr.WriteFileAtomic(ctx, defaultDaemonJSONPath, newBytes, ""); err != nil {
			return false, err
		}
		if mcTr, ok := a.tr.(transport.MirrorConfigTransport); ok {
			if err := mcTr.ReloadDockerDaemon(ctx); err != nil {
				return false, err
			}
		}
	}

	if hasContainerd {
		for src, urls := range customPairs {
			if !isSafeRegistryHostForCertsDir(src) {
				continue
			}
			tomlPath := "/etc/docker/certs.d/" + src + "/hosts.toml"
			_ = execTr.WriteFileAtomic(ctx, tomlPath, buildHostsTOML(src, urls), "")
		}
	}
	return false, nil
}

func isSafeRegistryHostForCertsDir(host string) bool {
	if host == "" || strings.Contains(host, "/") || strings.Contains(host, "\\") || strings.Contains(host, "..") {
		return false
	}
	return true
}

func equalStringSlices(a, b []string) bool {
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

// NewHostHTTPClient 返回经由当前适配器底层传输通道（本机或远端 SSH 宿主机代理环境）发起请求的 HTTP 客户端。
func (a *Adapter) NewHostHTTPClient(ctx context.Context) (*http.Client, func()) {
	if a != nil && a.tr != nil {
		if mcTr, ok := a.tr.(transport.MirrorConfigTransport); ok {
			return mcTr.NewHostHTTPClient(ctx)
		}
	}
	return transport.DefaultMirrorHTTPClient()
}

// RunMirrorProbeBatch 并发执行一组加速网址的连通与镜像拉取验证，并在自动多候选模式下处理旧标签 404 顺延与跨源存在性交叉复核。
func RunMirrorProbeBatch(ctx context.Context, hc *http.Client, req platform.MirrorProbeRequest) []platform.MirrorTestItem {
	cleaned := make([]string, 0, len(req.URLs))
	seen := make(map[string]bool, len(req.URLs))
	for _, raw := range req.URLs {
		u := NormalizeMirrorURL(raw)
		if u != "" && !seen[u] {
			seen[u] = true
			cleaned = append(cleaned, u)
		}
	}
	out := make([]platform.MirrorTestItem, len(cleaned))
	if len(cleaned) == 0 {
		return out
	}

	source := NormalizeRegistrySource(req.Source)
	if source == "" {
		source = "docker.io"
	}
	candidates := req.Candidates
	if len(candidates) == 0 {
		candidates = []platform.MirrorProbeCandidate{{}}
	}

	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, u := range cleaned {
		wg.Add(1)
		go func(idx int, targetURL string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			var lastItem platform.MirrorTestItem
			for cIdx, cand := range candidates {
				lastItem = transport.ProbeMirrorPullWithClient(ctx, hc, targetURL, source, cand, req.HasSpecificApp)
				// 仅当存在多个自动候选镜像且当前返回 404（可能旧标签已下架）时，自动顺延测试下一个候选镜像
				if lastItem.Status == "not_found" && cIdx+1 < len(candidates) {
					continue
				}
				break
			}
			out[idx] = lastItem
		}(i, u)
	}
	wg.Wait()
	return out
}

// TestMirrorURLs 在目标 Docker 宿主机网络环境下并发验证各加速镜像网址的连通与真实拉取能力。
func (a *Adapter) TestMirrorURLs(ctx context.Context, req platform.MirrorProbeRequest) ([]platform.MirrorTestItem, error) {
	hc, cleanup := a.NewHostHTTPClient(ctx)
	defer cleanup()
	return RunMirrorProbeBatch(ctx, hc, req), nil
}

// SplitImageRegistryAndRepoPath 拆分镜像引用的源仓库域名（默认 docker.io）、仓库路径与标签。
func SplitImageRegistryAndRepoPath(imageRef string) (regHost, repoPath, tag string) {
	return splitImageRegistryAndRepoPath(imageRef)
}

// splitImageRegistryAndRepoPath 拆分镜像引用的源仓库域名（默认 docker.io）、仓库路径与标签。
func splitImageRegistryAndRepoPath(imageRef string) (regHost, repoPath, tag string) {
	base, t := splitPullImageAndTag(strings.TrimSpace(imageRef))
	if t == "" {
		t = "latest"
	}
	first, rest, hasSlash := strings.Cut(base, "/")
	if !hasSlash {
		return "docker.io", "library/" + base, t
	}
	if strings.Contains(first, ".") || strings.Contains(first, ":") || strings.EqualFold(first, "localhost") {
		norm := NormalizeRegistrySource(first)
		if norm == "docker.io" && !strings.Contains(rest, "/") {
			rest = "library/" + rest
		}
		return norm, rest, t
	}
	return "docker.io", base, t
}

func buildMirrorPullRef(mirrorURL, repoPath, tag string) string {
	u, err := url.Parse(NormalizeMirrorURL(mirrorURL))
	if err != nil || u.Host == "" {
		return ""
	}
	prefix := strings.TrimRight(u.Host+u.Path, "/")
	ref := prefix + "/" + strings.TrimLeft(repoPath, "/")
	if tag != "" {
		ref += ":" + tag
	}
	return ref
}

func (a *Adapter) retagAndCleanupMirrorImage(ctx context.Context, execTr transport.ExecutableTransport, mirrorRef, targetRef string) error {
	targetRepo, targetTag := splitPullImageAndTag(targetRef)
	if targetTag == "" {
		targetTag = "latest"
	}
	q := url.Values{}
	q.Set("repo", targetRepo)
	q.Set("tag", targetTag)
	tagPath := fmt.Sprintf("/images/%s/tag?%s", url.PathEscape(mirrorRef), q.Encode())
	resp, err := execTr.RequestDocker(ctx, http.MethodPost, tagPath, nil, nil)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("docker tag HTTP %d", resp.StatusCode)
	}
	if mirrorRef != targetRef {
		delPath := fmt.Sprintf("/images/%s?noprune=1", url.PathEscape(mirrorRef))
		if dResp, dErr := execTr.RequestDocker(ctx, http.MethodDelete, delPath, nil, nil); dErr == nil && dResp != nil {
			_ = dResp.Body.Close()
		}
	}
	return nil
}

func (a *Adapter) pullImageWithMirrorPairs(
	ctx context.Context,
	execTr transport.ExecutableTransport,
	targetRef string,
	registryAuthB64 string,
	layerSizes map[string]int64,
	mirrorPairs []platform.MirrorPair,
	emit func(platform.PullProgress),
) (imageID, repoDigest string, err error) {
	regHost, repoPath, tag := splitImageRegistryAndRepoPath(targetRef)
	// docker.io 已直接写入宿主机 daemon.json registry-mirrors；此处专门为其他源仓库（如 ghcr.io / quay.io / lscr.io）执行配对代拉与自动还原原标签
	if regHost != "" && regHost != "docker.io" && len(mirrorPairs) > 0 {
		var candidateURLs []string
		for _, p := range mirrorPairs {
			if NormalizeRegistrySource(p.Source) == regHost {
				candidateURLs = append(candidateURLs, p.URLs...)
			}
		}
		for _, mURL := range candidateURLs {
			mClean := NormalizeMirrorURL(mURL)
			if mClean == "" {
				continue
			}
			mirrorRef := buildMirrorPullRef(mClean, repoPath, tag)
			if mirrorRef == "" || mirrorRef == targetRef {
				continue
			}
			if emit != nil {
				emit(platform.PullProgress{
					Phase:      "pulling",
					StatusText: "Pulling via mirror " + mClean,
					LogChunk:   fmt.Sprintf("Using mirror %s -> %s ...\r\n", regHost, mClean),
				})
			}
			_, _, mErr := a.pullImageStreamWithSizes(ctx, execTr, mirrorRef, "", layerSizes, emit)
			if mErr == nil {
				if tagErr := a.retagAndCleanupMirrorImage(ctx, execTr, mirrorRef, targetRef); tagErr == nil {
					fromImg, _ := splitPullImageAndTag(targetRef)
					var insp imageInspect
					if iErr := a.getJSON(ctx, "/images/"+url.PathEscape(targetRef)+"/json", &insp); iErr == nil && insp.ID != "" {
						return insp.ID, pickDigestForRepo(fromImg, insp.RepoDigests), nil
					}
				}
			} else if emit != nil {
				emit(platform.PullProgress{
					Phase:      "pulling",
					StatusText: "Mirror failed, trying next source",
					LogChunk:   fmt.Sprintf("\x1b[33m! 加速源 %s 拉取失败，尝试下一来源...\x1b[0m\r\n", mClean),
				})
			}
		}
	}
	return a.pullImageStreamWithSizes(ctx, execTr, targetRef, registryAuthB64, layerSizes, emit)
}

