// Package inventory 负责把各主机适配器发现的 App / Service 同步进 SQLite，并提供查询。
package inventory

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/kukushouhou/DKStore/internal/host"
	"github.com/kukushouhou/DKStore/internal/platform"
	"github.com/kukushouhou/DKStore/internal/store"
	"github.com/kukushouhou/DKStore/internal/version"
)

// Service 管理应用与服务的持久化库存。
type Service struct {
	st    *store.Store
	hosts *host.Registry
}

// New 创建 Inventory 服务。
func New(st *store.Store, hosts *host.Registry) *Service {
	return &Service{st: st, hosts: hosts}
}

// SyncHost 连接单台主机，执行发现并将结果 upsert 到 app / service 表。
func (s *Service) SyncHost(ctx context.Context, hostID string) ([]platform.App, *platform.HostInfo, error) {
	ad, _, err := s.hosts.OpenAdapter(ctx, hostID)
	if err != nil {
		return nil, nil, err
	}
	defer ad.Close()

	info, err := ad.Probe(ctx)
	if err != nil {
		return nil, nil, err
	}
	apps, err := ad.ListApps(ctx)
	if err != nil {
		return nil, info, err
	}
	if err := s.saveHostInventory(ctx, hostID, info, apps); err != nil {
		return nil, info, err
	}
	return apps, info, nil
}

func (s *Service) saveHostInventory(ctx context.Context, hostID string, info *platform.HostInfo, apps []platform.App) error {
	now := time.Now().Unix()
	platStr := formatHostPlatform(info)

	infoBytes, _ := json.Marshal(info)
	tx, err := s.st.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `UPDATE host SET status='online', info_json=?, last_seen_at=? WHERE id=?`,
		string(infoBytes), now, hostID); err != nil {
		return err
	}

	seenApps := make(map[string]bool, len(apps))
	seenSvcs := make(map[string]bool)

	for _, a := range apps {
		seenApps[a.Key] = true
		var projJSON string
		if a.Project != nil {
			b, _ := json.Marshal(a.Project)
			projJSON = string(b)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO app(key, host_id, name, source, project_json, gone_at, updated_at)
			VALUES (?, ?, ?, ?, ?, NULL, ?)
			ON CONFLICT(key) DO UPDATE SET
				name=excluded.name,
				source=excluded.source,
				project_json=excluded.project_json,
				gone_at=NULL,
				updated_at=excluded.updated_at`,
			a.Key, hostID, a.Name, string(a.Source), projJSON, now,
		); err != nil {
			return err
		}

		for _, svc := range a.Services {
			seenSvcs[svc.Key] = true
			repoDigest := pickRepoDigest(svc.ImageRef, svc.RepoDigests)
			var labelsJSON string
			if len(svc.Labels) > 0 {
				if b, err := json.Marshal(svc.Labels); err == nil {
					labelsJSON = string(b)
				}
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO service(key, app_key, name, image_ref, image_id, repo_digest, platform, state, labels_json, gone_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?)
				ON CONFLICT(key) DO UPDATE SET
					name=excluded.name,
					image_ref=excluded.image_ref,
					image_id=excluded.image_id,
					repo_digest=excluded.repo_digest,
					platform=excluded.platform,
					state=excluded.state,
					labels_json=excluded.labels_json,
					gone_at=NULL,
					updated_at=excluded.updated_at`,
				svc.Key, a.Key, svc.Name, svc.ImageRef, svc.ImageID, repoDigest, platStr, svc.State, labelsJSON, now,
			); err != nil {
				return err
			}
		}
	}

	// 将本次未出现的 app / service 标记为 gone_at（保留忽略规则，30 天后清理）
	rows, err := tx.QueryContext(ctx, `SELECT key FROM app WHERE host_id=? AND gone_at IS NULL`, hostID)
	if err == nil {
		var staleApps []string
		for rows.Next() {
			var k string
			if rows.Scan(&k) == nil && !seenApps[k] {
				staleApps = append(staleApps, k)
			}
		}
		rows.Close()
		for _, k := range staleApps {
			_, _ = tx.ExecContext(ctx, `UPDATE app SET gone_at=? WHERE key=?`, now, k)
			_, _ = tx.ExecContext(ctx, `UPDATE service SET gone_at=? WHERE app_key=? AND gone_at IS NULL`, now, k)
		}
	}

	sRows, err := tx.QueryContext(ctx, `
		SELECT s.key FROM service s JOIN app a ON s.app_key=a.key
		WHERE a.host_id=? AND s.gone_at IS NULL`, hostID)
	if err == nil {
		var staleSvcs []string
		for sRows.Next() {
			var k string
			if sRows.Scan(&k) == nil && !seenSvcs[k] {
				staleSvcs = append(staleSvcs, k)
			}
		}
		sRows.Close()
		for _, k := range staleSvcs {
			_, _ = tx.ExecContext(ctx, `UPDATE service SET gone_at=? WHERE key=?`, now, k)
		}
	}

	return tx.Commit()
}

// pickRepoDigest 从镜像的 RepoDigests（如 ["nginx@sha256:abc..."]）中提取 sha256:... 部分，
// 优先选择与当前镜像引用同一仓库的条目（审计 L5），找不到时退回第一个有效摘要。
func pickRepoDigest(imageRef string, repoDigests []string) string {
	if _, dig, ok := strings.Cut(imageRef, "@"); ok && strings.HasPrefix(dig, "sha256:") {
		// 引用本身固定了摘要时，优先使用 RepoDigests 中与之相同的条目
		for _, rd := range repoDigests {
			if _, d, ok := strings.Cut(rd, "@"); ok && d == dig {
				return d
			}
		}
	}
	wantRepo := repoKey(imageRef)
	first := ""
	for _, rd := range repoDigests {
		name, dig, ok := strings.Cut(rd, "@")
		if !ok || !strings.HasPrefix(dig, "sha256:") {
			continue
		}
		if first == "" {
			first = dig
		}
		if wantRepo != "" && repoKey(name) == wantRepo {
			return dig
		}
	}
	if first != "" {
		return first
	}
	if _, dig, ok := strings.Cut(imageRef, "@"); ok && strings.HasPrefix(dig, "sha256:") {
		return dig
	}
	return ""
}

// repoKey 返回 "registry/repository" 归一化键，解析失败返回空。
func repoKey(ref string) string {
	pr, err := version.ParseImageRef(ref)
	if err != nil {
		return ""
	}
	return strings.ToLower(pr.Registry + "/" + pr.Repository)
}

// formatHostPlatform 生成 service.platform 字符串；架构为空或未知时返回空串，检测时跳过平台级比对（不默认 amd64，审计 M2/H5）。
func formatHostPlatform(info *platform.HostInfo) string {
	if info == nil {
		return ""
	}
	osName := strings.ToLower(strings.TrimSpace(info.OS))
	arch := strings.ToLower(strings.TrimSpace(info.Arch))
	if arch == "" || arch == "unknown" {
		return ""
	}
	if osName == "" {
		osName = "linux"
	}
	p := osName + "/" + arch
	if v := strings.ToLower(strings.TrimSpace(info.Variant)); v != "" && !(arch == "arm64" && v == "v8") {
		p += "/" + v
	}
	return p
}
