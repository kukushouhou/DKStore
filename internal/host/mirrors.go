package host

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/kukushouhou/DKStore/internal/i18n"
	"github.com/kukushouhou/DKStore/internal/platform"
	"github.com/kukushouhou/DKStore/internal/platform/docker"
	"github.com/kukushouhou/DKStore/internal/transport"
)

func countMirrorURLs(pairs []platform.MirrorPair) int {
	total := 0
	for _, p := range pairs {
		for _, u := range p.URLs {
			if strings.TrimSpace(u) != "" {
				total++
			}
		}
	}
	return total
}

func ensureDockerHubFirst(pairs []platform.MirrorPair, liveHubURLs []string, hasLiveHub bool) []platform.MirrorPair {
	norm := docker.NormalizeMirrorPairs(pairs)
	var hubURLs []string
	var others []platform.MirrorPair
	for _, p := range norm {
		if p.Source == "docker.io" {
			hubURLs = p.URLs
		} else if len(p.URLs) > 0 {
			others = append(others, p)
		}
	}
	if hasLiveHub {
		hubURLs = nil
		seen := make(map[string]bool)
		for _, raw := range liveHubURLs {
			u := docker.NormalizeMirrorURL(raw)
			if u != "" && !seen[u] {
				seen[u] = true
				hubURLs = append(hubURLs, u)
			}
		}
	}
	out := make([]platform.MirrorPair, 0, 1+len(others))
	if len(hubURLs) > 0 {
		out = append(out, platform.MirrorPair{
			Source: "docker.io",
			URLs:   hubURLs,
		})
	}
	out = append(out, others...)
	return out
}

func (r *Registry) populateMirrorCounts(ctx context.Context, hosts []Host) {
	if r == nil || r.st == nil || r.st.DB == nil || len(hosts) == 0 {
		return
	}
	rows, err := r.st.DB.QueryContext(ctx, `SELECT host_id, pairs_json FROM host_mirror`)
	if err != nil {
		return
	}
	counts := make(map[string]int, len(hosts))
	recorded := make(map[string]bool, len(hosts))
	for rows.Next() {
		var hid, raw string
		if rows.Scan(&hid, &raw) == nil {
			recorded[hid] = true
			var pairs []platform.MirrorPair
			if json.Unmarshal([]byte(raw), &pairs) == nil {
				counts[hid] = countMirrorURLs(pairs)
			}
		}
	}
	_ = rows.Close()

	var missingOnline []string
	for i := range hosts {
		id := hosts[i].ID
		hosts[i].MirrorCount = counts[id]
		if !recorded[id] && hosts[i].Status == "online" {
			missingOnline = append(missingOnline, id)
		}
	}
	if len(missingOnline) > 0 {
		r.syncMissingHostMirrorsAsync(missingOnline)
	}
}

func (r *Registry) syncMissingHostMirrorsAsync(hostIDs []string) {
	r.rtMu.Lock()
	if !r.bgEnabled || r.bgCtx == nil || r.bgCtx.Err() != nil {
		r.rtMu.Unlock()
		return
	}
	var todo []string
	for _, id := range hostIDs {
		if !r.mirrorSyncing[id] {
			r.mirrorSyncing[id] = true
			todo = append(todo, id)
		}
	}
	if len(todo) == 0 {
		r.rtMu.Unlock()
		return
	}
	parentCtx := r.bgCtx
	r.bgWg.Add(1)
	r.rtMu.Unlock()

	go func() {
		defer func() {
			r.rtMu.Lock()
			for _, id := range todo {
				delete(r.mirrorSyncing, id)
			}
			r.rtMu.Unlock()
			r.bgWg.Done()
		}()
		for _, id := range todo {
			sCtx, cancel := context.WithTimeout(parentCtx, 8*time.Second)
			_, _ = r.GetHostMirrors(sCtx, id)
			cancel()
		}
	}()
}

