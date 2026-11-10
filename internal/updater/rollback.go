package updater

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/kukushouhou/DKStore/internal/i18n"
	"github.com/kukushouhou/DKStore/internal/platform"
	"github.com/kukushouhou/DKStore/internal/registry"
	"github.com/kukushouhou/DKStore/internal/version"
)

// RollbackVerifyResult 是 GET /api/v1/updates/{id}/rollback-check 的返回体。
type RollbackVerifyResult struct {
	Eligible    bool   `json:"eligible"`
	RollbackRef string `json:"rollbackRef,omitempty"`
	OldVersion  string `json:"oldVersion,omitempty"`
	OldDigest   string `json:"oldDigest,omitempty"`
	ReasonCode  string `json:"reasonCode,omitempty"`
	ReasonText  string `json:"reasonText,omitempty"`
	DataWarning string `json:"dataWarning,omitempty"`
}

// ListJobs 查询更新与回退任务历史（按语言本地化错误消息与回退原因，立项文档 §8.1）。
func (s *Service) ListJobs(ctx context.Context, limit int, lang i18n.Lang) ([]Job, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.st.DB.QueryContext(ctx, `
		SELECT id, kind, host_id, host_name, app_key, app_name, service_key, service_name, source,
		       status, phase, COALESCE(progress_json,''),
		       old_image_ref, COALESCE(old_image_id,''), COALESCE(old_repo_digest,''), COALESCE(old_version,''), COALESCE(old_matched_tags_json,''),
		       target_image_ref, COALESCE(target_repo_digest,''), COALESCE(target_version,''), COALESCE(target_matched_tags_json,''),
		       COALESCE(new_image_id,''), COALESCE(new_repo_digest,''),
		       rollback_eligible, COALESCE(rollback_ref,''), COALESCE(rollback_reason,''),
		       COALESCE(error_code,''), COALESCE(error_msg,''), COALESCE(logs_text,''),
		       started_at, COALESCE(finished_at,0)
		FROM update_job
		ORDER BY started_at DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Job
	for rows.Next() {
		j, err := scanJobRow(rows, lang)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// GetJob 查询单个更新任务详情。
func (s *Service) GetJob(ctx context.Context, id string, lang i18n.Lang) (*Job, error) {
	rows, err := s.st.DB.QueryContext(ctx, `
		SELECT id, kind, host_id, host_name, app_key, app_name, service_key, service_name, source,
		       status, phase, COALESCE(progress_json,''),
		       old_image_ref, COALESCE(old_image_id,''), COALESCE(old_repo_digest,''), COALESCE(old_version,''), COALESCE(old_matched_tags_json,''),
		       target_image_ref, COALESCE(target_repo_digest,''), COALESCE(target_version,''), COALESCE(target_matched_tags_json,''),
		       COALESCE(new_image_id,''), COALESCE(new_repo_digest,''),
		       rollback_eligible, COALESCE(rollback_ref,''), COALESCE(rollback_reason,''),
		       COALESCE(error_code,''), COALESCE(error_msg,''), COALESCE(logs_text,''),
		       started_at, COALESCE(finished_at,0)
		FROM update_job
		WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, i18n.NewEngineError(i18n.MsgUpdateJobNotFound, id)
	}
	j, err := scanJobRow(rows, lang)
	if err != nil {
		return nil, err
	}
	return &j, nil
}

