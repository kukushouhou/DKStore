// Package checker 编排主机应用发现、镜像更新检测、SSE 进度广播与增量通知推送（实施文档 §5 / §8）。
package checker

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/kukushouhou/DKStore/internal/classify"
	"github.com/kukushouhou/DKStore/internal/host"
	"github.com/kukushouhou/DKStore/internal/i18n"
	"github.com/kukushouhou/DKStore/internal/ignore"
	"github.com/kukushouhou/DKStore/internal/inventory"
	"github.com/kukushouhou/DKStore/internal/notify"
	"github.com/kukushouhou/DKStore/internal/registry"
	"github.com/kukushouhou/DKStore/internal/store"
	"github.com/kukushouhou/DKStore/internal/version"
	"github.com/kukushouhou/DKStore/rules"
)

// Progress 表示当前检查任务的实时状态。
type Progress struct {
	Running   bool   `json:"running"`
	RunID     int64  `json:"runId,omitempty"`
	Phase     string `json:"phase,omitempty"` // idle | discovering | checking | done
	HostName  string `json:"hostName,omitempty"`
	Completed int    `json:"completed"`
	Total     int    `json:"total"`
	StartedAt int64  `json:"startedAt,omitempty"`
}

// Service 负责执行检查任务与查询检测结果。
type Service struct {
	log    *slog.Logger
	st     *store.Store
	hosts  *host.Registry
	inv    *inventory.Service
	reg    *registry.Client
	ign    *ignore.Service
	ntf    *notify.Service

	mu          sync.Mutex
	progress    Progress
	subscribers map[chan Progress]struct{}
}

// New 创建检查服务。
func New(
	log *slog.Logger,
	st *store.Store,
	hosts *host.Registry,
	inv *inventory.Service,
	reg *registry.Client,
	ign *ignore.Service,
	ntf *notify.Service,
) *Service {
	return &Service{
		log:         log,
		st:          st,
		hosts:       hosts,
		inv:         inv,
		reg:         reg,
		ign:         ign,
		ntf:         ntf,
		progress:    Progress{Phase: "idle"},
		subscribers: make(map[chan Progress]struct{}),
	}
}

// CurrentProgress 返回当前任务进度快照。
func (s *Service) CurrentProgress() Progress {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.progress
}

// Subscribe 订阅进度变化事件（用于 SSE）。
func (s *Service) Subscribe() (ch chan Progress, unsubscribe func()) {
	ch = make(chan Progress, 16)
	s.mu.Lock()
	s.subscribers[ch] = struct{}{}
	ch <- s.progress
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		delete(s.subscribers, ch)
		close(ch)
		s.mu.Unlock()
	}
}

func (s *Service) broadcast(p Progress) {
	s.mu.Lock()
	s.progress = p
	for ch := range s.subscribers {
		select {
		case ch <- p:
		default:
		}
	}
	s.mu.Unlock()
}

// Trigger 异步启动一次检查任务（若已有任务在跑则直接返回当前进度）。
func (s *Service) Trigger(trigger, hostID string, bypassCache bool) Progress {
	s.mu.Lock()
	if s.progress.Running {
		p := s.progress
		s.mu.Unlock()
		return p
	}
	now := time.Now().Unix()
	s.progress = Progress{
		Running:   true,
		Phase:     "discovering",
		StartedAt: now,
	}
	p := s.progress
	s.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		s.runCheck(ctx, trigger, hostID, bypassCache)
	}()
	return p
}

// RunSync 同步执行一次检查任务（用于测试或命令行验证）。
func (s *Service) RunSync(ctx context.Context, trigger, hostID string, bypassCache bool) error {
	s.mu.Lock()
	if s.progress.Running {
		s.mu.Unlock()
		return nil
	}
	s.progress = Progress{Running: true, Phase: "discovering", StartedAt: time.Now().Unix()}
	s.mu.Unlock()
	s.runCheck(ctx, trigger, hostID, bypassCache)
	return nil
}

type svcRow struct {
	Key        string
	AppKey     string
	AppName    string
	HostID     string
	HostName   string
	Name       string
	ImageRef   string
	ImageID    string
	RepoDigest string
	Platform   string
}