// LoadCachedHostMirrors 从本地 SQLite 快照读取指定设备已记录的镜像源配对（零网络阻塞，供更新任务执行与错误归因使用）。
func (r *Registry) LoadCachedHostMirrors(ctx context.Context, hostID string) []platform.MirrorPair {
	if r == nil || r.st == nil || r.st.DB == nil || strings.TrimSpace(hostID) == "" {
		return nil
	}
	var raw string
	if err := r.st.DB.QueryRowContext(ctx, `SELECT pairs_json FROM host_mirror WHERE host_id=?`, hostID).Scan(&raw); err != nil {
		return nil
	}
	var pairs []platform.MirrorPair
	if json.Unmarshal([]byte(raw), &pairs) != nil {
		return nil
	}
	return docker.NormalizeMirrorPairs(pairs)
}

func (r *Registry) saveCachedHostMirrors(ctx context.Context, hostID string, pairs []platform.MirrorPair) error {
	if r == nil || r.st == nil || r.st.DB == nil {
		return nil
	}
	b, err := json.Marshal(pairs)
	if err != nil {
		return err
	}
	_, err = r.st.DB.ExecContext(ctx, `
		INSERT INTO host_mirror(host_id, pairs_json, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(host_id) DO UPDATE SET pairs_json=excluded.pairs_json, updated_at=excluded.updated_at`,
		hostID, string(b), time.Now().Unix(),
	)
	return err
}

// GetHostMirrors 实时读取目标设备底层的 docker.io 加速地址并与已保存的其他仓库配对合并后返回。
func (r *Registry) GetHostMirrors(ctx context.Context, hostID string) ([]platform.MirrorPair, error) {
	if _, err := r.Get(ctx, hostID); err != nil {
		return nil, err
	}
	cached := r.LoadCachedHostMirrors(ctx, hostID)

	ad, _, err := r.OpenAdapter(ctx, hostID)
	if err != nil {
		if len(cached) > 0 {
			return ensureDockerHubFirst(cached, nil, false), nil
		}
		return nil, err
	}
	defer ad.Close()

	var liveHub []string
	hasLive := false
	if mAd, ok := ad.(platform.MirrorAdapter); ok {
		if urls, mErr := mAd.GetDockerHubMirrors(ctx); mErr == nil {
			liveHub = urls
			hasLive = true
		}
	}
	merged := ensureDockerHubFirst(cached, liveHub, hasLive)
	_ = r.saveCachedHostMirrors(ctx, hostID, merged)
	return merged, nil
}

func (r *Registry) applyMirrorsToHost(ctx context.Context, hostID string, pairs []platform.MirrorPair) error {
	ad, _, err := r.OpenAdapter(ctx, hostID)
	if err != nil {
		return err
	}
	defer ad.Close()

	if mAd, ok := ad.(platform.MirrorAdapter); ok {
		restarted, err := mAd.ApplyMirrorPairs(ctx, pairs)
		if err != nil {
			return err
		}
		if restarted {
			r.evictRuntimeAdapter(hostID)
			r.RefreshRuntimeAsync(hostID)
		}
	}
	return r.saveCachedHostMirrors(ctx, hostID, pairs)
}

// SaveHostMirrors 先保存并写入当前设备镜像源，再按序同步应用到所勾选的其他设备。
func (r *Registry) SaveHostMirrors(ctx context.Context, hostID string, pairs []platform.MirrorPair, alsoApplyHostIDs []string) ([]platform.MirrorPair, error) {
	if _, err := r.Get(ctx, hostID); err != nil {
		return nil, err
	}
	normalized := ensureDockerHubFirst(pairs, nil, false)
	if err := r.applyMirrorsToHost(ctx, hostID, normalized); err != nil {
		return nil, err
	}

	seen := map[string]bool{hostID: true}
	for _, peerID := range alsoApplyHostIDs {
		peerID = strings.TrimSpace(peerID)
		if peerID == "" || seen[peerID] {
			continue
		}
		seen[peerID] = true
		if err := r.applyMirrorsToHost(ctx, peerID, normalized); err != nil {
			return nil, err
		}
	}
	return normalized, nil
}