// VerifyRollback 动态核验一条历史升级记录当前是否仍可安全无歧义回退（立项文档 §8.2 / A10 / A12）。
func (s *Service) VerifyRollback(ctx context.Context, jobID string, lang i18n.Lang) (*RollbackVerifyResult, error) {
	j, err := s.GetJob(ctx, jobID, lang)
	if err != nil {
		return nil, err
	}
	warn := "提示：回退只会恢复软件版本，不会还原已修改的数据文件。"
	if lang == i18n.EnUS {
		warn = "Note: Rolling back restores the app version only and does not revert modified data files."
	}

	if !j.RollbackEligible || j.RollbackRef == "" {
		code := j.RollbackReason
		if code == "" {
			code = "no_exact_version_tag"
		}
		return &RollbackVerifyResult{
			Eligible:   false,
			ReasonCode: code,
			ReasonText: i18n.FormatRollbackReason(lang, code),
		}, nil
	}

	// 查询主机平台架构（审计 L12：未记录平台时不盲目默认 linux/amd64）
	var plat string
	_ = s.st.DB.QueryRowContext(ctx, `SELECT COALESCE(platform,'') FROM service WHERE key=?`, j.ServiceKey).Scan(&plat)

	// 1. 先检查远端仓库中该精确版本标签是否仍指向历史旧 Digest（审计 M12）
	ref, err := version.ParseImageRef(j.RollbackRef)
	if err != nil {
		return &RollbackVerifyResult{
			Eligible:   false,
			ReasonCode: "no_exact_version_tag",
			ReasonText: i18n.FormatRollbackReason(lang, "no_exact_version_tag"),
		}, nil
	}

	remoteMatches := false
	mf, mfErr := s.reg.CheckManifest(ctx, ref, plat, true)
	if mfErr == nil && mf != nil {
		oldDigest := registry.NormalizeDigest(j.OldRepoDigest)
		oldImgID := registry.NormalizeDigest(j.OldImageID)
		remDigest := registry.NormalizeDigest(mf.RepoDigest)
		remPlatDigest := registry.NormalizeDigest(mf.PlatformDigest)
		remCfgDigest := registry.NormalizeDigest(mf.ConfigDigest)
		if oldDigest != "" {
			if remDigest == oldDigest || (remPlatDigest != "" && remPlatDigest == oldDigest) {
				remoteMatches = true
			}
		} else if oldImgID != "" && remCfgDigest != "" && remCfgDigest == oldImgID {
			remoteMatches = true
		}
	}

	// 2. 若远端标签不可达或被覆盖，再检查目标宿主机本地是否仍保留旧镜像 ID
	localMatches := false
	if !remoteMatches && j.OldImageID != "" {
		if ad, _, err := s.hosts.OpenAdapter(ctx, j.HostID); err == nil {
			if upAd, ok := ad.(platform.UpdateCapableAdapter); ok {
				localMatches = upAd.HasLocalImage(ctx, j.OldImageID)
			}
			_ = ad.Close()
		}
	}

	if !remoteMatches && !localMatches {
		code := "tag_overwritten"
		if mfErr != nil {
			code = "target_unreachable"
		}
		_, _ = s.st.DB.ExecContext(ctx, `UPDATE update_job SET rollback_eligible=0, rollback_reason=? WHERE id=?`, code, j.ID)
		return &RollbackVerifyResult{
			Eligible:   false,
			ReasonCode: code,
			ReasonText: i18n.FormatRollbackReason(lang, code),
		}, nil
	}

	return &RollbackVerifyResult{
		Eligible:    true,
		RollbackRef: j.RollbackRef,
		OldVersion:  j.OldVersion,
		OldDigest:   j.OldRepoDigest,
		DataWarning: warn,
	}, nil
}

