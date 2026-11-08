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
}

// New 创建 Updater 服务。
func New(
	log *slog.Logger,
	st *store.Store,
	hosts *host.Registry,
	inv *inventory.Service,
	reg *registry.Client,
) *Service {
	return &Service{
		log:         log,
		st:          st,
		hosts:       hosts,
		inv:         inv,
		reg:         reg,
		activeJobs:  make(map[string]Job),
		subscribers: make(map[chan Job]struct{}),
	}
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
		return nil, errors.New("未选择需要更新的服务")
	}
	var jobs []Job
	for _, sk := range serviceKeys {
		sk = strings.TrimSpace(sk)
		if sk == "" {
			continue
		}
		j, err := s.prepareUpgradeJob(ctx, sk, lang)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
		go s.executeJob(j)
	}
	return jobs, nil
}

func (s *Service) prepareUpgradeJob(ctx context.Context, serviceKey string, lang i18n.Lang) (Job, error) {
	s.mu.Lock()
	if active, ok := s.activeJobs[serviceKey]; ok && (active.Status == "running" || active.Status == "queued") {
		s.mu.Unlock()
		return active, nil
	}
	s.mu.Unlock()

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
		return Job{}, fmt.Errorf("未找到服务: %s", serviceKey)
	}
	if err != nil {
		return Job{}, err
	}
	if source == string(platform.SourceVendorManaged) {
		return Job{}, errors.New(i18n.FormatUpdateError(lang, "vendor_managed_readonly", ""))
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
		return Job{}, err
	}
	s.broadcast(job)
	return job, nil
}

func (s *Service) executeJob(j Job) {
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
		s.finishJobFailed(ctx, j, "connect_failed", err.Error(), "")
		return
	}
	defer ad.Close()

	upAd, ok := ad.(platform.UpdateCapableAdapter)
	if !ok {
		s.finishJobFailed(ctx, j, "capability_unsupported", "platform adapter does not support UpdateService", "")
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
		msg := err.Error()
		var ue *platform.UpdateError
		if errors.As(err, &ue) {
			code = ue.Code
			msg = ue.Message
		}
		s.finishJobFailed(ctx, j, code, msg, "")
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

	progBytes, _ := json.Marshal(j.Progress)
	_, _ = s.st.DB.ExecContext(ctx, `
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
	_, _ = s.st.DB.ExecContext(ctx, `
		UPDATE service
		SET image_ref=?, image_id=?, repo_digest=CASE WHEN ?<>'' THEN ? ELSE repo_digest END,
		    current_tags_json=?, updated_at=?
		WHERE key=?`,
		j.TargetImageRef, j.NewImageID, j.NewRepoDigest, j.NewRepoDigest, string(targetTagsJSON), finished, j.ServiceKey,
	)
	_, _ = s.st.DB.ExecContext(ctx, `
		UPDATE finding
		SET kind='none', current_version=?, target_version='', target_ref='', target_digest='', checked_at=?
		WHERE service_key=?`,
		j.TargetVersion, finished, j.ServiceKey,
	)
	s.broadcast(j)
}

func (s *Service) finishJobFailed(ctx context.Context, j Job, code, msg, logs string) {
	finished := time.Now().Unix()
	j.Status = "failed"
	j.Phase = "done"
	j.ErrorCode = code
	j.ErrorMsg = msg
	j.Logs = logs
	j.FinishedAt = finished
	progBytes, _ := json.Marshal(j.Progress)
	_, _ = s.st.DB.ExecContext(ctx, `
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
