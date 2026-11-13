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
)

// AppDeleteSubmitRequest 是 POST /api/v1/apps/delete 的请求结构。
type AppDeleteSubmitRequest struct {
	AppKey       string   `json:"appKey"`
	DeleteImages bool     `json:"deleteImages"`
	FileIDs      []string `json:"fileIds"`
}

func (s *Service) resolveAppTarget(
	ctx context.Context,
	appKey string,
	lang i18n.Lang,
) (hostID, appName string, source platform.SourceKind, proj *platform.ProjectInfo, err error) {
	appKey = strings.TrimSpace(appKey)
	if appKey == "" {
		return "", "", "", nil, i18n.NewEngineError(i18n.MsgDeleteAppNotFound, "")
	}

	var srcStr, projJSON string
	qErr := s.st.DB.QueryRowContext(ctx, `
		SELECT host_id, name, source, COALESCE(project_json,'')
		FROM app WHERE key=? AND gone_at IS NULL`, appKey).Scan(
		&hostID, &appName, &srcStr, &projJSON,
	)
	if qErr == nil {
		source = platform.SourceKind(srcStr)
		if source == platform.SourceVendorManaged {
			return "", "", "", nil, errors.New(i18n.FormatUpdateError(lang, "vendor_managed_readonly", ""))
		}
		if projJSON != "" {
			var p platform.ProjectInfo
			if json.Unmarshal([]byte(projJSON), &p) == nil {
				proj = &p
			}
		}
		if (source == platform.SourceCompose || source == platform.SourceDSMProject) && proj == nil {
			proj = &platform.ProjectInfo{Name: appName}
		}
		return hostID, appName, source, proj, nil
	}
	if !errors.Is(qErr, sql.ErrNoRows) {
		return "", "", "", nil, qErr
	}

	// 支持在「容器」页面对刚启动、尚未经过“检查更新”落库的新容器/项目直接发起删除（按 <hostId>/<kind>/<name> 解析）
	parts := strings.SplitN(appKey, "/", 3)
	if len(parts) != 3 || parts[0] == "" || parts[2] == "" {
		return "", "", "", nil, i18n.NewEngineError(i18n.MsgDeleteAppNotFound, appKey)
	}
	hostID = parts[0]
	appName = parts[2]
	switch parts[1] {
	case "container":
		source = platform.SourceStandalone
	case "compose":
		source = platform.SourceCompose
		proj = &platform.ProjectInfo{Name: appName}
	case "dsm_project":
		source = platform.SourceDSMProject
		proj = &platform.ProjectInfo{Name: appName}
	default:
		return "", "", "", nil, i18n.NewEngineError(i18n.MsgDeleteAppNotFound, appKey)
	}
	return hostID, appName, source, proj, nil
}

func enrichDSMProjectIfNeeded(ctx context.Context, ad platform.Adapter, appKey string, source platform.SourceKind, proj *platform.ProjectInfo) *platform.ProjectInfo {
	if source != platform.SourceDSMProject || (proj != nil && proj.DSMProjectID != "") {
		return proj
	}
	apps, err := ad.ListApps(ctx)
	if err != nil {
		return proj
	}
	for _, a := range apps {
		if a.Key == appKey && a.Project != nil {
			cp := *a.Project
			return &cp
		}
	}
	return proj
}

// PreviewDeleteApp 返回指定容器或整项目的删除预览（关联容器列表、镜像列表、可清理与受保护路径列表）。
func (s *Service) PreviewDeleteApp(ctx context.Context, appKey string, lang i18n.Lang) (*platform.DeleteAppPreview, error) {
	hostID, appName, source, proj, err := s.resolveAppTarget(ctx, appKey, lang)
	if err != nil {
		return nil, err
	}

	ad, _, err := s.hosts.OpenAdapter(ctx, hostID)
	if err != nil {
		return nil, err
	}
	defer ad.Close()

	delAd, ok := ad.(platform.AppDeleteAdapter)
	if !ok {
		return nil, i18n.NewEngineError(i18n.MsgDeleteUnsupported)
	}
	proj = enrichDSMProjectIfNeeded(ctx, ad, appKey, source, proj)
	prev, err := delAd.PreviewDeleteApp(ctx, appName, source, proj)
	if err != nil {
		var ue *platform.UpdateError
		if errors.As(err, &ue) {
			return nil, errors.New(i18n.FormatUpdateError(lang, ue.Code, i18n.LocalizeDetail(lang, ue.Message)))
		}
		return nil, err
	}
	prev.AppKey = strings.TrimSpace(appKey)
	if prev.Files == nil {
		prev.Files = []platform.DeleteFileItem{}
	}
	if prev.Images == nil {
		prev.Images = []string{}
	}
	if prev.Containers == nil {
		prev.Containers = []string{}
	}
	return prev, nil
}