// ListHostMirrorContainers 提取指定设备上现有容器的源仓库域名与镜像路径，供镜像源测速抽检与指定验证使用。
func (r *Registry) ListHostMirrorContainers(ctx context.Context, hostID string) []platform.HostMirrorContainerItem {
	if r == nil || strings.TrimSpace(hostID) == "" {
		return nil
	}
	apps, err := r.ListCachedRuntimeContainers(ctx, hostID)
	if err != nil || len(apps) == 0 {
		lCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		apps, _ = r.ListRuntimeContainers(lCtx, hostID, false)
		cancel()
	}
	var out []platform.HostMirrorContainerItem
	seen := make(map[string]bool)
	for _, app := range apps {
		for _, c := range app.Containers {
			rawImg := strings.TrimSpace(c.Image)
			if rawImg == "" || strings.HasPrefix(rawImg, "sha256:") {
				continue
			}
			src, repoPath, tag := docker.SplitImageRegistryAndRepoPath(rawImg)
			if src == "" || repoPath == "" {
				continue
			}
			fullImg := repoPath + ":" + tag
			if strings.HasPrefix(strings.ToLower(tag), "sha256:") {
				fullImg = repoPath + "@" + tag
			}
			key := src + "|" + fullImg
			if seen[key] {
				continue
			}
			seen[key] = true
			name := strings.TrimSpace(c.Name)
			if name == "" {
				name = strings.TrimSpace(app.Name)
			}
			if name == "" {
				parts := strings.Split(repoPath, "/")
				name = parts[len(parts)-1]
			}
			out = append(out, platform.HostMirrorContainerItem{
				Name:   name,
				Source: src,
				Image:  fullImg,
			})
		}
	}
	// 优先把非 library/* 的第三方业务容器排在前面，更容易直接测出第三方白名单源的覆盖限制
	sort.SliceStable(out, func(i, j int) bool {
		iLib := strings.HasPrefix(out[i].Image, "library/")
		jLib := strings.HasPrefix(out[j].Image, "library/")
		if iLib != jLib {
			return !iLib
		}
		return false
	})
	return out
}

func parseProbeCandidateForSource(source, rawTarget string) platform.MirrorProbeCandidate {
	s := strings.TrimSpace(rawTarget)
	if s == "" {
		return platform.MirrorProbeCandidate{}
	}
	if at := strings.IndexByte(s, '@'); at >= 0 {
		s = s[:at]
	}
	tag := "latest"
	lastSlash := strings.LastIndexByte(s, '/')
	lastColon := strings.LastIndexByte(s, ':')
	if lastColon > lastSlash {
		tag = strings.TrimSpace(s[lastColon+1:])
		s = strings.TrimSpace(s[:lastColon])
		if tag == "" {
			tag = "latest"
		}
	}
	s = strings.Trim(s, "/")
	if s == "" {
		return platform.MirrorProbeCandidate{}
	}

	first, rest, hasSlash := strings.Cut(s, "/")
	if hasSlash {
		normFirst := docker.NormalizeRegistrySource(first)
		if normFirst == source || (source == "docker.io" && (first == "index.docker.io" || first == "registry-1.docker.io" || first == "docker.io")) {
			s = rest
		}
	}
	if source == "docker.io" && !strings.Contains(s, "/") {
		s = "library/" + s
	}
	return platform.MirrorProbeCandidate{
		RepoPath: s,
		Tag:      tag,
	}
}

