// Package inventory 负责把各主机适配器发现的 App / Service 同步进 SQLite，并提供查询。
package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kukushouhou/DKStore/internal/host"
	"github.com/kukushouhou/DKStore/internal/platform"
	"github.com/kukushouhou/DKStore/internal/store"
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
	platStr := fmt.Sprintf("%s/%s", info.OS, info.Arch)
	if info.Variant != "" {
		platStr += "/" + info.Variant
	}

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
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO service(key, app_key, name, image_ref, image_id, repo_digest, platform, state, gone_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, ?)
				ON CONFLICT(key) DO UPDATE SET
					name=excluded.name,
					image_ref=excluded.image_ref,
					image_id=excluded.image_id,
					repo_digest=excluded.repo_digest,
					platform=excluded.platform,
					state=excluded.state,
					gone_at=NULL,
					updated_at=excluded.updated_at`,
				svc.Key, a.Key, svc.Name, svc.ImageRef, svc.ImageID, repoDigest, platStr, svc.State, now,
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

// pickRepoDigest 从镜像的 RepoDigests（如 ["nginx@sha256:abc..."]）中提取 sha256:... 部分。
func pickRepoDigest(imageRef string, repoDigests []string) string {
	for _, rd := range repoDigests {
		if _, dig, ok := strings.Cut(rd, "@"); ok && strings.HasPrefix(dig, "sha256:") {
			return dig
		}
	}
	if _, dig, ok := strings.Cut(imageRef, "@"); ok && strings.HasPrefix(dig, "sha256:") {
		return dig
	}
	return ""
}
