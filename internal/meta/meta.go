// Package meta 管理项目与容器的本地自定义信息（备注名、标签、访问入口与备注）。
package meta

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/kukushouhou/DKStore/internal/platform"
	"github.com/kukushouhou/DKStore/internal/store"
)

// ErrInvalidTarget 表示缺少有效的目标键或主机 ID。
var ErrInvalidTarget = errors.New("missing targetKey or hostId")

// Entry 表示单条待保存的项目或容器本地信息。
type Entry struct {
	TargetKey string             `json:"targetKey"`
	HostID    string             `json:"hostId"`
	Meta      platform.LocalMeta `json:"meta"`
}

// Normalize 清洗并归一化本地元数据字段。
func Normalize(in platform.LocalMeta) platform.LocalMeta {
	mode := strings.ToLower(strings.TrimSpace(in.AccessMode))
	switch mode {
	case "custom", "none":
	default:
		mode = "auto"
	}

	scheme := strings.ToLower(strings.TrimSpace(in.Scheme))
	if scheme != "http" && scheme != "https" {
		scheme = ""
	}

	seenTag := make(map[string]bool, len(in.Tags))
	var cleanTags []string
	for _, raw := range in.Tags {
		t := strings.TrimSpace(raw)
		if t == "" || seenTag[t] {
			continue
		}
		seenTag[t] = true
		cleanTags = append(cleanTags, t)
	}

	customURL := strings.TrimSpace(in.CustomURL)
	if customURL != "" &&
		!strings.HasPrefix(strings.ToLower(customURL), "http://") &&
		!strings.HasPrefix(strings.ToLower(customURL), "https://") {
		customURL = "http://" + customURL
	}

	out := platform.LocalMeta{
		Alias:      strings.TrimSpace(in.Alias),
		Tags:       cleanTags,
		AccessMode: mode,
		Scheme:     scheme,
		FixedPort:  strings.TrimSpace(in.FixedPort),
		CustomURL:  customURL,
		Note:       strings.TrimSpace(in.Note),
	}
	if out.AccessMode == "auto" {
		out.AccessMode = ""
	}
	return out
}

// IsEmpty 判断归一化后的元数据是否完全处于默认空状态。
func IsEmpty(m platform.LocalMeta) bool {
	return m.Alias == "" &&
		len(m.Tags) == 0 &&
		(m.AccessMode == "" || m.AccessMode == "auto") &&
		m.Scheme == "" &&
		m.FixedPort == "" &&
		m.CustomURL == "" &&
		m.Note == ""
}

// SaveBatch 在单个事务内批量保存或清理本地元数据。
func SaveBatch(ctx context.Context, st *store.Store, items []Entry) error {
	if st == nil || st.DB == nil {
		return nil
	}
	if len(items) == 0 {
		return ErrInvalidTarget
	}
	for _, it := range items {
		if strings.TrimSpace(it.TargetKey) == "" || strings.TrimSpace(it.HostID) == "" {
			return ErrInvalidTarget
		}
	}

	now := time.Now().Unix()
	tx, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, it := range items {
		tk := strings.TrimSpace(it.TargetKey)
		hid := strings.TrimSpace(it.HostID)
		m := Normalize(it.Meta)
		if IsEmpty(m) {
			if _, err := tx.ExecContext(ctx, `DELETE FROM local_meta WHERE target_key=?`, tk); err != nil {
				return err
			}
			continue
		}
		mode := m.AccessMode
		if mode == "" {
			mode = "auto"
		}
		tagsBytes, _ := json.Marshal(m.Tags)
		if m.Tags == nil {
			tagsBytes = []byte("[]")
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO local_meta(target_key, host_id, alias, tags_json, access_mode, scheme, fixed_port, custom_url, note, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(target_key) DO UPDATE SET
				host_id=excluded.host_id,
				alias=excluded.alias,
				tags_json=excluded.tags_json,
				access_mode=excluded.access_mode,
				scheme=excluded.scheme,
				fixed_port=excluded.fixed_port,
				custom_url=excluded.custom_url,
				note=excluded.note,
				updated_at=excluded.updated_at`,
			tk, hid, m.Alias, string(tagsBytes), mode, m.Scheme, m.FixedPort, m.CustomURL, m.Note, now,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListAll 查询全部已保存的本地元数据（按 targetKey 索引）及去重排序后的全局标签池。
func ListAll(ctx context.Context, st *store.Store) (map[string]*platform.LocalMeta, []string, error) {
	out := make(map[string]*platform.LocalMeta)
	if st == nil || st.DB == nil {
		return out, nil, nil
	}
	rows, err := st.DB.QueryContext(ctx, `
		SELECT target_key, COALESCE(alias,''), COALESCE(tags_json,'[]'),
		       COALESCE(access_mode,'auto'), COALESCE(scheme,''),
		       COALESCE(fixed_port,''), COALESCE(custom_url,''), COALESCE(note,'')
		FROM local_meta`)
	if err != nil {
		return out, nil, err
	}
	defer rows.Close()

	tagSet := make(map[string]bool)
	for rows.Next() {
		var tk, alias, tagsJSON, mode, scheme, fixedPort, customURL, note string
		if rows.Scan(&tk, &alias, &tagsJSON, &mode, &scheme, &fixedPort, &customURL, &note) != nil {
			continue
		}
		var tags []string
		_ = json.Unmarshal([]byte(tagsJSON), &tags)
		m := Normalize(platform.LocalMeta{
			Alias:      alias,
			Tags:       tags,
			AccessMode: mode,
			Scheme:     scheme,
			FixedPort:  fixedPort,
			CustomURL:  customURL,
			Note:       note,
		})
		if IsEmpty(m) {
			continue
		}
		for _, t := range m.Tags {
			tagSet[t] = true
		}
		cp := m
		out[tk] = &cp
	}
	var allTags []string
	for t := range tagSet {
		allTags = append(allTags, t)
	}
	sort.Strings(allTags)
	return out, allTags, rows.Err()
}

// DeleteByApp 清理指定应用及其所属子容器的本地元数据。
func DeleteByApp(ctx context.Context, st *store.Store, appKey string) error {
	appKey = strings.TrimSpace(appKey)
	if st == nil || st.DB == nil || appKey == "" {
		return nil
	}
	_, err := st.DB.ExecContext(ctx, `DELETE FROM local_meta WHERE target_key = ? OR target_key LIKE ?`, appKey, appKey+"/%")
	return err
}
