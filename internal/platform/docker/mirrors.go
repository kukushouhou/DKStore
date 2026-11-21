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
		if len(urls) == 0 {
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

// ExtractMirrorsFromOpenWrtUCI 从 OpenWrt / iStoreOS 的 /etc/config/dockerd 中提取 globals 段的 registry_mirrors 列表。
func ExtractMirrorsFromOpenWrtUCI(raw []byte) []string {
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	inGlobals := false
	var out []string
	seen := make(map[string]bool)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) >= 2 && fields[0] == "config" {
			secType := strings.Trim(fields[1], `"'`)
			secName := ""
			if len(fields) >= 3 {
				secName = strings.Trim(fields[2], `"'`)
			}
			inGlobals = secType == "globals" || secName == "globals"
			continue
		}
		if !inGlobals || len(fields) < 3 {
			continue
		}
		if (fields[0] == "list" || fields[0] == "option") && fields[1] == "registry_mirrors" {
			valIdx := strings.Index(trimmed, "registry_mirrors")
			valRaw := strings.TrimSpace(trimmed[valIdx+len("registry_mirrors"):])
			valRaw = strings.Trim(valRaw, `"'`)
			if u := NormalizeMirrorURL(valRaw); u != "" && !seen[u] {
				seen[u] = true
				out = append(out, u)
			}
		}
	}
	return out
}

// UpdateOpenWrtDockerdUCI 在保留 /etc/config/dockerd 其余配置与注释的前提下更新 globals 段的 registry_mirrors。
func UpdateOpenWrtDockerdUCI(existing []byte, urls []string) ([]byte, bool) {
	var clean []string
	seen := make(map[string]bool, len(urls))
	for _, raw := range urls {
		if u := NormalizeMirrorURL(raw); u != "" && !seen[u] {
			seen[u] = true
			clean = append(clean, u)
		}
	}
	prev := ExtractMirrorsFromOpenWrtUCI(existing)
	if equalStringSlices(prev, clean) {
		return existing, false
	}

	rawStr := strings.ReplaceAll(string(existing), "\r\n", "\n")
	lines := strings.Split(rawStr, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	buildListLines := func() []string {
		res := make([]string, 0, len(clean))
		for _, u := range clean {
			escaped := strings.ReplaceAll(u, "'", "'\\''")
			res = append(res, "\tlist registry_mirrors '"+escaped+"'")
		}
		return res
	}

	var out []string
	inGlobals := false
	foundGlobals := false
	inserted := false

	flushGlobalsMirrors := func() {
		if inGlobals && !inserted {
			out = append(out, buildListLines()...)
			inserted = true
		}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		fields := strings.Fields(trimmed)
		if !strings.HasPrefix(trimmed, "#") && len(fields) >= 2 && fields[0] == "config" {
			if inGlobals {
				flushGlobalsMirrors()
			}
			secType := strings.Trim(fields[1], `"'`)
			secName := ""
			if len(fields) >= 3 {
				secName = strings.Trim(fields[2], `"'`)
			}
			inGlobals = secType == "globals" || secName == "globals"
			if inGlobals {
				foundGlobals = true
			}
			out = append(out, line)
			continue
		}
		if inGlobals && !strings.HasPrefix(trimmed, "#") && len(fields) >= 2 &&
			(fields[0] == "list" || fields[0] == "option") && fields[1] == "registry_mirrors" {
			if !inserted {
				out = append(out, buildListLines()...)
				inserted = true
			}
			continue
		}
		out = append(out, line)
	}
	if inGlobals {
		flushGlobalsMirrors()
	}
	if !foundGlobals && len(clean) > 0 {
		if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
			out = append(out, "")
		}
		out = append(out, "config globals 'globals'")
		out = append(out, buildListLines()...)
	}
	encoded := strings.Join(out, "\n") + "\n"
	return []byte(encoded), true
}

