// Package updater 编排单服务一键更新、分层下载实时进度广播、冲突检测、更新历史记录与有条件回退（立项文档 §7 / §8 / §9）。
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
	"strings"
	"sync"
	"time"

	"github.com/kukushouhou/DKStore/internal/host"
	"github.com/kukushouhou/DKStore/internal/i18n"
	"github.com/kukushouhou/DKStore/internal/inventory"
	"github.com/kukushouhou/DKStore/internal/platform"
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
	StartedAt          int64                 `json:"startedAt"`
	FinishedAt         int64                 `json:"finishedAt,omitempty"`
}

// finalizeTimeout 是更新任务收尾写库使用的独立超时（审计 M6）。
const finalizeTimeout = 5 * time.Second

// Service 管理更新任务与历史回退。
type Service struct {
	log   *slog.Logger
	st    *store.Store
	hosts *host.Registry
	inv   *inventory.Service
	reg   *registry.Client

	mu          sync.Mutex
	activeJobs  map[string]Job // key: serviceKey -> running Job
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
	ch := make(chan Job, 16)
	s.mu.Lock()
	s.subscribers[ch] = struct{}{}
	for _, j := range s.activeJobs {
		select {
		case ch <- j:
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
		s.activeJobs[j.ServiceKey] = j
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

// StartUpdates 为用户指定的 1 个或多个服务启动升级任务（严禁隐式附带未选中的数据库或依赖服务，立项文档 §3.3 / §7.2）。
func (s *Service) StartUpdates(ctx context.Context, serviceKeys []string, lang i18n.Lang) ([]Job, error) {
	if len(serviceKeys) == 0 {
		return nil, i18n.NewEngineError(i18n.MsgUpdateNoService)
	}
	var jobs []Job
	for _, sk := range serviceKeys {
		sk = strings.TrimSpace(sk)
		if sk == "" {
			continue
		}
		j, alreadyRunning, err := s.prepareUpgradeJob(ctx, sk, lang)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
		if !alreadyRunning {
			go s.executeJob(j)
		}
	}
	return jobs, nil
}

func (s *Service) prepareUpgradeJob(ctx context.Context, serviceKey string, lang i18n.Lang) (Job, bool, error) {
	// 审计 M4：在同一次 s.mu.Lock() 内完成 activeJobs 检查与占位写入，消除并发请求的 TOCTOU 竞态
	s.mu.Lock()
	if active, ok := s.activeJobs[serviceKey]; ok && (active.Status == "running" || active.Status == "queued") {
		s.mu.Unlock()
		return active, true, nil
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
		appKey, appName, hostID, hostName, svcName, source string
		oldImageRef, oldImageID, oldRepoDigest, curTagsRaw string
		targetRef, targetDigest, targetVer, targetTagsRaw  string
		curVer                                             string
	)
	err := s.st.DB.QueryRowContext(ctx, `
		SELECT s.app_key, a.name, a.host_id, h.name, s.name, a.source,
		       COALESCE(s.image_ref,''), COALESCE(s.image_id,''), COALESCE(s.repo_digest,''), COALESCE(s.current_tags_json,''),
		       COALESCE(f.current_version,''), COALESCE(f.target_ref,''), COALESCE(f.target_digest,''), COALESCE(f.target_version,''), COALESCE(f.target_tags_json,'')
		FROM service s
		JOIN app a ON s.app_key = a.key
		JOIN host h ON a.host_id = h.id
		LEFT JOIN finding f ON f.service_key = s.key
		WHERE s.key = ? AND s.gone_at IS NULL`, serviceKey).Scan(
		&appKey, &appName, &hostID, &hostName, &svcName, &source,
		&oldImageRef, &oldImageID, &oldRepoDigest, &curTagsRaw,
		&curVer, &targetRef, &targetDigest, &targetVer, &targetTagsRaw,
	)
	if errors.Is(err, sql.ErrNoRows) {
		cleanupPlaceholder()
		return Job{}, false, i18n.NewEngineError(i18n.MsgUpdateServiceNotFound, serviceKey)
	}
	if err != nil {
		cleanupPlaceholder()
		return Job{}, false, err
	}
	if source == string(platform.SourceVendorManaged) {
		cleanupPlaceholder()
		return Job{}, false, errors.New(i18n.FormatUpdateError(lang, "vendor_managed_readonly", ""))
	}

	if targetRef == "" {
		targetRef = oldImageRef
	} else if hasCustomMirrorPrefix(oldImageRef) {
		// 若用户原声明镜像使用了加速前缀（如 docker.1ms.run/postgres:17），升级新标签时保留其原有前缀习惯
		if _, newTag := splitRefAndTag(targetRef); newTag != "" {
			targetRef = replaceRefTag(oldImageRef, newTag)
		}
	}

	var oldMatched, targetMatched []string
	_ = json.Unmarshal([]byte(curTagsRaw), &oldMatched)
	_ = json.Unmarshal([]byte(targetTagsRaw), &targetMatched)

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
	if curVer != "" && version.ParseTag(curVer).Scheme != version.SchemeUnknown {
		rollbackEligible = true
		rollbackRef = replaceRefTag(oldImageRef, curVer)
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
		Progress:           platform.PullProgress{Phase: "precheck", StatusText: "Preparing update"},
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
	if err := s.insertJob(ctx, job); err != nil {
		cleanupPlaceholder()
		return Job{}, false, err
	}
	s.broadcast(job)
	return job, false, nil
}

func (s *Service) executeJob(j Job) {
	defer func() {
		if r := recover(); r != nil {
			if s.log != nil {
				s.log.Error("更新任务发生 panic", "job", j.ID, "recover", r)
			}
			s.finishJobFailed(j, "recreate_failed", i18n.EngineDetail(i18n.MsgUpdatePanic, fmt.Sprint(r)), "")
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

	ad, _, err := s.hosts.OpenAdapter(ctx, j.HostID)
	if err != nil {
		s.finishJobFailed(j, "connect_failed", i18n.DetailOf(err), "")
		return
	}
	defer ad.Close()

	upAd, ok := ad.(platform.UpdateCapableAdapter)
	if !ok {
		s.finishJobFailed(j, "capability_unsupported", i18n.EngineDetail(i18n.MsgUpdateAdapterNoUpdate), "")
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
		j.Phase = p.Phase
		j.Progress = p
		s.updateJobProgress(ctx, j.ID, p)
		s.broadcast(j)
	})

	if err != nil {
		code := "recreate_failed"
		msg := i18n.DetailOf(err)
		var ue *platform.UpdateError
		if errors.As(err, &ue) {
			code = ue.Code
			msg = ue.Message
		}
		s.finishJobFailed(j, code, msg, "")
		return
	}

	finished := time.Now().Unix()
	j.Status = "succeeded"
	j.Phase = "done"
	j.Progress = platform.PullProgress{Phase: "done", StatusText: "Completed", Percent: 100}
	j.NewImageID = res.NewImageID
	if res.NewRepoDigest != "" {
		j.NewRepoDigest = res.NewRepoDigest
	} else {
		j.NewRepoDigest = j.TargetRepoDigest
	}
	j.Logs = strings.Join(res.Logs, "\n")
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
		j.TargetVersion, finished, j.ServiceKey,
	)
	s.broadcast(j)
}

func (s *Service) finishJobFailed(j Job, code, msg, logs string) {
	finished := time.Now().Unix()
	j.Status = "failed"
	j.Phase = "done"
	j.ErrorCode = code
	j.ErrorMsg = i18n.FormatUpdateError(i18n.ZhCN, code, msg)
	j.Logs = logs
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
	progBytes, _ := json.Marshal(j.Progress)
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
			rollback_eligible, rollback_ref, rollback_reason, started_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.ID, j.Kind, j.HostID, j.HostName, j.AppKey, j.AppName, j.ServiceKey, j.ServiceName, j.Source,
		j.Status, j.Phase, string(progBytes),
		j.OldImageRef, j.OldImageID, j.OldRepoDigest, j.OldVersion, string(oldTags),
		j.TargetImageRef, j.TargetRepoDigest, j.TargetVersion, string(tgtTags),
		eligibleInt, j.RollbackRef, j.RollbackReason, j.StartedAt,
	)
	return err
}

func (s *Service) updateJobProgress(ctx context.Context, id string, p platform.PullProgress) {
	b, _ := json.Marshal(p)
	_, _ = s.st.DB.ExecContext(ctx, `UPDATE update_job SET phase=?, progress_json=? WHERE id=?`, p.Phase, string(b), id)
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
		"docker.nju.edu.cn", "hub-mirror.c.163.com", "mirror.baidubce.com",
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
