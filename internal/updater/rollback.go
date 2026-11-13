package updater

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
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
		       old_image_ref, COALESCE(old_image_id,''), COALESCE(old_repo_digest,''), COALESCE(old_version,''), COALESCE(old_resolved_tag,''), COALESCE(old_matched_tags_json,''),
		       target_image_ref, COALESCE(target_repo_digest,''), COALESCE(target_version,''), COALESCE(target_resolved_tag,''), COALESCE(target_matched_tags_json,''),
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
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 若任务仍在运行中，叠加内存态的最新实时分层进度与日志偏移量
	s.mu.Lock()
	if len(s.activeJobs) > 0 {
		byID := make(map[string]Job, len(s.activeJobs))
		for _, aj := range s.activeJobs {
			if aj.ID != "" {
				byID[aj.ID] = aj
			}
		}
		for i := range out {
			if live, ok := byID[out[i].ID]; ok {
				out[i].Status = live.Status
				out[i].Phase = live.Phase
				out[i].Progress = live.Progress
				out[i].LogOffset = len(live.Logs)
			}
		}
	}
	s.mu.Unlock()
	return out, nil
}

// GetJob 查询单个更新任务详情（运行中任务直接返回实时累积内存态 Logs 与 LogOffset，附录 E.3）。
func (s *Service) GetJob(ctx context.Context, id string, lang i18n.Lang) (*Job, error) {
	s.mu.Lock()
	for _, active := range s.activeJobs {
		if active.ID == id {
			cp := active
			s.mu.Unlock()
			cp.LogChunk = ""
			cp.LogOffset = len(cp.Logs)
			if cp.RollbackReason != "" {
				cp.RollbackReasonText = i18n.FormatRollbackReason(lang, cp.RollbackReason)
			}
			if cp.TargetVersion != "" {
				cp.TargetVersion = i18n.FormatTargetVersion(lang, cp.TargetVersion)
			}
			return &cp, nil
		}
	}
	s.mu.Unlock()

	rows, err := s.st.DB.QueryContext(ctx, `
		SELECT id, kind, host_id, host_name, app_key, app_name, service_key, service_name, source,
		       status, phase, COALESCE(progress_json,''),
		       old_image_ref, COALESCE(old_image_id,''), COALESCE(old_repo_digest,''), COALESCE(old_version,''), COALESCE(old_resolved_tag,''), COALESCE(old_matched_tags_json,''),
		       target_image_ref, COALESCE(target_repo_digest,''), COALESCE(target_version,''), COALESCE(target_resolved_tag,''), COALESCE(target_matched_tags_json,''),
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

	// 查询主机平台架构（审计 L12：未记录平台时不盲目默认 linux/amd64，若服务未存则回退查询宿主探针架构）
	var plat string
	_ = s.st.DB.QueryRowContext(ctx, `SELECT COALESCE(platform,'') FROM service WHERE key=?`, j.ServiceKey).Scan(&plat)
	if plat == "" && j.HostID != "" {
		var infoJSON string
		if s.st.DB.QueryRowContext(ctx, `SELECT COALESCE(info_json,'') FROM host WHERE id=?`, j.HostID).Scan(&infoJSON) == nil && infoJSON != "" {
			var hi platform.HostInfo
			if json.Unmarshal([]byte(infoJSON), &hi) == nil && hi.Arch != "" && hi.Arch != "unknown" {
				osName := strings.ToLower(strings.TrimSpace(hi.OS))
				if osName == "" {
					osName = "linux"
				}
				plat = osName + "/" + strings.ToLower(strings.TrimSpace(hi.Arch))
			}
		}
	}

	// 1. 先检查远端仓库中该精确版本标签是否仍指向历史旧 Digest / 旧 ImageID（审计 M12）
	ref, err := version.ParseImageRef(j.RollbackRef)
	if err != nil {
		return &RollbackVerifyResult{
			Eligible:   false,
			ReasonCode: "no_exact_version_tag",
			ReasonText: i18n.FormatRollbackReason(lang, "no_exact_version_tag"),
		}, nil
	}

	oldDigest := registry.NormalizeDigest(j.OldRepoDigest)
	oldImgID := registry.NormalizeDigest(j.OldImageID)
	needCfg := oldDigest == "" && oldImgID != ""

	checkCandidateRef := func(candRef version.ParsedRef) (bool, error) {
		mf, mfErr := s.reg.CheckManifestEx(ctx, candRef, registry.ManifestCheckOptions{
			HostPlatform: plat,
			BypassCache:  true,
			NeedConfig:   needCfg,
		})
		if mfErr != nil || mf == nil {
			return false, mfErr
		}
		remDigest := registry.NormalizeDigest(mf.RepoDigest)
		remPlatDigest := registry.NormalizeDigest(mf.PlatformDigest)
		remCfgDigest := registry.NormalizeDigest(mf.ConfigDigest)
		if oldDigest != "" {
			if remDigest == oldDigest || (remPlatDigest != "" && remPlatDigest == oldDigest) {
				return true, nil
			}
		} else if oldImgID != "" && remCfgDigest != "" && remCfgDigest == oldImgID {
			return true, nil
		}
		return false, nil
	}

	remoteMatches, mfErr := checkCandidateRef(ref)
	verifiedRollbackRef := j.RollbackRef

	// 若 OCI 标签与仓库 Tag 仅相差 v 前缀（如 3.8.3 vs v3.8.3）导致 404，自动核验一次切换 v 前缀的候选标签
	if !remoteMatches {
		var re *registry.RegistryError
		if errors.As(mfErr, &re) && re.Kind == registry.ErrNotFound && ref.Tag != "" {
			altTag := "v" + ref.Tag
			if strings.HasPrefix(ref.Tag, "v") || strings.HasPrefix(ref.Tag, "V") {
				altTag = ref.Tag[1:]
			}
			if altParsed, pErr := version.ParseImageRef(ref.WithTag(altTag)); pErr == nil {
				if ok, altErr := checkCandidateRef(altParsed); ok {
					remoteMatches = true
					mfErr = nil
					verifiedRollbackRef = replaceRefTag(j.OldImageRef, altTag)
					_, _ = s.st.DB.ExecContext(ctx, `UPDATE update_job SET rollback_ref=?, old_resolved_tag=? WHERE id=?`, verifiedRollbackRef, altTag, j.ID)
				} else if altErr == nil {
					mfErr = nil
				}
			}
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
		RollbackRef: verifiedRollbackRef,
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
	targetRef := ver.RollbackRef
	if targetRef == "" {
		targetRef = orig.RollbackRef
	}
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
		Progress:           platform.PullProgress{Phase: "precheck", StatusText: "Preparing rollback", Percent: 5},
		OldImageRef:        curImageRef,
		OldImageID:         curImageID,
		OldRepoDigest:      curRepoDigest,
		OldVersion:         orig.TargetVersion,
		OldResolvedTag:     orig.TargetResolvedTag,
		TargetImageRef:     targetRef,
		TargetRepoDigest:   orig.OldRepoDigest,
		TargetVersion:      orig.OldVersion,
		TargetResolvedTag:  orig.OldResolvedTag,
		TargetMatchedTags:  orig.OldMatchedTags,
		RollbackEligible:   false,
		RollbackReason:     "no_exact_version_tag",
		RollbackReasonText: i18n.FormatRollbackReason(lang, "no_exact_version_tag"),
		StartedAt:          now,
	}
	initHeader := formatJobHeader(rbJob)
	rbJob.Logs = initHeader
	rbJob.LogChunk = initHeader
	rbJob.LogOffset = len(initHeader)
	if err := s.insertJob(ctx, rbJob); err != nil {
		cleanupPlaceholder()
		return nil, err
	}
	s.broadcast(rbJob)
	go s.executeJob(rbJob)
	return &rbJob, nil
}

// ClearJobs 按范围清理历史更新/回退记录（scope: "failed" 仅清理失败记录；"all" 清理全部非执行中记录）。
func (s *Service) ClearJobs(ctx context.Context, scope string) (int64, error) {
	var (
		res sql.Result
		err error
	)
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "failed":
		res, err = s.st.DB.ExecContext(ctx, `DELETE FROM update_job WHERE status = 'failed'`)
	case "all":
		res, err = s.st.DB.ExecContext(ctx, `DELETE FROM update_job WHERE status NOT IN ('running', 'queued')`)
	default:
		return 0, errors.New("invalid scope")
	}
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
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
		&j.OldImageRef, &j.OldImageID, &j.OldRepoDigest, &j.OldVersion, &j.OldResolvedTag, &oldTagsJSON,
		&j.TargetImageRef, &j.TargetRepoDigest, &j.TargetVersion, &j.TargetResolvedTag, &tgtTagsJSON,
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
	j.LogOffset = len(j.Logs)
	return j, nil
}
