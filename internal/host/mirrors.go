package host

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/kukushouhou/DKStore/internal/i18n"
	"github.com/kukushouhou/DKStore/internal/platform"
	"github.com/kukushouhou/DKStore/internal/platform/docker"
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
	out = append(out, platform.MirrorPair{
		Source: "docker.io",
		URLs:   hubURLs,
	})
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

// TestHostMirrors 从指定目标设备发起各加速镜像网址的连通与延迟实测。
func (r *Registry) TestHostMirrors(ctx context.Context, hostID string, urls []string, lang i18n.Lang) ([]platform.MirrorTestItem, error) {
	ad, _, err := r.OpenAdapter(ctx, hostID)
	if err != nil {
		return nil, err
	}
	defer ad.Close()

	mAd, ok := ad.(platform.MirrorAdapter)
	if !ok {
		return nil, nil
	}
	items, err := mAd.TestMirrorURLs(ctx, urls)
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