func (s *Service) runCheck(ctx context.Context, trigger, hostIDFilter string, bypassCache bool) {
	started := time.Now().Unix()
	res, _ := s.st.DB.ExecContext(ctx, `INSERT INTO check_run(trigger, started_at, status) VALUES (?, ?, 'running')`, trigger, started)
	var runID int64
	if res != nil {
		runID, _ = res.LastInsertId()
	}

	hosts, _ := s.hosts.List(ctx)
	for _, h := range hosts {
		if hostIDFilter != "" && h.ID != hostIDFilter {
			continue
		}
		s.broadcast(Progress{
			Running:   true,
			RunID:     runID,
			Phase:     "discovering",
			HostName:  h.Name,
			StartedAt: started,
		})
		if _, _, err := s.inv.SyncHost(ctx, h.ID); err != nil {
			s.log.Warn("同步主机应用失败", "host", h.Name, "err", err)
		}
	}

	// 读取待检查的活跃服务
	query := `
		SELECT s.key, s.app_key, a.name, a.host_id, h.name, s.name,
		       COALESCE(s.image_ref,''), COALESCE(s.image_id,''), COALESCE(s.repo_digest,''), COALESCE(s.platform,'linux/amd64')
		FROM service s
		JOIN app a ON s.app_key = a.key
		JOIN host h ON a.host_id = h.id
		WHERE s.gone_at IS NULL AND a.gone_at IS NULL`
	var args []any
	if hostIDFilter != "" {
		query += ` AND a.host_id = ?`
		args = append(args, hostIDFilter)
	}
	rows, err := s.st.DB.QueryContext(ctx, query, args...)
	var services []svcRow
	if err == nil {
		for rows.Next() {
			var r svcRow
			if rows.Scan(&r.Key, &r.AppKey, &r.AppName, &r.HostID, &r.HostName, &r.Name, &r.ImageRef, &r.ImageID, &r.RepoDigest, &r.Platform) == nil {
				services = append(services, r)
			}
		}
		rows.Close()
	}

	total := len(services)
	s.broadcast(Progress{Running: true, RunID: runID, Phase: "checking", Completed: 0, Total: total, StartedAt: started})

	ignoreRules, _ := s.ign.ListAll(ctx)
	var (
		newNotifyItems []i18n.NotifyItem
		normalCount    int
		cautionCount   int
		failedCount    int
	)

	for idx, svc := range services {
		out, errKind, errMsg, curTags := s.checkSingleService(ctx, svc, bypassCache)
		isNew := s.saveFinding(ctx, runID, svc, out, errKind, errMsg, curTags)

		switch out.Kind {
		case classify.KindDigestUpdate, classify.KindVersionUpdate:
			if out.Category == classify.CategoryCaution {
				cautionCount++
			} else {
				normalCount++
			}
			rule := ignoreRules[svc.Key]
			if isNew && !ignore.Matches(rule, string(out.Kind), out.TargetDigest, out.TargetVersion) {
				newNotifyItems = append(newNotifyItems, i18n.NotifyItem{
					HostName:       svc.HostName,
					AppName:        svc.AppName,
					ServiceName:    svc.Name,
					Category:       string(out.Category),
					CurrentVersion: out.CurrentVersion,
					TargetVersion:  out.TargetVersion,
					Reason:         out.Reason,
					DatabaseName:   out.DatabaseName,
				})
			}
		case classify.KindCheckFailed:
			failedCount++
		}

		s.broadcast(Progress{
			Running:   true,
			RunID:     runID,
			Phase:     "checking",
			Completed: idx + 1,
			Total:     total,
			StartedAt: started,
		})
	}

	if len(newNotifyItems) > 0 {
		if err := s.ntf.SendUpdates(ctx, newNotifyItems); err != nil {
			s.log.Warn("发送更新推送失败", "err", err)
		} else {
			now := time.Now().Unix()
			for _, svc := range services {
				_, _ = s.st.DB.ExecContext(ctx, `UPDATE finding SET notified_at=? WHERE service_key=? AND notified_at IS NULL`, now, svc.Key)
			}
		}
	}

	finished := time.Now().Unix()
	summaryBytes, _ := json.Marshal(map[string]int{
		"total":   total,
		"normal":  normalCount,
		"caution": cautionCount,
		"failed":  failedCount,
	})
	_, _ = s.st.DB.ExecContext(ctx, `UPDATE check_run SET finished_at=?, status='done', summary_json=? WHERE id=?`,
		finished, string(summaryBytes), runID)

	s.broadcast(Progress{
		Running:   false,
		RunID:     runID,
		Phase:     "done",
		Completed: total,
		Total:     total,
		StartedAt: started,
	})
}

func (s *Service) checkSingleService(ctx context.Context, svc svcRow, bypassCache bool) (out classify.Output, errKind, errMsg string, curMatched []string) {
	ref, err := version.ParseImageRef(svc.ImageRef)
	if err != nil || strings.HasPrefix(svc.ImageRef, "sha256:") {
		return classify.Output{Kind: classify.KindUncomparable}, "", "", nil
	}

	mf, err := s.reg.CheckManifest(ctx, ref, svc.Platform, bypassCache)
	if err != nil {
		var re *registry.RegistryError
		if errors.As(err, &re) {
			errKind = string(re.Kind)
			errMsg = re.Message
		} else {
			errKind = string(registry.ErrRegistry)
			errMsg = err.Error()
		}
		return classify.Output{
			Kind:  classify.KindCheckFailed,
			Links: rules.Default().ResolveLinks(ref.Registry, ref.Repository, ""),
		}, errKind, errMsg, nil
	}

	// 获取有限窗口候选标签（用于版本发现与 Digest 反查）
	recentTags, _ := s.reg.FetchRecentTags(ctx, ref, 3, bypassCache)
	if svc.RepoDigest != "" {
		curMatched = s.reg.LookupTagsByDigest(ctx, ref, svc.RepoDigest, "", svc.Platform, recentTags)
	}
	var remMatched []string
	if mf != nil {
		remMatched = s.reg.LookupTagsByDigest(ctx, ref, mf.RepoDigest, mf.PlatformDigest, svc.Platform, recentTags)
	}

	out = classify.Evaluate(classify.Input{
		Ref:                ref,
		LocalRepoDigest:    svc.RepoDigest,
		LocalImageID:       svc.ImageID,
		RemoteManifest:     mf,
		CurrentMatchedTags: curMatched,
		RemoteMatchedTags:  remMatched,
		CandidateTags:      recentTags,
		HostPlatform:       svc.Platform,
	})
	return out, "", "", curMatched
}
