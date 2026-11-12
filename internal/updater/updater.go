// Package updater 编排单服务一键更新、自定义版本切换、分层下载实时进度广播、整项目新版配置更新与有条件回退（立项文档 §7 / §8 / §9 / 附录 D.3 / D.4）。
package updater

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kukushouhou/DKStore/internal/host"
	"github.com/kukushouhou/DKStore/internal/i18n"
	"github.com/kukushouhou/DKStore/internal/ignore"
	"github.com/kukushouhou/DKStore/internal/inventory"
	"github.com/kukushouhou/DKStore/internal/platform"
	"github.com/kukushouhou/DKStore/internal/platform/docker"
	"github.com/kukushouhou/DKStore/internal/registry"
	"github.com/kukushouhou/DKStore/internal/store"
	"github.com/kukushouhou/DKStore/internal/version"
)

// Job 是对外返回与 SSE 广播的更新/回退任务模型。
type Job struct {
	ID                 string                `json:"id"`
	Kind               string                `json:"kind"` // upgrade | rollback
	HostID             string                `json:"hostId"`
	HostName           string                `json:"hostName"`
	AppKey             string                `json:"appKey"`
	AppName            string                `json:"appName"`
	ServiceKey         string                `json:"serviceKey"`
	ServiceName        string                `json:"serviceName"`
	Source             string                `json:"source"`
	Status             string                `json:"status"` // queued | running | succeeded | failed
	Phase              string                `json:"phase"`  // precheck | pulling | recreating | verifying | done
	Progress           platform.PullProgress `json:"progress"`
	OldImageRef        string                `json:"oldImageRef"`
	OldImageID         string                `json:"oldImageId,omitempty"`
	OldRepoDigest      string                `json:"oldRepoDigest,omitempty"`
	OldVersion         string                `json:"oldVersion,omitempty"`
	OldMatchedTags     []string              `json:"oldMatchedTags,omitempty"`
	TargetImageRef     string                `json:"targetImageRef"`
	TargetRepoDigest   string                `json:"targetRepoDigest,omitempty"`
	TargetVersion      string                `json:"targetVersion,omitempty"`
	TargetMatchedTags  []string              `json:"targetMatchedTags,omitempty"`
	NewImageID         string                `json:"newImageId,omitempty"`
	NewRepoDigest      string                `json:"newRepoDigest,omitempty"`
	RollbackEligible   bool                  `json:"rollbackEligible"`
	RollbackRef        string                `json:"rollbackRef,omitempty"`
	RollbackReason     string                `json:"rollbackReason,omitempty"`
	RollbackReasonText string                `json:"rollbackReasonText,omitempty"`
	ErrorCode          string                `json:"errorCode,omitempty"`
	ErrorMsg           string                `json:"errorMsg,omitempty"`
	Logs               string                `json:"logs,omitempty"`
	LogChunk           string                `json:"logChunk,omitempty"`
	LogOffset          int                   `json:"logOffset,omitempty"`
	StartedAt          int64                 `json:"startedAt"`
	FinishedAt         int64                 `json:"finishedAt,omitempty"`
}

// UpdateItemRequest 是单个服务的更新请求（支持自定义目标版本标签与更新后提醒策略，附录 D.3）。
type UpdateItemRequest struct {
	ServiceKey    string `json:"serviceKey"`
	TargetTag     string `json:"targetTag,omitempty"`
	PolicyMode    string `json:"policyMode,omitempty"` // "" | "all" | "skip_latest" | "prefix" | "forever"
	PolicyPrefix  string `json:"policyPrefix,omitempty"`
	SkipTargetTag string `json:"skipTargetTag,omitempty"`
}

// ComposeUpdateRequest 是整项目/单容器 YAML 按新版配置更新的异步任务请求（附录 E.4）。
type ComposeUpdateRequest struct {
	AppKey        string `json:"appKey"`
	Content       string `json:"content"`
	ServiceKey    string `json:"serviceKey,omitempty"`
	PolicyMode    string `json:"policyMode,omitempty"`
	PolicyPrefix  string `json:"policyPrefix,omitempty"`
	SkipTargetTag string `json:"skipTargetTag,omitempty"`
}

// StandaloneConfigUpdateRequest 是无 YAML 独立容器按新版配置（环境变量 + 端口映射）更新的异步任务请求（附录 E.4）。
type StandaloneConfigUpdateRequest struct {
	ServiceKey    string                 `json:"serviceKey"`
	TargetTag     string                 `json:"targetTag,omitempty"`
	PolicyMode    string                 `json:"policyMode,omitempty"`
	PolicyPrefix  string                 `json:"policyPrefix,omitempty"`
	SkipTargetTag string                 `json:"skipTargetTag,omitempty"`
	Env           []platform.EnvVar      `json:"env"`
	Ports         []platform.PortBinding `json:"ports"`
}

// ComposeInfo 是 GET /api/v1/apps/compose 的返回体（附录 D.4）。
type ComposeInfo struct {
	Key         string `json:"key"`
	HostID      string `json:"hostId,omitempty"`
	Name        string `json:"name,omitempty"`
	Source      string `json:"source,omitempty"`
	ComposePath string `json:"composePath,omitempty"`
	Editable    bool   `json:"editable"`
	Content     string `json:"content"`
	Reason      string `json:"reason,omitempty"`
}

// finalizeTimeout 是更新任务收尾写库使用的独立超时（审计 M6）。
const finalizeTimeout = 5 * time.Second

// Service 管理更新任务与历史回退。
type Service struct {
	log   *slog.Logger
	st    *store.Store
	hosts *host.Registry
	inv   *inventory.Service
	reg           *registry.Client
	ign           *ignore.Service
	onHostUpdated func(hostID string)

	mu          sync.Mutex
	activeJobs  map[string]Job // key: serviceKey -> running Job (含实时累积 Logs 与 LogOffset)
	subscribers map[chan Job]struct{}
	appLocks    sync.Map // key: appKey -> *sync.Mutex（按 Compose 项目串行化更新，审计 M4）
}

// New 创建 Updater 服务，并将上次异常退出残留的 running/queued 任务标记为 failed（审计 M6）。
func New(
	log *slog.Logger,
	st *store.Store,
	hosts *host.Registry,
	inv *inventory.Service,
	reg *registry.Client,
) *Service {
	s := &Service{
		log:         log,
		st:          st,
		hosts:       hosts,
		inv:         inv,
		reg:         reg,
		activeJobs:  make(map[string]Job),
		subscribers: make(map[chan Job]struct{}),
	}
	s.recoverInterruptedJobs()
	return s
}

// SetIgnore 注入忽略/版本锁定服务，用于在发起更新时一并应用用户的提醒策略。
func (s *Service) SetIgnore(ign *ignore.Service) {
	s.ign = ign
}

// SetOnHostUpdated 注入宿主机配置重建完成后的回调（用于异步任务完成后触发后台重检）。
func (s *Service) SetOnHostUpdated(fn func(hostID string)) {
	s.onHostUpdated = fn
}

