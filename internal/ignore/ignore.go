// Package ignore 管理“仅忽略当前版本”、“锁定版本号前缀”与“永久忽略”规则（实施文档 §7 / 附录 D.3）。
package ignore

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/kukushouhou/DKStore/internal/i18n"
	"github.com/kukushouhou/DKStore/internal/store"
)

const (
	ModeThisVersion = "this_version"
	ModePrefix      = "prefix"
	ModeForever     = "forever"
)

// Rule 表示一条忽略或版本前缀锁定规则。
type Rule struct {
	ID           int64  `json:"id"`
	ServiceKey   string `json:"serviceKey"`
	Mode         string `json:"mode"` // this_version | prefix | forever
	Prefix       string `json:"prefix,omitempty"`
	TargetDigest string `json:"targetDigest,omitempty"`
	TargetTag    string `json:"targetTag,omitempty"`
	Note         string `json:"note,omitempty"`
	CreatedAt    int64  `json:"createdAt"`
}

// SetOptions 是设置忽略或版本前缀锁定规则的参数。
type SetOptions struct {
	ServiceKeys  []string `json:"serviceKeys"`
	Mode         string   `json:"mode"`
	Prefix       string   `json:"prefix,omitempty"`
	TargetTag    string   `json:"targetTag,omitempty"`
	TargetDigest string   `json:"targetDigest,omitempty"`
	Note         string   `json:"note,omitempty"`
}

// Service 提供忽略规则的增删改查与匹配。
type Service struct {
	st *store.Store
}

// New 创建忽略服务。
func New(st *store.Store) *Service {
	return &Service{st: st}
}

// Set 为一组 serviceKey 设置忽略规则（兼容旧签名）。
func (s *Service) Set(ctx context.Context, serviceKeys []string, mode, note string) error {
	return s.SetWithOptions(ctx, SetOptions{
		ServiceKeys: serviceKeys,
		Mode:        mode,
		Note:        note,
	})
}