// DeleteApp 执行删除独立容器或整项目，并在成功后立即清理本地库存、待更新结果与实时运行态快照。
func (s *Service) DeleteApp(ctx context.Context, req AppDeleteSubmitRequest, lang i18n.Lang) (*platform.DeleteAppResult, error) {
	appKey := strings.TrimSpace(req.AppKey)
	hostID, appName, source, proj, err := s.resolveAppTarget(ctx, appKey, lang)
	if err != nil {
		return nil, err
	}

	// 若该应用当前有正在执行的更新任务，拒绝并发删除
	s.mu.Lock()
	for _, j := range s.activeJobs {
		if j.AppKey == appKey && (j.Status == "running" || j.Status == "queued") {
			s.mu.Unlock()
			return nil, errors.New("当前应用正在执行更新任务，请等待更新结束后再删除 / An update is currently running for this app")
		}
	}
	s.mu.Unlock()

	unlock := s.lockApp(appKey)
	defer unlock()

	ad, _, err := s.hosts.OpenAdapter(ctx, hostID)
	if err != nil {
		return nil, err
	}
	defer ad.Close()

	delAd, ok := ad.(platform.AppDeleteAdapter)
	if !ok {
		return nil, i18n.NewEngineError(i18n.MsgDeleteUnsupported)
	}
	proj = enrichDSMProjectIfNeeded(ctx, ad, appKey, source, proj)

	res, err := delAd.DeleteApp(ctx, platform.DeleteAppRequest{
		AppKey:       appKey,
		AppName:      appName,
		Source:       source,
		Project:      proj,
		DeleteImages: req.DeleteImages,
		FileIDs:      req.FileIDs,
	})
	if err != nil {
		var ue *platform.UpdateError
		if errors.As(err, &ue) {
			return nil, errors.New(i18n.FormatUpdateError(lang, ue.Code, i18n.LocalizeDetail(lang, ue.Message)))
		}
		return nil, err
	}

	s.purgeDeletedAppState(ctx, hostID, appKey)
	if s.hosts != nil {
		s.hosts.RefreshRuntimeAsync(hostID)
	}
	return res, nil
}

func (s *Service) purgeDeletedAppState(ctx context.Context, hostID, appKey string) {
	if s.st == nil || s.st.DB == nil {
		return
	}
	cCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finalizeTimeout)
	defer cancel()

	now := time.Now().Unix()
	tx, err := s.st.DB.BeginTx(cCtx, nil)
	if err != nil {
		return
	}
	defer tx.Rollback()

	_, _ = tx.ExecContext(cCtx, `DELETE FROM finding WHERE service_key IN (SELECT key FROM service WHERE app_key=?)`, appKey)
	_, _ = tx.ExecContext(cCtx, `DELETE FROM ignore_rule WHERE service_key IN (SELECT key FROM service WHERE app_key=?)`, appKey)
	_, _ = tx.ExecContext(cCtx, `UPDATE service SET gone_at=? WHERE app_key=? AND gone_at IS NULL`, now, appKey)
	_, _ = tx.ExecContext(cCtx, `UPDATE app SET gone_at=? WHERE key=? AND gone_at IS NULL`, now, appKey)

	var rawSnap string
	if tx.QueryRowContext(cCtx, `SELECT trees_json FROM host_runtime_snapshot WHERE host_id=?`, hostID).Scan(&rawSnap) == nil && rawSnap != "" {
		var trees []platform.AppRuntimeTree
		if json.Unmarshal([]byte(rawSnap), &trees) == nil {
			filtered := make([]platform.AppRuntimeTree, 0, len(trees))
			for _, t := range trees {
				if t.Key != appKey {
					filtered = append(filtered, t)
				}
			}
			if b, mErr := json.Marshal(filtered); mErr == nil {
				_, _ = tx.ExecContext(cCtx, `UPDATE host_runtime_snapshot SET trees_json=?, updated_at=? WHERE host_id=?`, string(b), now, hostID)
			}
		}
	}

	_ = tx.Commit()
}