func (s *Service) recoverInterruptedJobs() {
	if s.st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), finalizeTimeout)
	defer cancel()
	res, err := s.st.DB.ExecContext(ctx, `
		UPDATE update_job
		SET status='failed', phase='done', error_code='interrupted', error_msg=?, finished_at=COALESCE(finished_at, ?)
		WHERE status IN ('running', 'queued')`,
		i18n.EngineDetail(i18n.MsgUpdateInterrupted), time.Now().Unix(),
	)
	if err != nil {
		if s.log != nil {
			s.log.Warn("标记残留更新任务失败", "err", err)
		}
		return
	}
	if n, _ := res.RowsAffected(); n > 0 && s.log != nil {
		s.log.Info("已将残留的进行中更新任务标记为失败", "count", n)
	}
}

func (s *Service) lockApp(appKey string) func() {
	if appKey == "" {
		return func() {}
	}
	v, _ := s.appLocks.LoadOrStore(appKey, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// Subscribe 订阅实时更新任务进度事件（SSE）。
func (s *Service) Subscribe() (<-chan Job, func()) {
	ch := make(chan Job, 256)
	s.mu.Lock()
	s.subscribers[ch] = struct{}{}
	for _, j := range s.activeJobs {
		snap := j
		snap.Logs = ""
		snap.LogChunk = ""
		select {
		case ch <- snap:
		default:
		}
	}
	s.mu.Unlock()
	cancel := func() {
		s.mu.Lock()
		delete(s.subscribers, ch)
		close(ch)
		s.mu.Unlock()
	}
	return ch, cancel
}

func (s *Service) broadcast(j Job) {
	s.mu.Lock()
	if j.Status == "running" || j.Status == "queued" {
		stored := j
		stored.LogChunk = ""
		s.activeJobs[j.ServiceKey] = stored
	} else {
		delete(s.activeJobs, j.ServiceKey)
	}
	for ch := range s.subscribers {
		select {
		case ch <- j:
		default:
		}
	}
	s.mu.Unlock()
}

// StartUpdates 为用户指定的 1 个或多个服务启动升级任务（兼容旧签名）。
func (s *Service) StartUpdates(ctx context.Context, serviceKeys []string, lang i18n.Lang) ([]Job, error) {
	items := make([]UpdateItemRequest, 0, len(serviceKeys))
	for _, sk := range serviceKeys {
		if sk = strings.TrimSpace(sk); sk != "" {
			items = append(items, UpdateItemRequest{ServiceKey: sk})
		}
	}
	return s.StartUpdatesWithItems(ctx, items, lang)
}

// StartUpdatesWithItems 支持按服务指定自定义目标标签（校验仓库存在性）并应用更新后提醒策略（附录 D.3）。
func (s *Service) StartUpdatesWithItems(ctx context.Context, items []UpdateItemRequest, lang i18n.Lang) ([]Job, error) {
	var cleaned []UpdateItemRequest
	for _, it := range items {
		it.ServiceKey = strings.TrimSpace(it.ServiceKey)
		it.TargetTag = strings.TrimSpace(it.TargetTag)
		it.PolicyMode = strings.TrimSpace(it.PolicyMode)
		it.PolicyPrefix = strings.TrimSpace(it.PolicyPrefix)
		it.SkipTargetTag = strings.TrimSpace(it.SkipTargetTag)
		if it.ServiceKey != "" {
			cleaned = append(cleaned, it)
		}
	}
	if len(cleaned) == 0 {
		return nil, i18n.NewEngineError(i18n.MsgUpdateNoService)
	}

	var jobs []Job
	for _, item := range cleaned {
		j, alreadyRunning, prevFindingVer, err := s.prepareUpgradeJob(ctx, item, lang)
		if err != nil {
			return nil, err
		}
		if err := s.applyPostUpdatePolicy(ctx, item, prevFindingVer); err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
		if !alreadyRunning {
			go s.executeJob(j)
		}
	}
	return jobs, nil
}

func (s *Service) applyPostUpdatePolicy(ctx context.Context, item UpdateItemRequest, prevFindingVer string) error {
	if s.ign == nil || item.PolicyMode == "" {
		return nil
	}
	switch item.PolicyMode {
	case "all":
		return s.ign.Remove(ctx, item.ServiceKey)
	case "skip_latest":
		skipTag := item.SkipTargetTag
		if skipTag == "" {
			skipTag = prevFindingVer
		}
		return s.ign.SetWithOptions(ctx, ignore.SetOptions{
			ServiceKeys: []string{item.ServiceKey},
			Mode:        ignore.ModeThisVersion,
			TargetTag:   skipTag,
		})
	case "prefix":
		return s.ign.SetWithOptions(ctx, ignore.SetOptions{
			ServiceKeys: []string{item.ServiceKey},
			Mode:        ignore.ModePrefix,
			Prefix:      item.PolicyPrefix,
		})
	case "forever":
		return s.ign.SetWithOptions(ctx, ignore.SetOptions{
			ServiceKeys: []string{item.ServiceKey},
			Mode:        ignore.ModeForever,
		})
	default:
		return nil
	}
}

func (s *Service) prepareUpgradeJob(ctx context.Context, item UpdateItemRequest, lang i18n.Lang) (Job, bool, string, error) {
	serviceKey := item.ServiceKey
	// 审计 M4：在同一次 s.mu.Lock() 内完成 activeJobs 检查与占位写入，消除并发请求的 TOCTOU 竞态
	s.mu.Lock()
	if active, ok := s.activeJobs[serviceKey]; ok && (active.Status == "running" || active.Status == "queued") {
		s.mu.Unlock()
		return active, true, "", nil
	}
	s.activeJobs[serviceKey] = Job{ServiceKey: serviceKey, Status: "queued", Phase: "precheck"}
	s.mu.Unlock()

	cleanupPlaceholder := func() {
		s.mu.Lock()
		if cur, ok := s.activeJobs[serviceKey]; ok && cur.ID == "" {
			delete(s.activeJobs, serviceKey)
		}
		s.mu.Unlock()
	}

	var (
		appKey, appName, hostID, hostName, svcName, source      string
		oldImageRef, oldImageID, oldRepoDigest, svcPlat         string
		curTagsRaw, targetRef, targetDigest, targetVer          string
		targetTagsRaw, curVer                                   string
	)
	err := s.st.DB.QueryRowContext(ctx, `
		SELECT s.app_key, a.name, a.host_id, h.name, s.name, a.source,
		       COALESCE(s.image_ref,''), COALESCE(s.image_id,''), COALESCE(s.repo_digest,''), COALESCE(s.platform,''), COALESCE(s.current_tags_json,''),
		       COALESCE(f.current_version,''), COALESCE(f.target_ref,''), COALESCE(f.target_digest,''), COALESCE(f.target_version,''), COALESCE(f.target_tags_json,'')
		FROM service s
		JOIN app a ON s.app_key = a.key
		JOIN host h ON a.host_id = h.id
		LEFT JOIN finding f ON f.service_key = s.key
		WHERE s.key = ? AND s.gone_at IS NULL`, serviceKey).Scan(
		&appKey, &appName, &hostID, &hostName, &svcName, &source,
		&oldImageRef, &oldImageID, &oldRepoDigest, &svcPlat, &curTagsRaw,
		&curVer, &targetRef, &targetDigest, &targetVer, &targetTagsRaw,
	)
	if errors.Is(err, sql.ErrNoRows) {
		cleanupPlaceholder()
		return Job{}, false, "", i18n.NewEngineError(i18n.MsgUpdateServiceNotFound, serviceKey)
	}
	if err != nil {
		cleanupPlaceholder()
		return Job{}, false, "", err
	}
	if source == string(platform.SourceVendorManaged) {
		cleanupPlaceholder()
		return Job{}, false, "", errors.New(i18n.FormatUpdateError(lang, "vendor_managed_readonly", ""))
	}

	prevFindingVer := targetVer
	var oldMatched, targetMatched []string
	_ = json.Unmarshal([]byte(curTagsRaw), &oldMatched)
	_ = json.Unmarshal([]byte(targetTagsRaw), &targetMatched)

	// 若用户显式指定了自定义目标标签，先校验仓库中该标签存在并获取最新摘要
	if item.TargetTag != "" {
		parsedCurRef, err := version.ParseImageRef(oldImageRef)
		if err != nil {
			cleanupPlaceholder()
			return Job{}, false, "", err
		}
		customRefStr := parsedCurRef.WithTag(item.TargetTag)
		parsedTargetRef, err := version.ParseImageRef(customRefStr)
		if err != nil {
			cleanupPlaceholder()
			return Job{}, false, "", err
		}
		if s.reg != nil {
			mf, mfErr := s.reg.CheckManifest(ctx, parsedTargetRef, svcPlat, false)
			if mfErr != nil {
				cleanupPlaceholder()
				var re *registry.RegistryError
				if errors.As(mfErr, &re) && re.Kind == registry.ErrNotFound {
					return Job{}, false, "", i18n.NewEngineError(i18n.MsgUpdateTagNotFound, item.TargetTag)
				}
				return Job{}, false, "", errors.New(i18n.LocalizeError(lang, mfErr))
			}
			if mf != nil && mf.RepoDigest != "" {
				targetDigest = mf.RepoDigest
			} else {
				targetDigest = ""
			}
		}
		if hasCustomMirrorPrefix(oldImageRef) {
			targetRef = replaceRefTag(oldImageRef, item.TargetTag)
		} else {
			targetRef = customRefStr
		}
		targetVer = item.TargetTag
		targetMatched = []string{item.TargetTag}
	} else {
		if targetRef == "" {
			targetRef = oldImageRef
		} else if hasCustomMirrorPrefix(oldImageRef) {
			// 若用户原声明镜像使用了加速前缀（如 docker.1ms.run/postgres:17），升级新标签时保留其原有前缀习惯
			if _, newTag := splitRefAndTag(targetRef); newTag != "" {
				targetRef = replaceRefTag(oldImageRef, newTag)
			}
		}
	}

	if curVer == "" {
		if pr, err := version.ParseImageRef(oldImageRef); err == nil {
			curVer = version.SelectBestDisplayTag(pr.Tag, oldMatched)
			if curVer == "" && version.ParseTag(pr.Tag).Scheme != version.SchemeUnknown {
				curVer = pr.Tag
			}
		}
	}

	// 计算是否具备可验证回退条件（立项文档 §8.2 / A10 / A12）
	rollbackEligible := false
	rollbackRef := ""
	rollbackReason := ""
	cleanCurForRollback := curVer
	if base, _, ok := strings.Cut(cleanCurForRollback, " ("); ok {
		cleanCurForRollback = strings.TrimSpace(base)
	}
	if cleanCurForRollback != "" && version.ParseTag(cleanCurForRollback).Scheme != version.SchemeUnknown {
		rollbackEligible = true
		rollbackRef = replaceRefTag(oldImageRef, cleanCurForRollback)
	} else {
		rollbackReason = "no_exact_version_tag"
	}

	now := time.Now().Unix()
	job := Job{
		ID:                 newJobID(),
		Kind:               "upgrade",
		HostID:             hostID,
		HostName:           hostName,
		AppKey:             appKey,
		AppName:            appName,
		ServiceKey:         serviceKey,
		ServiceName:        svcName,
		Source:             source,
		Status:             "running",
		Phase:              "precheck",
		Progress:           platform.PullProgress{Phase: "precheck", StatusText: "Preparing update", Percent: 5},
		OldImageRef:        oldImageRef,
		OldImageID:         oldImageID,
		OldRepoDigest:      oldRepoDigest,
		OldVersion:         curVer,
		OldMatchedTags:     oldMatched,
		TargetImageRef:     targetRef,
		TargetRepoDigest:   targetDigest,
		TargetVersion:      targetVer,
		TargetMatchedTags:  targetMatched,
		RollbackEligible:   rollbackEligible,
		RollbackRef:        rollbackRef,
		RollbackReason:     rollbackReason,
		RollbackReasonText: i18n.FormatRollbackReason(lang, rollbackReason),
		StartedAt:          now,
	}
	initHeader := formatJobHeader(job)
	job.Logs = initHeader
	job.LogChunk = initHeader
	job.LogOffset = len(initHeader)
	if err := s.insertJob(ctx, job); err != nil {
		cleanupPlaceholder()
		return Job{}, false, "", err
	}
	s.broadcast(job)
	return job, false, prevFindingVer, nil
}

// GetAppCompose 读取指定 App（Compose / DSM Project）当前生效的 compose.yaml 内容（附录 D.4）。
func (s *Service) GetAppCompose(ctx context.Context, appKey string, lang i18n.Lang) (*ComposeInfo, error) {
	var hostID, appName, source, projJSON string
	err := s.st.DB.QueryRowContext(ctx, `
		SELECT host_id, name, source, COALESCE(project_json,'')
		FROM app WHERE key=? AND gone_at IS NULL`, strings.TrimSpace(appKey)).Scan(
		&hostID, &appName, &source, &projJSON,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, i18n.NewEngineError(i18n.MsgComposeNotEditable)
	}
	if err != nil {
		return nil, err
	}
	if source == string(platform.SourceVendorManaged) || source == string(platform.SourceStandalone) {
		return &ComposeInfo{
			Key:      appKey,
			HostID:   hostID,
			Name:     appName,
			Source:   source,
			Editable: false,
			Reason:   i18n.EngineMessage(lang, i18n.MsgComposeNotEditable),
		}, nil
	}

	var proj platform.ProjectInfo
	if projJSON != "" {
		_ = json.Unmarshal([]byte(projJSON), &proj)
	}
	if proj.Name == "" {
		proj.Name = appName
	}

	ad, _, err := s.hosts.OpenAdapter(ctx, hostID)
	if err != nil {
		return &ComposeInfo{
			Key:      appKey,
			HostID:   hostID,
			Name:     appName,
			Source:   source,
			Editable: false,
			Reason:   i18n.LocalizeErr(lang, err),
		}, nil
	}
	defer ad.Close()

	content, err := ad.GetProjectCompose(ctx, &proj)
	if err != nil {
		return &ComposeInfo{
			Key:      appKey,
			HostID:   hostID,
			Name:     appName,
			Source:   source,
			Editable: false,
			Reason:   i18n.LocalizeErr(lang, err),
		}, nil
	}
	composePath := ""
	if len(proj.ConfigFiles) > 0 {
		composePath = proj.ConfigFiles[0]
	} else if proj.WorkingDir != "" {
		composePath = strings.TrimRight(proj.WorkingDir, "/") + "/compose.yaml"
	}
	return &ComposeInfo{
		Key:         appKey,
		HostID:      hostID,
		Name:        appName,
		Source:      source,
		ComposePath: composePath,
		Editable:    true,
		Content:     content,
	}, nil
}

// StartComposeUpdate 校验新版 Compose YAML 后启动异步跟踪任务（SSE 实时推送分层进度与原始 xterm 终端流，附录 E.4）。
func (s *Service) StartComposeUpdate(ctx context.Context, req ComposeUpdateRequest, lang i18n.Lang) (*Job, error) {
	appKey := strings.TrimSpace(req.AppKey)
	svcKey := strings.TrimSpace(req.ServiceKey)
	req.PolicyMode = strings.TrimSpace(req.PolicyMode)
	req.PolicyPrefix = strings.TrimSpace(req.PolicyPrefix)
	req.SkipTargetTag = strings.TrimSpace(req.SkipTargetTag)

	svcImages, err := docker.ValidateComposeYAML(req.Content)
	if err != nil {
		return nil, err
	}

	var hostID, hostName, appName, source, projJSON string
	err = s.st.DB.QueryRowContext(ctx, `
		SELECT a.host_id, h.name, a.name, a.source, COALESCE(a.project_json,'')
		FROM app a
		JOIN host h ON a.host_id = h.id
		WHERE a.key=? AND a.gone_at IS NULL`, appKey).Scan(
		&hostID, &hostName, &appName, &source, &projJSON,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, i18n.NewEngineError(i18n.MsgComposeNotEditable)
	}
	if err != nil {
		return nil, err
	}
	if source == string(platform.SourceVendorManaged) {
		return nil, errors.New(i18n.FormatUpdateError(lang, "vendor_managed_readonly", ""))
	}
	if source == string(platform.SourceStandalone) {
		return nil, i18n.NewEngineError(i18n.MsgComposeNotEditable)
	}

	var proj platform.ProjectInfo
	if projJSON != "" {
		_ = json.Unmarshal([]byte(projJSON), &proj)
	}
	if proj.Name == "" {
		proj.Name = appName
	}

	// 查询代表服务元数据（若前端指定了 serviceKey 则取该服务，否则取该项目下第一个服务）
	var svcName, oldImageRef, oldImageID, oldRepoDigest, curVer, prevFindingVer string
	if svcKey != "" {
		_ = s.st.DB.QueryRowContext(ctx, `
			SELECT s.name, COALESCE(s.image_ref,''), COALESCE(s.image_id,''), COALESCE(s.repo_digest,''),
			       COALESCE(f.current_version,''), COALESCE(f.target_version,'')
			FROM service s
			LEFT JOIN finding f ON f.service_key = s.key
			WHERE s.key=? AND s.app_key=? AND s.gone_at IS NULL`, svcKey, appKey).Scan(
			&svcName, &oldImageRef, &oldImageID, &oldRepoDigest, &curVer, &prevFindingVer,
		)
	}
	if svcName == "" {
		_ = s.st.DB.QueryRowContext(ctx, `
			SELECT s.key, s.name, COALESCE(s.image_ref,''), COALESCE(s.image_id,''), COALESCE(s.repo_digest,''),
			       COALESCE(f.current_version,''), COALESCE(f.target_version,'')
			FROM service s
			LEFT JOIN finding f ON f.service_key = s.key
			WHERE s.app_key=? AND s.gone_at IS NULL
			ORDER BY s.name ASC LIMIT 1`, appKey).Scan(
			&svcKey, &svcName, &oldImageRef, &oldImageID, &oldRepoDigest, &curVer, &prevFindingVer,
		)
	}
	if svcKey == "" {
		svcKey = appKey
	}
	if svcName == "" {
		svcName = appName
	}

	targetRef := svcImages[svcName]
	if targetRef == "" {
		for _, img := range svcImages {
			targetRef = img
			break
		}
	}
	if targetRef == "" {
		targetRef = oldImageRef
	}
	_, targetVer := splitRefAndTag(targetRef)

	s.mu.Lock()
	if active, ok := s.activeJobs[svcKey]; ok && (active.Status == "running" || active.Status == "queued") {
		s.mu.Unlock()
		return &active, nil
	}
	s.activeJobs[svcKey] = Job{ServiceKey: svcKey, Status: "queued", Phase: "precheck"}
	s.mu.Unlock()

	cleanupPlaceholder := func() {
		s.mu.Lock()
		if cur, ok := s.activeJobs[svcKey]; ok && cur.ID == "" {
			delete(s.activeJobs, svcKey)
		}
		s.mu.Unlock()
	}

	if req.ServiceKey != "" && req.PolicyMode != "" {
		if err := s.applyPostUpdatePolicy(ctx, UpdateItemRequest{
			ServiceKey:    req.ServiceKey,
			PolicyMode:    req.PolicyMode,
			PolicyPrefix:  req.PolicyPrefix,
			SkipTargetTag: req.SkipTargetTag,
		}, prevFindingVer); err != nil {
			cleanupPlaceholder()
			return nil, err
		}
	}

	now := time.Now().Unix()
	job := Job{
		ID:                 newJobID(),
		Kind:               "upgrade",
		HostID:             hostID,
		HostName:           hostName,
		AppKey:             appKey,
		AppName:            appName,
		ServiceKey:         svcKey,
		ServiceName:        svcName,
		Source:             source,
		Status:             "running",
		Phase:              "precheck",
		Progress:           platform.PullProgress{Phase: "precheck", StatusText: "Preparing Compose config update", Percent: 5},
		OldImageRef:        oldImageRef,
		OldImageID:         oldImageID,
		OldRepoDigest:      oldRepoDigest,
		OldVersion:         curVer,
		TargetImageRef:     targetRef,
		TargetVersion:      targetVer,
		RollbackEligible:   false,
		RollbackReason:     "no_exact_version_tag",
		RollbackReasonText: i18n.FormatRollbackReason(lang, "no_exact_version_tag"),
		StartedAt:          now,
	}
	initHeader := formatJobHeader(job)
	job.Logs = initHeader
	job.LogChunk = initHeader
	job.LogOffset = len(initHeader)
	if err := s.insertJob(ctx, job); err != nil {
		cleanupPlaceholder()
		return nil, err
	}
	s.broadcast(job)
	go s.executeComposeJob(job, proj, req.Content)
	return &job, nil
}

// GetServiceStandaloneConfig 读取无 YAML 独立容器（source == "standalone"）的当前环境变量、端口映射与挂载目录（附录 E.4）。
func (s *Service) GetServiceStandaloneConfig(ctx context.Context, serviceKey string, lang i18n.Lang) (*platform.StandaloneConfig, error) {
	serviceKey = strings.TrimSpace(serviceKey)
	var hostID, appName, svcName, source, imageRef string
	err := s.st.DB.QueryRowContext(ctx, `
		SELECT a.host_id, a.name, s.name, a.source, COALESCE(s.image_ref,'')
		FROM service s
		JOIN app a ON s.app_key = a.key
		WHERE s.key=? AND s.gone_at IS NULL`, serviceKey).Scan(
		&hostID, &appName, &svcName, &source, &imageRef,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, i18n.NewEngineError(i18n.MsgUpdateServiceNotFound, serviceKey)
	}
	if err != nil {
		return nil, err
	}
	if source != string(platform.SourceStandalone) {
		return &platform.StandaloneConfig{
			Editable: false,
			Reason:   i18n.EngineMessage(lang, i18n.MsgComposeNotEditable),
			ImageRef: imageRef,
			Env:      []platform.EnvVar{},
			Ports:    []platform.PortBinding{},
			Volumes:  []platform.VolumeBinding{},
		}, nil
	}

	ad, _, err := s.hosts.OpenAdapter(ctx, hostID)
	if err != nil {
		return &platform.StandaloneConfig{
			Editable: false,
			Reason:   i18n.LocalizeErr(lang, err),
			ImageRef: imageRef,
			Env:      []platform.EnvVar{},
			Ports:    []platform.PortBinding{},
			Volumes:  []platform.VolumeBinding{},
		}, nil
	}
	defer ad.Close()

	scAd, ok := ad.(platform.StandaloneConfigAdapter)
	if !ok {
		return &platform.StandaloneConfig{
			Editable: false,
			Reason:   i18n.EngineMessage(lang, i18n.MsgUpdateAdapterNoUpdate),
			ImageRef: imageRef,
			Env:      []platform.EnvVar{},
			Ports:    []platform.PortBinding{},
			Volumes:  []platform.VolumeBinding{},
		}, nil
	}
	cfg, err := scAd.GetStandaloneConfig(ctx, appName, svcName)
	if err != nil {
		return &platform.StandaloneConfig{
			Editable: false,
			Reason:   i18n.LocalizeErr(lang, err),
			ImageRef: imageRef,
			Env:      []platform.EnvVar{},
			Ports:    []platform.PortBinding{},
			Volumes:  []platform.VolumeBinding{},
		}, nil
	}
	return cfg, nil
}

// StartStandaloneConfigUpdate 启动独立容器按新版配置（镜像标签 + 环境变量 + 端口映射）重建任务（附录 E.4）。
func (s *Service) StartStandaloneConfigUpdate(ctx context.Context, req StandaloneConfigUpdateRequest, lang i18n.Lang) (*Job, error) {
	svcKey := strings.TrimSpace(req.ServiceKey)
	if svcKey == "" {
		return nil, i18n.NewEngineError(i18n.MsgUpdateNoService)
	}
	var source string
	err := s.st.DB.QueryRowContext(ctx, `
		SELECT a.source
		FROM service s
		JOIN app a ON s.app_key = a.key
		WHERE s.key=? AND s.gone_at IS NULL`, svcKey).Scan(&source)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, i18n.NewEngineError(i18n.MsgUpdateServiceNotFound, svcKey)
	}
	if err != nil {
		return nil, err
	}
	if source == string(platform.SourceVendorManaged) {
		return nil, errors.New(i18n.FormatUpdateError(lang, "vendor_managed_readonly", ""))
	}
	if source != string(platform.SourceStandalone) {
		return nil, i18n.NewEngineError(i18n.MsgComposeNotEditable)
	}

	item := UpdateItemRequest{
		ServiceKey:    svcKey,
		TargetTag:     strings.TrimSpace(req.TargetTag),
		PolicyMode:    strings.TrimSpace(req.PolicyMode),
		PolicyPrefix:  strings.TrimSpace(req.PolicyPrefix),
		SkipTargetTag: strings.TrimSpace(req.SkipTargetTag),
	}
	j, alreadyRunning, prevFindingVer, err := s.prepareUpgradeJob(ctx, item, lang)
	if err != nil {
		return nil, err
	}
	if err := s.applyPostUpdatePolicy(ctx, item, prevFindingVer); err != nil {
		return nil, err
	}
	if !alreadyRunning {
		envCopy := append([]platform.EnvVar(nil), req.Env...)
		portsCopy := append([]platform.PortBinding(nil), req.Ports...)
		go s.executeStandaloneConfigJob(j, envCopy, portsCopy)
	}
	return &j, nil
}

// UpdateAppCompose 执行整项目按新版 Compose 配置同步更新（保留供同步调用方使用，附录 D.4）。
func (s *Service) UpdateAppCompose(ctx context.Context, appKey, newYAML string, lang i18n.Lang) (string, error) {
	appKey = strings.TrimSpace(appKey)
	var hostID, appName, source, projJSON string
	err := s.st.DB.QueryRowContext(ctx, `
		SELECT host_id, name, source, COALESCE(project_json,'')
		FROM app WHERE key=? AND gone_at IS NULL`, appKey).Scan(
		&hostID, &appName, &source, &projJSON,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return "", i18n.NewEngineError(i18n.MsgComposeNotEditable)
	}
	if err != nil {
		return "", err
	}
	if source == string(platform.SourceVendorManaged) {
		return "", errors.New(i18n.FormatUpdateError(lang, "vendor_managed_readonly", ""))
	}
	if source == string(platform.SourceStandalone) {
		return "", i18n.NewEngineError(i18n.MsgComposeNotEditable)
	}

	var proj platform.ProjectInfo
	if projJSON != "" {
		_ = json.Unmarshal([]byte(projJSON), &proj)
	}
	if proj.Name == "" {
		proj.Name = appName
	}

	unlockApp := s.lockApp(appKey)
	defer unlockApp()

	ad, _, err := s.hosts.OpenAdapter(ctx, hostID)
	if err != nil {
		return "", errors.New(i18n.LocalizeErr(lang, err))
	}
	defer ad.Close()

	if err := ad.UpdateProjectCompose(ctx, appName, &proj, newYAML, nil); err != nil {
		var ue *platform.UpdateError
		if errors.As(err, &ue) {
			return "", errors.New(i18n.FormatUpdateError(lang, ue.Code, ue.Message))
		}
		return "", errors.New(i18n.LocalizeErr(lang, err))
	}

	if s.inv != nil {
		_, _, _ = s.inv.SyncHost(ctx, hostID)
	}
	return hostID, nil
}

// jobProgressTracker 维护运行中任务的单调百分比、镜像层状态快照与实时原始终端日志缓冲（附录 E.1 / E.3）。
type jobProgressTracker struct {
	svc         *Service
	logBuf      strings.Builder
	prevPercent int
	prevPhase   string
	prevLayers  []platform.LayerProgress
	lastPersist time.Time
}

func newJobProgressTracker(s *Service, j *Job) *jobProgressTracker {
	t := &jobProgressTracker{
		svc:         s,
		prevPercent: j.Progress.Percent,
		prevPhase:   j.Phase,
	}
	if t.prevPercent < 5 {
		t.prevPercent = 5
		j.Progress.Percent = 5
	}
	if j.Logs != "" {
		t.logBuf.WriteString(j.Logs)
	} else {
		hdr := formatJobHeader(*j)
		t.logBuf.WriteString(hdr)
		j.Logs = hdr
		j.LogOffset = len(hdr)
	}
	return t
}

func formatJobHeader(j Job) string {
	oldRef := j.OldImageRef
	if oldRef == "" {
		oldRef = "current"
	}
	tgtRef := j.TargetImageRef
	if tgtRef == "" {
		tgtRef = oldRef
	}
	return fmt.Sprintf("\x1b[36m==> [%s] Preparing update: %s / %s (%s -> %s)\x1b[0m\r\n",
		time.Now().Format("15:04:05"), j.AppName, j.ServiceName, oldRef, tgtRef)
}

func cloneLayerSlice(in []platform.LayerProgress) []platform.LayerProgress {
	if len(in) == 0 {
		return nil
	}
	out := make([]platform.LayerProgress, len(in))
	copy(out, in)
	return out
}

func markLayersDone(in []platform.LayerProgress) []platform.LayerProgress {
	if len(in) == 0 {
		return nil
	}
	out := make([]platform.LayerProgress, len(in))
	for i, lp := range in {
		lp.Status = "done"
		if lp.Total > 0 {
			lp.Current = lp.Total
		}
		lp.Percent = 100
		out[i] = lp
	}
	return out
}

func (t *jobProgressTracker) handleProgress(ctx context.Context, j *Job, p platform.PullProgress) {
	// 1. 严格保持总进度百分比单调递增，禁止回退（附录 E.1）
	if p.Percent < t.prevPercent {
		p.Percent = t.prevPercent
	} else {
		t.prevPercent = p.Percent
	}

	// 2. 维护镜像层状态并在进入重建/校验阶段后保留已完成层展示
	if len(p.Layers) > 0 {
		t.prevLayers = cloneLayerSlice(p.Layers)
	} else if len(t.prevLayers) > 0 {
		if p.Phase == "recreating" || p.Phase == "verifying" || p.Phase == "done" {
			t.prevLayers = markLayersDone(t.prevLayers)
			p.Layers = cloneLayerSlice(t.prevLayers)
			p.TotalLayers = len(t.prevLayers)
			p.CompletedLayers = len(t.prevLayers)
		} else {
			p.Layers = cloneLayerSlice(t.prevLayers)
			if p.TotalLayers == 0 {
				p.TotalLayers = len(t.prevLayers)
			}
		}
	}

	// 3. 追加原始终端增量流并同步 activeJobs 内存态
	chunk := p.LogChunk
	if chunk == "" && p.Phase != t.prevPhase && p.StatusText != "" {
		chunk = fmt.Sprintf("\x1b[90m[%s]\x1b[0m %s\r\n", time.Now().Format("15:04:05"), p.StatusText)
	}
	if chunk != "" {
		t.logBuf.WriteString(chunk)
	}

	phaseChanged := p.Phase != t.prevPhase
	t.prevPhase = p.Phase

	progForDB := p
	progForDB.LogChunk = ""
	j.Phase = p.Phase
	j.Progress = progForDB
	j.LogChunk = chunk
	j.Logs = t.logBuf.String()
	j.LogOffset = t.logBuf.Len()

	if phaseChanged || time.Since(t.lastPersist) >= 400*time.Millisecond {
		t.svc.updateJobProgressAndLogs(ctx, j.ID, progForDB, j.Logs)
		t.lastPersist = time.Now()
	}
	t.svc.broadcast(*j)
}

func (t *jobProgressTracker) fail(j Job, code, msg string) {
	displayErr := i18n.FormatUpdateError(i18n.ZhCN, code, msg)
	errChunk := fmt.Sprintf("\r\n\x1b[31m✖ ERROR: %s\x1b[0m\r\n", displayErr)
	t.logBuf.WriteString(errChunk)
	j.LogChunk = errChunk
	j.Logs = t.logBuf.String()
	j.LogOffset = t.logBuf.Len()
	t.svc.finishJobFailed(j, code, msg, j.Logs)
}

func (t *jobProgressTracker) succeed(j *Job, extraLogs []string) {
	if len(extraLogs) > 0 && t.logBuf.Len() == 0 {
		t.logBuf.WriteString(strings.Join(extraLogs, "\r\n") + "\r\n")
	}
	doneChunk := "\x1b[32m✔ Update completed successfully.\x1b[0m\r\n"
	t.logBuf.WriteString(doneChunk)
	doneLayers := markLayersDone(t.prevLayers)

	j.Status = "succeeded"
	j.Phase = "done"
	j.Progress = platform.PullProgress{
		Phase:           "done",
		StatusText:      "Completed",
		Percent:         100,
		TotalLayers:     len(doneLayers),
		CompletedLayers: len(doneLayers),
		Layers:          doneLayers,
	}
	j.LogChunk = doneChunk
	j.Logs = t.logBuf.String()
	j.LogOffset = t.logBuf.Len()
}

func (s *Service) executeJob(j Job) {
	tracker := newJobProgressTracker(s, &j)
	defer func() {
		if r := recover(); r != nil {
			if s.log != nil {
				s.log.Error("更新任务发生 panic", "job", j.ID, "recover", r)
			}
			tracker.fail(j, "recreate_failed", i18n.EngineDetail(i18n.MsgUpdatePanic, fmt.Sprint(r)))
		}
	}()

	// 审计 M4：按 AppKey 串行化同一 Compose 项目的多个服务更新
	unlockApp := s.lockApp(j.AppKey)
	defer unlockApp()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	// 读取 App 的 ProjectInfo
	var projJSON string
	_ = s.st.DB.QueryRowContext(ctx, `SELECT COALESCE(project_json,'') FROM app WHERE key=?`, j.AppKey).Scan(&projJSON)
	var proj *platform.ProjectInfo
	if projJSON != "" {
		var p platform.ProjectInfo
		if json.Unmarshal([]byte(projJSON), &p) == nil {
			proj = &p
		}
	}

	ad, h, err := s.hosts.OpenAdapter(ctx, j.HostID)
	if err != nil {
		tracker.fail(j, "connect_failed", i18n.DetailOf(err))
		return
	}
	defer ad.Close()

	upAd, ok := ad.(platform.UpdateCapableAdapter)
	if !ok {
		tracker.fail(j, "capability_unsupported", i18n.EngineDetail(i18n.MsgUpdateAdapterNoUpdate))
		return
	}

	var regAuth string
	if pr, err := version.ParseImageRef(j.TargetImageRef); err == nil {
		regAuth = s.reg.DockerAuthHeader(ctx, pr.Registry)
	}

	res, err := upAd.UpdateService(ctx, platform.UpdateRequest{
		AppName:         j.AppName,
		ServiceName:     j.ServiceName,
		Source:          platform.SourceKind(j.Source),
		Project:         proj,
		ExpectedImageID: j.OldImageID,
		OldImageRef:     j.OldImageRef,
		TargetImageRef:  j.TargetImageRef,
		RegistryAuthB64: regAuth,
	}, func(p platform.PullProgress) {
		tracker.handleProgress(ctx, &j, p)
	})

	if err != nil {
		code := "recreate_failed"
		msg := i18n.DetailOf(err)
		var ue *platform.UpdateError
		if errors.As(err, &ue) {
			code = ue.Code
			msg = ue.Message
		}
		code, msg = normalizeUpdateError(code, msg, h)
		tracker.fail(j, code, msg)
		return
	}

	s.finalizeSucceededServiceJob(&j, tracker, res)
}

func (s *Service) executeComposeJob(j Job, proj platform.ProjectInfo, newYAML string) {
	tracker := newJobProgressTracker(s, &j)
	defer func() {
		if r := recover(); r != nil {
			if s.log != nil {
				s.log.Error("Compose 配置更新任务发生 panic", "job", j.ID, "recover", r)
			}
			tracker.fail(j, "recreate_failed", i18n.EngineDetail(i18n.MsgUpdatePanic, fmt.Sprint(r)))
		}
	}()

	unlockApp := s.lockApp(j.AppKey)
	defer unlockApp()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	ad, h, err := s.hosts.OpenAdapter(ctx, j.HostID)
	if err != nil {
		tracker.fail(j, "connect_failed", i18n.DetailOf(err))
		return
	}
	defer ad.Close()

	if err := ad.UpdateProjectCompose(ctx, j.AppName, &proj, newYAML, func(p platform.PullProgress) {
		tracker.handleProgress(ctx, &j, p)
	}); err != nil {
		code := "recreate_failed"
		msg := i18n.DetailOf(err)
		var ue *platform.UpdateError
		if errors.As(err, &ue) {
			code = ue.Code
			msg = ue.Message
		}
		code, msg = normalizeUpdateError(code, msg, h)
		tracker.fail(j, code, msg)
		return
	}

	if s.inv != nil {
		_, _, _ = s.inv.SyncHost(ctx, j.HostID)
	}

	finished := time.Now().Unix()
	tracker.succeed(&j, nil)
	j.FinishedAt = finished

	fCtx, fCancel := context.WithTimeout(context.Background(), finalizeTimeout)
	defer fCancel()
	progBytes, _ := json.Marshal(j.Progress)
	_, _ = s.st.DB.ExecContext(fCtx, `
		UPDATE update_job
		SET status='succeeded', phase='done', progress_json=?, logs_text=?, finished_at=?
		WHERE id=?`,
		string(progBytes), j.Logs, finished, j.ID,
	)
	s.broadcast(j)

	if s.onHostUpdated != nil {
		s.onHostUpdated(j.HostID)
	}
}

func (s *Service) executeStandaloneConfigJob(j Job, env []platform.EnvVar, ports []platform.PortBinding) {
	tracker := newJobProgressTracker(s, &j)
	defer func() {
		if r := recover(); r != nil {
			if s.log != nil {
				s.log.Error("独立容器配置更新任务发生 panic", "job", j.ID, "recover", r)
			}
			tracker.fail(j, "recreate_failed", i18n.EngineDetail(i18n.MsgUpdatePanic, fmt.Sprint(r)))
		}
	}()

	unlockApp := s.lockApp(j.AppKey)
	defer unlockApp()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	ad, h, err := s.hosts.OpenAdapter(ctx, j.HostID)
	if err != nil {
		tracker.fail(j, "connect_failed", i18n.DetailOf(err))
		return
	}
	defer ad.Close()

	scAd, ok := ad.(platform.StandaloneConfigAdapter)
	if !ok {
		tracker.fail(j, "capability_unsupported", i18n.EngineDetail(i18n.MsgUpdateAdapterNoUpdate))
		return
	}

	var regAuth string
	if pr, err := version.ParseImageRef(j.TargetImageRef); err == nil {
		regAuth = s.reg.DockerAuthHeader(ctx, pr.Registry)
	}

	res, err := scAd.UpdateStandaloneConfig(ctx, platform.StandaloneUpdateRequest{
		AppName:         j.AppName,
		ServiceName:     j.ServiceName,
		ExpectedImageID: j.OldImageID,
		OldImageRef:     j.OldImageRef,
		TargetImageRef:  j.TargetImageRef,
		RegistryAuthB64: regAuth,
		Env:             env,
		Ports:           ports,
	}, func(p platform.PullProgress) {
		tracker.handleProgress(ctx, &j, p)
	})
	if err != nil {
		code := "recreate_failed"
		msg := i18n.DetailOf(err)
		var ue *platform.UpdateError
		if errors.As(err, &ue) {
			code = ue.Code
			msg = ue.Message
		}
		code, msg = normalizeUpdateError(code, msg, h)
		tracker.fail(j, code, msg)
		return
	}

	s.finalizeSucceededServiceJob(&j, tracker, res)
	if s.onHostUpdated != nil {
		s.onHostUpdated(j.HostID)
	}
}

func (s *Service) finalizeSucceededServiceJob(j *Job, tracker *jobProgressTracker, res *platform.UpdateResult) {
	finished := time.Now().Unix()
	tracker.succeed(j, res.Logs)
	j.NewImageID = res.NewImageID
	if res.NewRepoDigest != "" {
		j.NewRepoDigest = res.NewRepoDigest
	} else {
		j.NewRepoDigest = j.TargetRepoDigest
	}
	j.FinishedAt = finished

	// 审计 M6：收尾写库使用独立超时上下文，避免 15 分钟执行超时后写库失败导致任务卡死
	fCtx, fCancel := context.WithTimeout(context.Background(), finalizeTimeout)
	defer fCancel()

	progBytes, _ := json.Marshal(j.Progress)
	_, _ = s.st.DB.ExecContext(fCtx, `
		UPDATE update_job
		SET status='succeeded', phase='done', progress_json=?,
		    new_image_id=?, new_repo_digest=?,
		    old_compose_path=?, old_compose_sha256=?,
		    logs_text=?, finished_at=?
		WHERE id=?`,
		string(progBytes), j.NewImageID, j.NewRepoDigest,
		res.ComposePath, res.ComposeSHA256, j.Logs, finished, j.ID,
	)

	// 同步更新库存与 finding 状态为已是最新（resolved）
	newDisplayVer := j.TargetVersion
	if base, _, ok := strings.Cut(newDisplayVer, " ("); ok && strings.TrimSpace(base) != "" {
		newDisplayVer = strings.TrimSpace(base)
	}
	if newDisplayVer == "" {
		if _, t := splitRefAndTag(j.TargetImageRef); t != "" {
			newDisplayVer = t
		}
	}
	targetTagsJSON, _ := json.Marshal(j.TargetMatchedTags)
	_, _ = s.st.DB.ExecContext(fCtx, `
		UPDATE service
		SET image_ref=?, image_id=?, repo_digest=CASE WHEN ?<>'' THEN ? ELSE repo_digest END,
		    current_tags_json=?, updated_at=?
		WHERE key=?`,
		j.TargetImageRef, j.NewImageID, j.NewRepoDigest, j.NewRepoDigest, string(targetTagsJSON), finished, j.ServiceKey,
	)
	_, _ = s.st.DB.ExecContext(fCtx, `
		UPDATE finding
		SET kind='none', current_version=?, target_version='', target_ref='', target_digest='', checked_at=?
		WHERE service_key=?`,
		newDisplayVer, finished, j.ServiceKey,
	)
	s.broadcast(*j)
}

func (s *Service) finishJobFailed(j Job, code, msg, logs string) {
	finished := time.Now().Unix()
	j.Status = "failed"
	j.Phase = "done"
	j.ErrorCode = code
	j.ErrorMsg = i18n.FormatUpdateError(i18n.ZhCN, code, msg)
	if logs == "" {
		errChunk := fmt.Sprintf("\r\n\x1b[31m✖ ERROR: %s\x1b[0m\r\n", j.ErrorMsg)
		logs = j.Logs + errChunk
		j.LogChunk = errChunk
	}
	j.Logs = logs
	j.LogOffset = len(logs)
	j.FinishedAt = finished
	progBytes, _ := json.Marshal(j.Progress)

	fCtx, cancel := context.WithTimeout(context.Background(), finalizeTimeout)
	defer cancel()
	_, _ = s.st.DB.ExecContext(fCtx, `
		UPDATE update_job
		SET status='failed', phase='done', progress_json=?, error_code=?, error_msg=?, logs_text=?, finished_at=?
		WHERE id=?`,
		string(progBytes), code, msg, logs, finished, j.ID,
	)
	s.broadcast(j)
}

func (s *Service) insertJob(ctx context.Context, j Job) error {
	progForDB := j.Progress
	progForDB.LogChunk = ""
	progBytes, _ := json.Marshal(progForDB)
	oldTags, _ := json.Marshal(j.OldMatchedTags)
	tgtTags, _ := json.Marshal(j.TargetMatchedTags)
	eligibleInt := 0
	if j.RollbackEligible {
		eligibleInt = 1
	}
	_, err := s.st.DB.ExecContext(ctx, `
		INSERT INTO update_job(
			id, kind, host_id, host_name, app_key, app_name, service_key, service_name, source,
			status, phase, progress_json,
			old_image_ref, old_image_id, old_repo_digest, old_version, old_matched_tags_json,
			target_image_ref, target_repo_digest, target_version, target_matched_tags_json,
			rollback_eligible, rollback_ref, rollback_reason, logs_text, started_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.ID, j.Kind, j.HostID, j.HostName, j.AppKey, j.AppName, j.ServiceKey, j.ServiceName, j.Source,
		j.Status, j.Phase, string(progBytes),
		j.OldImageRef, j.OldImageID, j.OldRepoDigest, j.OldVersion, string(oldTags),
		j.TargetImageRef, j.TargetRepoDigest, j.TargetVersion, string(tgtTags),
		eligibleInt, j.RollbackRef, j.RollbackReason, j.Logs, j.StartedAt,
	)
	return err
}

func (s *Service) updateJobProgressAndLogs(ctx context.Context, id string, p platform.PullProgress, logs string) {
	b, _ := json.Marshal(p)
	_, _ = s.st.DB.ExecContext(ctx, `UPDATE update_job SET phase=?, progress_json=?, logs_text=? WHERE id=?`, p.Phase, string(b), logs, id)
}

func newJobID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "up_" + hex.EncodeToString(b)
}

func hasCustomMirrorPrefix(ref string) bool {
	first, _, ok := strings.Cut(strings.TrimSpace(ref), "/")
	if !ok {
		return false
	}
	switch strings.ToLower(first) {
	case "docker.1ms.run", "docker.3ms.run", "docker.m.daocloud.io", "dockerproxy.com",
		"docker.nju.edu.cn", "hub-mirror.c.163.com", "mirror.baidubce.com", "docker.fnnas.com",
		"ghcr.nju.edu.cn", "ghcr.m.daocloud.io", "ghcr-proxy.com":
		return true
	default:
		return false
	}
}

func splitRefAndTag(ref string) (repo, tag string) {
	s := strings.TrimSpace(ref)
	if at := strings.Index(s, "@"); at >= 0 {
		s = s[:at]
	}
	lastSlash := strings.LastIndex(s, "/")
	colon := strings.LastIndex(s, ":")
	if colon > lastSlash {
		return s[:colon], s[colon+1:]
	}
	return s, ""
}

func replaceRefTag(origRef, newTag string) string {
	base, _ := splitRefAndTag(origRef)
	return base + ":" + newTag
}

// normalizeUpdateError 识别旧版 Docker Engine (< 20.10.10) 拉取 zstd 压缩层触发的 invalid tar header 错误，转为直观可操作提示。
func normalizeUpdateError(code, msg string, h *host.Host) (string, string) {
	if code != "pull_failed" && code != "recreate_failed" {
		return code, msg
	}
	raw := i18n.LocalizeDetail(i18n.ZhCN, msg)
	lower := strings.ToLower(raw)
	isZstdUnpackErr := (strings.Contains(raw, "ApplyLayer") && strings.Contains(raw, "archive/tar: invalid tar header")) ||
		(strings.Contains(lower, "zstd") && (strings.Contains(lower, "unhandled") || strings.Contains(lower, "unsupported")))
	if !isZstdUnpackErr {
		return code, msg
	}
	if h != nil && h.Info != nil && isDockerBeforeZstd(h.Info.DockerVersion) {
		return "pull_failed", i18n.EngineDetail(i18n.MsgDockerPullOldZstdVer, strings.TrimSpace(h.Info.DockerVersion))
	}
	if strings.Contains(raw, "ApplyLayer") && strings.Contains(raw, "archive/tar: invalid tar header") {
		return "pull_failed", i18n.EngineDetail(i18n.MsgDockerPullOldZstd)
	}
	return code, msg
}

// isDockerBeforeZstd 判断 Docker Engine 版本号是否早于原生支持 zstd OCI 层解压的 20.10.10。
func isDockerBeforeZstd(ver string) bool {
	s := strings.TrimPrefix(strings.TrimSpace(ver), "v")
	if idx := strings.IndexAny(s, "-+ "); idx >= 0 {
		s = s[:idx]
	}
	parts := strings.Split(s, ".")
	if len(parts) < 2 {
		return false
	}
	maj, err1 := strconv.Atoi(parts[0])
	min, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	patch := 0
	if len(parts) >= 3 {
		if p, err := strconv.Atoi(parts[2]); err == nil {
			patch = p
		}
	}
	if maj < 20 {
		return true
	}
	if maj > 20 {
		return false
	}
	if min < 10 {
		return true
	}
	if min > 10 {
		return false
	}
	return patch < 10
}
