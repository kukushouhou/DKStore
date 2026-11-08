// Package ignore 管理“仅忽略当前版本”与“永久忽略”规则（实施文档 §7）。
package ignore

import (
	"context"
	"errors"
	"time"

	"github.com/kukushouhou/DKStore/internal/store"
)

const (
	ModeThisVersion = "this_version"
	ModeForever     = "forever"
)

// Rule 表示一条忽略规则。
type Rule struct {
	ID           int64  `json:"id"`
	ServiceKey   string `json:"serviceKey"`
	Mode         string `json:"mode"` // this_version | forever
	TargetDigest string `json:"targetDigest,omitempty"`
	TargetTag    string `json:"targetTag,omitempty"`
	Note         string `json:"note,omitempty"`
	CreatedAt    int64  `json:"createdAt"`
}

// Service 提供忽略规则的增删改查与匹配。
type Service struct {
	st *store.Store
}

// New 创建忽略服务。
func New(st *store.Store) *Service {
	return &Service{st: st}
}

// Set 为一组 serviceKey 设置忽略规则。
// 若 mode == "this_version"，会自动从 finding 表读取每个 service 当前的 target_digest 与 target_version 记录下来。
func (s *Service) Set(ctx context.Context, serviceKeys []string, mode, note string) error {
	if len(serviceKeys) == 0 {
		return errors.New("未指定要忽略的服务")
	}
	if mode != ModeThisVersion && mode != ModeForever {
		return errors.New("忽略模式无效，仅支持 this_version 或 forever")
	}
	now := time.Now().Unix()
	tx, err := s.st.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, sk := range serviceKeys {
		var tDigest, tTag string
		if mode == ModeThisVersion {
			_ = tx.QueryRowContext(ctx, `
				SELECT COALESCE(target_digest,''), COALESCE(target_version,'')
				FROM finding WHERE service_key=?`, sk).Scan(&tDigest, &tTag)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO ignore_rule(service_key, mode, target_digest, target_tag, note, created_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(service_key) DO UPDATE SET
				mode=excluded.mode,
				target_digest=excluded.target_digest,
				target_tag=excluded.target_tag,
				note=excluded.note,
				created_at=excluded.created_at`,
			sk, mode, tDigest, tTag, note, now,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Remove 取消指定 serviceKey 的忽略规则（恢复提醒）。
func (s *Service) Remove(ctx context.Context, serviceKey string) error {
	_, err := s.st.DB.ExecContext(ctx, `DELETE FROM ignore_rule WHERE service_key=?`, serviceKey)
	return err
}

// RemoveByApp 取消某个 App 下所有 Service 的忽略规则。
func (s *Service) RemoveByApp(ctx context.Context, appKey string) error {
	_, err := s.st.DB.ExecContext(ctx, `
		DELETE FROM ignore_rule WHERE service_key IN (
			SELECT key FROM service WHERE app_key=?
		)`, appKey)
	return err
}

// ListAll 返回全部忽略规则（按 serviceKey 索引）。
func (s *Service) ListAll(ctx context.Context) (map[string]Rule, error) {
	rows, err := s.st.DB.QueryContext(ctx, `
		SELECT id, service_key, mode, COALESCE(target_digest,''), COALESCE(target_tag,''), COALESCE(note,''), created_at
		FROM ignore_rule`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]Rule)
	for rows.Next() {
		var r Rule
		if err := rows.Scan(&r.ID, &r.ServiceKey, &r.Mode, &r.TargetDigest, &r.TargetTag, &r.Note, &r.CreatedAt); err != nil {
			return nil, err
		}
		out[r.ServiceKey] = r
	}
	return out, rows.Err()
}

// Matches 判断一条规则是否对当前检测到的 (findingKind, targetDigest, targetVersion) 生效（实施文档 §7.2）。
func Matches(r Rule, findingKind, targetDigest, targetVersion string) bool {
	if r.ServiceKey == "" {
		return false
	}
	if r.Mode == ModeForever {
		return true
	}
	if r.Mode == ModeThisVersion {
		if findingKind == "version_update" && r.TargetTag != "" {
			return r.TargetTag == targetVersion
		}
		if r.TargetDigest != "" && targetDigest != "" {
			return r.TargetDigest == targetDigest
		}
		if r.TargetTag != "" && targetVersion != "" {
			return r.TargetTag == targetVersion
		}
	}
	return false
}