// ExecuteRollback 在动态验证通过后启动回退任务（立项文档 §8.2 / A12）。
func (s *Service) ExecuteRollback(ctx context.Context, jobID string, lang i18n.Lang) (*Job, error) {
	ver, err := s.VerifyRollback(ctx, jobID, lang)
	if err != nil {
		return nil, err
	}
	if !ver.Eligible {
		return nil, errors.New(ver.ReasonText)
	}
	orig, err := s.GetJob(ctx, jobID, lang)
	if err != nil {
		return nil, err
	}

	// 审计 M4：在同一次 s.mu.Lock() 内检查并占位写入 activeJobs
	s.mu.Lock()
	if active, ok := s.activeJobs[orig.ServiceKey]; ok && (active.Status == "running" || active.Status == "queued") {
		s.mu.Unlock()
		return &active, nil
	}
	s.activeJobs[orig.ServiceKey] = Job{ServiceKey: orig.ServiceKey, Status: "queued", Phase: "precheck"}
	s.mu.Unlock()

	cleanupPlaceholder := func() {
		s.mu.Lock()
		if cur, ok := s.activeJobs[orig.ServiceKey]; ok && cur.ID == "" {
			delete(s.activeJobs, orig.ServiceKey)
		}
		s.mu.Unlock()
	}

	var curImageRef, curImageID, curRepoDigest string
	_ = s.st.DB.QueryRowContext(ctx, `
		SELECT COALESCE(image_ref,''), COALESCE(image_id,''), COALESCE(repo_digest,'')
		FROM service WHERE key=?`, orig.ServiceKey).Scan(&curImageRef, &curImageID, &curRepoDigest)

	now := time.Now().Unix()
	rbJob := Job{
		ID:                 newJobID(),
		Kind:               "rollback",
		HostID:             orig.HostID,
		HostName:           orig.HostName,
		AppKey:             orig.AppKey,
		AppName:            orig.AppName,
		ServiceKey:         orig.ServiceKey,
		ServiceName:        orig.ServiceName,
		Source:             orig.Source,
		Status:             "running",
		Phase:              "precheck",
		Progress:           platform.PullProgress{Phase: "precheck", StatusText: "Preparing rollback"},
		OldImageRef:        curImageRef,
		OldImageID:         curImageID,
		OldRepoDigest:      curRepoDigest,
		OldVersion:         orig.TargetVersion,
		TargetImageRef:     orig.RollbackRef,
		TargetRepoDigest:   orig.OldRepoDigest,
		TargetVersion:      orig.OldVersion,
		TargetMatchedTags:  orig.OldMatchedTags,
		RollbackEligible:   false,
		RollbackReason:     "no_exact_version_tag",
		RollbackReasonText: i18n.FormatRollbackReason(lang, "no_exact_version_tag"),
		StartedAt:          now,
	}
	if err := s.insertJob(ctx, rbJob); err != nil {
		cleanupPlaceholder()
		return nil, err
	}
	s.broadcast(rbJob)
	go s.executeJob(rbJob)
	return &rbJob, nil
}

func scanJobRow(rows *sql.Rows, lang i18n.Lang) (Job, error) {
	var (
		j                        Job
		progJSON                 string
		oldTagsJSON, tgtTagsJSON string
		eligibleInt              int
	)
	err := rows.Scan(
		&j.ID, &j.Kind, &j.HostID, &j.HostName, &j.AppKey, &j.AppName, &j.ServiceKey, &j.ServiceName, &j.Source,
		&j.Status, &j.Phase, &progJSON,
		&j.OldImageRef, &j.OldImageID, &j.OldRepoDigest, &j.OldVersion, &oldTagsJSON,
		&j.TargetImageRef, &j.TargetRepoDigest, &j.TargetVersion, &tgtTagsJSON,
		&j.NewImageID, &j.NewRepoDigest,
		&eligibleInt, &j.RollbackRef, &j.RollbackReason,
		&j.ErrorCode, &j.ErrorMsg, &j.Logs,
		&j.StartedAt, &j.FinishedAt,
	)
	if err != nil {
		return Job{}, err
	}
	j.RollbackEligible = eligibleInt == 1
	if progJSON != "" {
		_ = json.Unmarshal([]byte(progJSON), &j.Progress)
	}
	if oldTagsJSON != "" {
		_ = json.Unmarshal([]byte(oldTagsJSON), &j.OldMatchedTags)
	}
	if tgtTagsJSON != "" {
		_ = json.Unmarshal([]byte(tgtTagsJSON), &j.TargetMatchedTags)
	}
	if j.RollbackReason != "" {
		j.RollbackReasonText = i18n.FormatRollbackReason(lang, j.RollbackReason)
	}
	if j.ErrorCode != "" {
		j.ErrorMsg = i18n.FormatUpdateError(lang, j.ErrorCode, j.ErrorMsg)
	}
	if j.TargetVersion != "" {
		j.TargetVersion = i18n.FormatTargetVersion(lang, j.TargetVersion)
	}
	return j, nil
}