// TestHostMirrors 从指定目标设备发起各加速镜像网址的连通、镜像拉取与延迟验证。
func (r *Registry) TestHostMirrors(
	ctx context.Context,
	hostID string,
	source string,
	urls []string,
	targetImage string,
	lang i18n.Lang,
) ([]platform.MirrorTestItem, error) {
	normSource := docker.NormalizeRegistrySource(source)
	if normSource == "" {
		normSource = "docker.io"
	}

	probeReq := platform.MirrorProbeRequest{
		Source: normSource,
		URLs:   urls,
	}
	if strings.TrimSpace(targetImage) != "" {
		if cand := parseProbeCandidateForSource(normSource, targetImage); cand.RepoPath != "" {
			probeReq.Candidates = []platform.MirrorProbeCandidate{cand}
			probeReq.HasSpecificApp = true
		}
	} else {
		locals := r.ListHostMirrorContainers(ctx, hostID)
		for _, c := range locals {
			if c.Source != normSource {
				continue
			}
			if cand := parseProbeCandidateForSource(normSource, c.Image); cand.RepoPath != "" {
				probeReq.Candidates = append(probeReq.Candidates, cand)
				if len(probeReq.Candidates) >= 3 {
					break
				}
			}
		}
		if len(probeReq.Candidates) > 0 {
			probeReq.HasSpecificApp = true
		} else if normSource == "docker.io" {
			probeReq.Candidates = []platform.MirrorProbeCandidate{
				{RepoPath: "library/alpine", Tag: "latest"},
				{RepoPath: "library/busybox", Tag: "latest"},
			}
			probeReq.HasSpecificApp = false
		}
	}

	ad, _, err := r.OpenAdapter(ctx, hostID)
	if err != nil {
		return nil, err
	}
	defer ad.Close()

	mAd, ok := ad.(platform.MirrorAdapter)
	if !ok {
		return nil, nil
	}
	items, err := mAd.TestMirrorURLs(ctx, probeReq)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if !items[i].OK && items[i].Error != "" {
			items[i].Error = i18n.HumanizeRawNetError(lang, items[i].Error)
		}
	}
	return items, nil
}

type hostHTTPClientAdapter interface {
	NewHostHTTPClient(ctx context.Context) (*http.Client, func())
}

// SearchRegistryRepositories 调用公开仓库官方搜索接口查询匹配镜像名（仅支持官方开放公开搜索的 docker.io 与 quay.io）。
func (r *Registry) SearchRegistryRepositories(ctx context.Context, hostID, source, query string) []string {
	normSource := docker.NormalizeRegistrySource(source)
	q := strings.TrimSpace(query)
	if q == "" || (normSource != "docker.io" && normSource != "quay.io") {
		return nil
	}

	sCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	trySearch := func(hc *http.Client) ([]string, bool) {
		switch normSource {
		case "docker.io":
			u := "https://hub.docker.com/v2/search/repositories/?page_size=8&query=" + url.QueryEscape(q)
			req, err := http.NewRequestWithContext(sCtx, http.MethodGet, u, nil)
			if err != nil {
				return nil, false
			}
			resp, err := hc.Do(req)
			if err != nil {
				return nil, false
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return nil, false
			}
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 128<<10))
			var parsed struct {
				Results []struct {
					RepoName string `json:"repo_name"`
				} `json:"results"`
			}
			if json.Unmarshal(body, &parsed) != nil {
				return nil, false
			}
			var out []string
			for _, item := range parsed.Results {
				if rn := strings.TrimSpace(item.RepoName); rn != "" {
					out = append(out, rn)
				}
			}
			return out, true
		case "quay.io":
			u := "https://quay.io/api/v1/find/repositories?query=" + url.QueryEscape(q)
			req, err := http.NewRequestWithContext(sCtx, http.MethodGet, u, nil)
			if err != nil {
				return nil, false
			}
			resp, err := hc.Do(req)
			if err != nil {
				return nil, false
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return nil, false
			}
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 128<<10))
			var parsed struct {
				Results []struct {
					Namespace struct {
						Name string `json:"name"`
					} `json:"namespace"`
					Name string `json:"name"`
				} `json:"results"`
			}
			if json.Unmarshal(body, &parsed) != nil {
				return nil, false
			}
			var out []string
			for _, item := range parsed.Results {
				ns := strings.TrimSpace(item.Namespace.Name)
				nm := strings.TrimSpace(item.Name)
				if ns != "" && nm != "" {
					out = append(out, ns+"/"+nm)
				}
				if len(out) >= 8 {
					break
				}
			}
			return out, true
		}
		return nil, false
	}

	if strings.TrimSpace(hostID) != "" {
		if ad, _, err := r.OpenAdapter(sCtx, hostID); err == nil {
			defer ad.Close()
			if hAd, ok := ad.(hostHTTPClientAdapter); ok {
				hc, cleanup := hAd.NewHostHTTPClient(sCtx)
				defer cleanup()
				if res, ok := trySearch(hc); ok {
					return res
				}
			}
		}
	}

	hc, cleanup := transport.DefaultMirrorHTTPClient()
	defer cleanup()
	res, _ := trySearch(hc)
	return res
}