// GetDockerHubMirrors 读取目标 Docker 宿主机当前配置的 docker.io 镜像加速源列表。
func (a *Adapter) GetDockerHubMirrors(ctx context.Context) ([]string, error) {
	if dcTr, ok := a.tr.(transport.DaemonConfigTransport); ok {
		if st, err := dcTr.InspectDockerDaemonConfig(ctx); err == nil && st != nil {
			if st.Exists && len(bytes.TrimSpace(st.ConfigBytes)) > 0 {
				if urls, valid := ExtractMirrorsFromDaemonJSON(st.ConfigBytes); valid {
					if len(urls) > 0 || !st.HasOpenWrtUCI {
						return urls, nil
					}
				}
			}
			if st.HasOpenWrtUCI {
				if uciURLs := ExtractMirrorsFromOpenWrtUCI(st.OpenWrtUCIBytes); len(uciURLs) > 0 {
					return uciURLs, nil
				}
			}
			if st.DefaultJSONExists && len(bytes.TrimSpace(st.DefaultJSONBytes)) > 0 {
				if urls, valid := ExtractMirrorsFromDaemonJSON(st.DefaultJSONBytes); valid && len(urls) > 0 {
					return urls, nil
				}
			}
		}
	}
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
	sb.WriteString("# Managed by DKStore\n")
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

func (a *Adapter) hasContainerdSnapshotter(ctx context.Context, hasInJSON bool) bool {
	if hasInJSON {
		return true
	}
	var info dockerInfoMirrorResp
	if a.getJSON(ctx, "/info", &info) == nil {
		for _, kv := range info.DriverStatus {
			if strings.Contains(strings.ToLower(kv[0]), "driver-type") && strings.Contains(strings.ToLower(kv[1]), "containerd") {
				return true
			}
		}
	}
	return false
}

// ApplyMirrorPairs 将配对列表应用到当前 Docker 宿主机：
// 自动识别 dockerd 实际配置文件（兼容飞牛 fnOS data-root 保留、OpenWrt /etc/config/dockerd 与容器部署穿透宿主机）并热重载 dockerd；
// 若宿主机启用了 containerd 镜像存储，同步维护与清理 /etc/docker/certs.d/<source>/hosts.toml。
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

	if dcTr, ok := a.tr.(transport.DaemonConfigTransport); ok {
		st, err := dcTr.InspectDockerDaemonConfig(ctx)
		if err != nil {
			return false, err
		}
		prevURLs, prevValid := ExtractMirrorsFromDaemonJSON(st.ConfigBytes)
		newActiveBytes, hasContainerdInJSON, err := UpdateDaemonJSONMirrors(st.ConfigBytes, dockerHubURLs)
		if err != nil {
			return false, err
		}
		hasContainerd := a.hasContainerdSnapshotter(ctx, hasContainerdInJSON)

		needWriteActive := false
		if st.Exists {
			needWriteActive = !prevValid || !equalStringSlices(prevURLs, dockerHubURLs)
		} else if len(dockerHubURLs) > 0 {
			needWriteActive = true
		}

		needWriteDef := false
		var newDefBytes []byte
		if st.DefaultJSONExists && st.ActiveConfigPath != defaultDaemonJSONPath {
			defPrev, defValid := ExtractMirrorsFromDaemonJSON(st.DefaultJSONBytes)
			if !defValid || !equalStringSlices(defPrev, dockerHubURLs) {
				if updatedDef, _, dErr := UpdateDaemonJSONMirrors(st.DefaultJSONBytes, dockerHubURLs); dErr == nil {
					needWriteDef = true
					newDefBytes = updatedDef
				}
			}
		}

		needWriteUCI := false
		var newUCIBytes []byte
		if st.HasOpenWrtUCI {
			if updatedUCI, changed := UpdateOpenWrtDockerdUCI(st.OpenWrtUCIBytes, dockerHubURLs); changed {
				needWriteUCI = true
				newUCIBytes = updatedUCI
			}
		}

		var certTOMLs map[string][]byte
		if hasContainerd {
			certTOMLs = make(map[string][]byte, len(customPairs))
			for src, urls := range customPairs {
				if isSafeRegistryHostForCertsDir(src) {
					certTOMLs[src] = buildHostsTOML(src, urls)
				}
			}
		}

		if needWriteActive || needWriteDef || needWriteUCI || hasContainerd {
			plan := transport.ApplyDaemonMirrorPlan{
				WriteActiveConfig:   needWriteActive,
				ActiveConfigPath:    st.ActiveConfigPath,
				ActiveConfigBytes:   newActiveBytes,
				WriteDefaultJSON:    needWriteDef,
				DefaultJSONBytes:    newDefBytes,
				WriteOpenWrtUCI:     needWriteUCI,
				OpenWrtUCIBytes:     newUCIBytes,
				ReloadDaemon:        needWriteActive || needWriteUCI,
				ManageCertsDir:      hasContainerd,
				CustomCertHostsTOML: certTOMLs,
			}
			if err := dcTr.ApplyDockerDaemonConfig(ctx, plan); err != nil {
				return false, err
			}
		}
		return false, nil
	}

	existingBytes, readErr := a.tr.ReadFile(ctx, defaultDaemonJSONPath)
	if readErr != nil && !transport.IsNotExistError(readErr) {
		return false, readErr
	}
	prevURLs, prevValid := ExtractMirrorsFromDaemonJSON(existingBytes)
	newBytes, hasContainerdInJSON, err := UpdateDaemonJSONMirrors(existingBytes, dockerHubURLs)
	if err != nil {
		return false, err
	}
	hasContainerd := a.hasContainerdSnapshotter(ctx, hasContainerdInJSON)

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
			if err := execTr.WriteFileAtomic(ctx, tomlPath, buildHostsTOML(src, urls), ""); err != nil {
				return false, err
			}
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

// splitImageRegistryAndRepoPath 拆分镜像引用的源仓库域名（默认 docker.io）、仓库路径与标签（兼容 tag@sha256:... 格式）。
func splitImageRegistryAndRepoPath(imageRef string) (regHost, repoPath, tag string) {
	raw := strings.TrimSpace(imageRef)
	beforeAt, digestPart, hasAt := strings.Cut(raw, "@")
	beforeAt = strings.TrimSpace(beforeAt)
	digestPart = strings.TrimSpace(digestPart)

	base := beforeAt
	t := ""
	lastSlash := strings.LastIndexByte(beforeAt, '/')
	lastColon := strings.LastIndexByte(beforeAt, ':')
	if lastColon > lastSlash {
		base = strings.TrimSpace(beforeAt[:lastColon])
		t = strings.TrimSpace(beforeAt[lastColon+1:])
	}
	if t == "" {
		if hasAt && strings.HasPrefix(strings.ToLower(digestPart), "sha256:") {
			t = digestPart
		} else {
			t = "latest"
		}
	}
	if base == "" {
		return "", "", ""
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
	if strings.HasPrefix(strings.ToLower(tag), "sha256:") {
		ref += "@" + tag
	} else if tag != "" {
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