// SetWithOptions 为一组 serviceKey 设置忽略或版本前缀锁定规则。
// 若 mode == "this_version" 且未显式指定 TargetTag/TargetDigest，会自动从 finding 表读取当前待更新目标记录下来。
func (s *Service) SetWithOptions(ctx context.Context, opt SetOptions) error {
	var cleanKeys []string
	for _, k := range opt.ServiceKeys {
		if k = strings.TrimSpace(k); k != "" {
			cleanKeys = append(cleanKeys, k)
		}
	}
	if len(cleanKeys) == 0 {
		return i18n.NewEngineError(i18n.MsgIgnoreNoService)
	}
	if opt.Mode != ModeThisVersion && opt.Mode != ModePrefix && opt.Mode != ModeForever {
		return i18n.NewEngineError(i18n.MsgIgnoreInvalidMode)
	}

	normPrefix := ""
	if opt.Mode == ModePrefix {
		var ok bool
		normPrefix, ok = normalizeNumericPrefix(opt.Prefix)
		if !ok {
			return i18n.NewEngineError(i18n.MsgIgnoreInvalidMode)
		}
	}

	now := time.Now().Unix()
	tx, err := s.st.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, sk := range cleanKeys {
		tDigest := strings.TrimSpace(opt.TargetDigest)
		tTag := strings.TrimSpace(opt.TargetTag)
		if opt.Mode == ModeThisVersion && tDigest == "" && tTag == "" {
			_ = tx.QueryRowContext(ctx, `
				SELECT COALESCE(target_digest,''), COALESCE(target_version,'')
				FROM finding WHERE service_key=?`, sk).Scan(&tDigest, &tTag)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO ignore_rule(service_key, mode, prefix, target_digest, target_tag, note, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(service_key) DO UPDATE SET
				mode=excluded.mode,
				prefix=excluded.prefix,
				target_digest=excluded.target_digest,
				target_tag=excluded.target_tag,
				note=excluded.note,
				created_at=excluded.created_at`,
			sk, opt.Mode, normPrefix, tDigest, tTag, opt.Note, now,
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
		SELECT id, service_key, mode, COALESCE(prefix,''), COALESCE(target_digest,''), COALESCE(target_tag,''), COALESCE(note,''), created_at
		FROM ignore_rule`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]Rule)
	for rows.Next() {
		var r Rule
		if err := rows.Scan(&r.ID, &r.ServiceKey, &r.Mode, &r.Prefix, &r.TargetDigest, &r.TargetTag, &r.Note, &r.CreatedAt); err != nil {
			return nil, err
		}
		out[r.ServiceKey] = r
	}
	return out, rows.Err()
}

// Matches 判断一条规则是否对当前检测到的 (findingKind, targetDigest, targetVersion) 生效（返回 true 表示过滤/抑制提醒）。
func Matches(r Rule, findingKind, targetDigest, targetVersion string) bool {
	if r.ServiceKey == "" {
		return false
	}
	if r.Mode == ModeForever {
		return true
	}
	if r.Mode == ModeThisVersion {
		if findingKind == "version_update" && r.TargetTag != "" {
			return sameVersionTag(r.TargetTag, targetVersion)
		}
		if r.TargetDigest != "" && targetDigest != "" {
			return r.TargetDigest == targetDigest
		}
		if r.TargetTag != "" && targetVersion != "" {
			return sameVersionTag(r.TargetTag, targetVersion)
		}
		return false
	}
	if r.Mode == ModePrefix && r.Prefix != "" {
		normPref, ok := normalizeNumericPrefix(r.Prefix)
		if !ok {
			return false
		}
		tvCore, tvOK := extractNumericCore(targetVersion)
		if !tvOK {
			// 若是已锁定标签自身的纯摘要更新（无更高数字版本号），允许提醒
			if findingKind == "digest_update" {
				return false
			}
			return true
		}
		if tvCore == normPref || strings.HasPrefix(tvCore, normPref+".") {
			return false // 在锁定前缀范围内（如锁 131 时出了 131.0.6778.108），允许正常提醒！
		}
		return true // 超出锁定前缀范围（如锁 131 时出了 132.x，或锁 8.3 时出了 8.9.1），抑制提醒！
	}
	return false
}

func unwrapVersionToken(raw string) string {
	s := strings.TrimSpace(raw)
	if base, rest, ok := strings.Cut(s, " ("); ok && strings.HasSuffix(rest, ")") {
		inner := strings.TrimSpace(strings.TrimSuffix(rest, ")"))
		if inner != "" && !isShortHexHash(inner) {
			return inner
		}
		return strings.TrimSpace(base)
	}
	return s
}

func isShortHexHash(s string) bool {
	if len(s) != 6 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func sameVersionTag(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == b {
		return true
	}
	ua := unwrapVersionToken(a)
	ub := unwrapVersionToken(b)
	if ua != "" && ua == ub {
		return true
	}
	av := strings.TrimPrefix(strings.TrimPrefix(ua, "v"), "V")
	bv := strings.TrimPrefix(strings.TrimPrefix(ub, "v"), "V")
	return av != "" && av == bv
}

func normalizeNumericPrefix(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if strings.HasPrefix(s, "v") || strings.HasPrefix(s, "V") {
		s = s[1:]
	}
	s = strings.Trim(s, ".")
	if s == "" {
		return "", false
	}
	parts := strings.Split(s, ".")
	norm := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			return "", false
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return "", false
		}
		norm = append(norm, strconv.Itoa(n))
	}
	return strings.Join(norm, "."), true
}

func extractNumericCore(targetVersion string) (string, bool) {
	s := strings.TrimSpace(targetVersion)
	if base, rest, ok := strings.Cut(s, " ("); ok && strings.HasSuffix(rest, ")") {
		inner := strings.TrimSpace(strings.TrimSuffix(rest, ")"))
		if inner != "" && !isShortHexHash(inner) {
			if normInner, okInner := cleanAndNormalizeCore(inner); okInner {
				return normInner, true
			}
		}
		s = strings.TrimSpace(base)
	}
	return cleanAndNormalizeCore(s)
}

func cleanAndNormalizeCore(s string) (string, bool) {
	s = strings.TrimSuffix(strings.TrimSpace(s), "@republished")
	if strings.HasPrefix(s, "v") || strings.HasPrefix(s, "V") {
		s = s[1:]
	}
	if idx := strings.IndexAny(s, "-+_"); idx >= 0 {
		s = s[:idx]
	}
	return normalizeNumericPrefix(s)
}
