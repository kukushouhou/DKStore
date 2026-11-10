// Package checker 编排主机应用发现、镜像更新检测、SSE 逐项进度广播与增量通知推送（实施文档 §5 / §8 / 附录 D.2）。
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

// Progress 表示当前检查任务的实时状态（含逐个服务流式检查进度，附录 D.2）。
type Progress struct {
	Running              bool     `json:"running"`
	RunID                int64    `json:"runId,omitempty"`
	Phase                string   `json:"phase,omitempty"` // idle | discovering | checking | done
	HostName             string   `json:"hostName,omitempty"`
	CurrentServiceKey    string   `json:"currentServiceKey,omitempty"`
	CurrentAppName       string   `json:"currentAppName,omitempty"`
	CompletedServiceKeys []string `json:"completedServiceKeys,omitempty"`
	Completed            int      `json:"completed"`
	Total                int      `json:"total"`
	StartedAt            int64    `json:"startedAt,omitempty"`
}

// Service 负责执行检查任务与查询检测结果。
type Service struct {
	log   *slog.Logger
	st    *store.Store
	hosts *host.Registry
	inv   *inventory.Service
	reg   *registry.Client
	ign   *ignore.Service
	ntf   *notify.Service

	mu          sync.Mutex
	progress    Progress
	subscribers map[chan Progress]struct{}
	pending     *pendingRun // 运行期间收到的 Trigger，当前任务结束后补跑一次（审计 L9）

	// runFn 仅供单元测试替换实际检查逻辑。
	runFn func(ctx context.Context, trigger, hostID string, bypassCache bool)
}

type pendingRun struct {
	trigger     string
	hostID      string
	bypassCache bool
}

// finalizeTimeout 是检查收尾写库使用的独立超时（审计 M6）。
const finalizeTimeout = 5 * time.Second

// New 创建检查服务，并把上次进程异常退出时残留的 running 检查记录标记为 failed（审计 M6）。
func New(
	log *slog.Logger,
	st *store.Store,
	hosts *host.Registry,
	inv *inventory.Service,
	reg *registry.Client,
	ign *ignore.Service,
	ntf *notify.Service,
) *Service {
	s := &Service{
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
	s.runFn = s.runCheck
	s.recoverInterruptedRuns()
	return s
}

// recoverInterruptedRuns 把库中残留的 running 状态 check_run 标记为 failed。
func (s *Service) recoverInterruptedRuns() {
	if s.st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), finalizeTimeout)
	defer cancel()
	res, err := s.st.DB.ExecContext(ctx, `
		UPDATE check_run SET status='failed', finished_at=COALESCE(finished_at, ?)
		WHERE status='running'`, time.Now().Unix())
	if err != nil {
		if s.log != nil {
			s.log.Warn("标记残留检查记录失败", "err", err)
		}
		return
	}
	if n, _ := res.RowsAffected(); n > 0 && s.log != nil {
		s.log.Info("已将残留的进行中检查记录标记为失败", "count", n)
	}
}

// CurrentProgress 返回当前任务进度快照。
func (s *Service) CurrentProgress() Progress {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := s.progress
	if len(s.progress.CompletedServiceKeys) > 0 {
		cp.CompletedServiceKeys = append([]string(nil), s.progress.CompletedServiceKeys...)
	}
	return cp
}

// Subscribe 订阅进度变化事件（用于 SSE）。
func (s *Service) Subscribe() (ch chan Progress, unsubscribe func()) {
	ch = make(chan Progress, 32)
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
	if len(p.CompletedServiceKeys) > 0 {
		p.CompletedServiceKeys = append([]string(nil), p.CompletedServiceKeys...)
	}
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

// Trigger 异步启动一次检查任务（若已有任务在跑则挂起合并，当前任务结束后补跑一次，审计 L9）。
func (s *Service) Trigger(trigger, hostID string, bypassCache bool) Progress {
	s.mu.Lock()
	if s.progress.Running {
		if s.pending == nil {
			s.pending = &pendingRun{
				trigger:     trigger,
				hostID:      hostID,
				bypassCache: bypassCache,
			}
		} else {
			if s.pending.hostID == "" || hostID == "" || s.pending.hostID != hostID {
				s.pending.hostID = ""
			}
			s.pending.bypassCache = s.pending.bypassCache || bypassCache
			if trigger != "" {
				s.pending.trigger = trigger
			}
		}
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

	s.launchRun(trigger, hostID, bypassCache)
	return p
}

func (s *Service) launchRun(trigger, hostID string, bypassCache bool) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		fn := s.runFn
		if fn == nil {
			fn = s.runCheck
		}
		fn(ctx, trigger, hostID, bypassCache)
	}()
}

type svcRow struct {
	Key           string
	AppKey        string
	AppName       string
	AppSource     string
	HostID        string
	HostName      string
	HostTransport string
	Name          string
	ImageRef      string
	ImageID       string
	RepoDigest    string
	Platform      string
	LabelsJSON    string
}

func (s *Service) runCheck(ctx context.Context, trigger, hostIDFilter string, bypassCache bool) {
	started := time.Now().Unix()
	var (
		runID     int64
		total     int
		doneKeys  []string
		completed bool
	)
	if s.st != nil {
		res, err := s.st.DB.ExecContext(ctx, `INSERT INTO check_run(trigger, started_at, status) VALUES (?, ?, 'running')`, trigger, started)
		if err != nil {
			if s.log != nil {
				s.log.Warn("创建 check_run 记录失败", "err", err)
			}
		} else if res != nil {
			runID, _ = res.LastInsertId()
		}
	}

	defer func() {
		if r := recover(); r != nil {
			if s.log != nil {
				s.log.Error("检查任务发生 panic", "recover", r)
			}
		}
		if !completed {
			if s.st != nil && runID > 0 {
				fCtx, cancel := context.WithTimeout(context.Background(), finalizeTimeout)
				_, _ = s.st.DB.ExecContext(fCtx, `UPDATE check_run SET finished_at=?, status='failed' WHERE id=?`, time.Now().Unix(), runID)
				cancel()
			}
			s.broadcast(Progress{
				Running:              false,
				RunID:                runID,
				Phase:                "done",
				CompletedServiceKeys: doneKeys,
				Completed:            total,
				Total:                total,
				StartedAt:            started,
			})
		}
		s.finishOrRunPending()
	}()

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
			if s.log != nil {
				s.log.Warn("同步主机应用失败", "host", h.Name, "err", err)
			}
		}
	}

	// 读取待检查的活跃服务
	query := `
		SELECT s.key, s.app_key, a.name, COALESCE(a.source,'standalone'), a.host_id, h.name, COALESCE(h.transport,'local'), s.name,
		       COALESCE(s.image_ref,''), COALESCE(s.image_id,''), COALESCE(s.repo_digest,''), COALESCE(s.platform,''), COALESCE(s.labels_json,'')
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
	if err != nil {
		if s.log != nil {
			s.log.Warn("查询活跃服务列表失败", "err", err)
		}
	} else {
		for rows.Next() {
			var r svcRow
			if rows.Scan(&r.Key, &r.AppKey, &r.AppName, &r.AppSource, &r.HostID, &r.HostName, &r.HostTransport, &r.Name, &r.ImageRef, &r.ImageID, &r.RepoDigest, &r.Platform, &r.LabelsJSON) == nil {
				services = append(services, r)
			}
		}
		if err := rows.Err(); err != nil && s.log != nil {
			s.log.Warn("遍历活跃服务列表出错", "err", err)
		}
		rows.Close()
	}

	total = len(services)
	doneKeys = make([]string, 0, total)
	s.broadcast(Progress{
		Running:              true,
		RunID:                runID,
		Phase:                "checking",
		CompletedServiceKeys: doneKeys,
		Completed:            0,
		Total:                total,
		StartedAt:            started,
	})

	ignoreRules, _ := s.ign.ListAll(ctx)
	var (
		newNotifyItems []i18n.NotifyItem
		normalCount    int
		cautionCount   int
		failedCount    int
	)

	for idx, svc := range services {
		s.broadcast(Progress{
			Running:              true,
			RunID:                runID,
			Phase:                "checking",
			HostName:             svc.HostName,
			CurrentServiceKey:    svc.Key,
			CurrentAppName:       svc.AppName,
			CompletedServiceKeys: doneKeys,
			Completed:            idx,
			Total:                total,
			StartedAt:            started,
		})

		out, errKind, errMsg, curTags := s.checkSingleService(ctx, svc, bypassCache)
		isNew := s.saveFinding(ctx, runID, svc, out, errKind, errMsg, curTags)
		doneKeys = append(doneKeys, svc.Key)

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
			Running:              true,
			RunID:                runID,
			Phase:                "checking",
			HostName:             svc.HostName,
			CurrentServiceKey:    svc.Key,
			CurrentAppName:       svc.AppName,
			CompletedServiceKeys: doneKeys,
			Completed:            idx + 1,
			Total:                total,
			StartedAt:            started,
		})
	}

	if len(newNotifyItems) > 0 {
		if err := s.ntf.SendUpdates(ctx, newNotifyItems); err != nil {
			if !errors.Is(err, notify.ErrQuietHours) && s.log != nil {
				s.log.Warn("发送更新推送失败", "err", err)
			}
		} else {
			now := time.Now().Unix()
			fCtx, cancel := context.WithTimeout(context.Background(), finalizeTimeout)
			for _, svc := range services {
				_, _ = s.st.DB.ExecContext(fCtx, `UPDATE finding SET notified_at=? WHERE service_key=? AND notified_at IS NULL`, now, svc.Key)
			}
			cancel()
		}
	}

	finished := time.Now().Unix()
	summaryBytes, _ := json.Marshal(map[string]int{
		"total":   total,
		"normal":  normalCount,
		"caution": cautionCount,
		"failed":  failedCount,
	})
	fCtx, cancel := context.WithTimeout(context.Background(), finalizeTimeout)
	_, _ = s.st.DB.ExecContext(fCtx, `UPDATE check_run SET finished_at=?, status='done', summary_json=? WHERE id=?`,
		finished, string(summaryBytes), runID)
	cancel()

	completed = true
	s.broadcast(Progress{
		Running:              false,
		RunID:                runID,
		Phase:                "done",
		CompletedServiceKeys: doneKeys,
		Completed:            total,
		Total:                total,
		StartedAt:            started,
	})
}

func (s *Service) finishOrRunPending() {
	s.mu.Lock()
	next := s.pending
	s.pending = nil
	if next == nil {
		s.progress.Running = false
		s.mu.Unlock()
		return
	}
	s.progress = Progress{
		Running:   true,
		Phase:     "discovering",
		StartedAt: time.Now().Unix(),
	}
	s.mu.Unlock()
	s.launchRun(next.trigger, next.hostID, next.bypassCache)
}

func (s *Service) checkSingleService(ctx context.Context, svc svcRow, bypassCache bool) (out classify.Output, errKind, errMsg string, curMatched []string) {
	var labels map[string]string
	if svc.LabelsJSON != "" {
		_ = json.Unmarshal([]byte(svc.LabelsJSON), &labels)
	}
	localVerLabel := classify.ExtractVersionLabel(labels)

	if svc.AppSource == "vendor_managed" {
		curVer := localVerLabel
		if curVer == "" {
			if pr, err := version.ParseImageRef(svc.ImageRef); err == nil && pr.Tag != "" {
				curVer = pr.Tag
			}
		}
		return classify.Output{Kind: classify.KindUncomparable, CurrentVersion: curVer}, "", "", nil
	}
	ref, err := version.ParseImageRef(svc.ImageRef)
	if err != nil || strings.HasPrefix(svc.ImageRef, "sha256:") || (svc.RepoDigest == "" && svc.ImageID == "" && !ref.IsPinnedDigest()) {
		curVer := localVerLabel
		if curVer == "" && err == nil && ref.Tag != "" {
			curVer = ref.Tag
		}
		return classify.Output{Kind: classify.KindUncomparable, CurrentVersion: curVer}, "", "", nil
	}

	isDSMAPI := svc.HostTransport == "dsm_api"
	mf, err := s.reg.CheckManifestEx(ctx, ref, registry.ManifestCheckOptions{
		HostPlatform:    svc.Platform,
		BypassCache:     bypassCache,
		LocalRepoDigest: svc.RepoDigest,
		NeedConfig:      isDSMAPI && svc.RepoDigest == "",
	})
	if err != nil {
		var re *registry.RegistryError
		if errors.As(err, &re) {
			// 仅当远端明确返回 404 且本地无 RepoDigest（如本地构建/导入镜像）时，才归为 uncomparable（审计 M3）
			if re.Kind == registry.ErrNotFound && svc.RepoDigest == "" && !ref.IsPinnedDigest() {
				curVer := localVerLabel
				if curVer == "" && ref.Tag != "" {
					curVer = ref.Tag
				}
				return classify.Output{Kind: classify.KindUncomparable, CurrentVersion: curVer}, "", "", nil
			}
			errKind = string(re.Kind)
			errMsg = re.Message
		} else {
			errKind = string(registry.ErrRegistry)
			errMsg = err.Error()
		}
		curVer := localVerLabel
		if curVer == "" && ref.Tag != "" {
			curVer = ref.Tag
		}
		return classify.Output{
			Kind:           classify.KindCheckFailed,
			CurrentVersion: curVer,
			Links:          rules.Default().ResolveLinks(ref.Registry, ref.Repository, ""),
		}, errKind, errMsg, nil
	}

	// 获取有限窗口候选标签（用于版本发现与 Digest/ConfigDigest 反查）
	recentTags, _ := s.reg.FetchRecentTags(ctx, ref, 3, bypassCache)
	localCfgForLookup := ""
	if svc.RepoDigest == "" && isDSMAPI {
		localCfgForLookup = svc.ImageID
	}
	if svc.RepoDigest != "" || localCfgForLookup != "" {
		curMatched = s.reg.LookupTagsByDigestOrConfig(ctx, ref, svc.RepoDigest, "", localCfgForLookup, svc.Platform, recentTags)
	}
	var remMatched []string
	if mf != nil {
		remMatched = s.reg.LookupTagsByDigestOrConfig(ctx, ref, mf.RepoDigest, mf.PlatformDigest, mf.ConfigDigest, svc.Platform, recentTags)
	}

	out = classify.Evaluate(classify.Input{
		Ref:                 ref,
		ServiceName:         svc.Name,
		LocalRepoDigest:     svc.RepoDigest,
		LocalImageID:        svc.ImageID,
		LocalVersionLabel:   localVerLabel,
		RemoteVersionLabel:  mf.VersionLabel,
		RemoteManifest:      mf,
		CurrentMatchedTags:  curMatched,
		RemoteMatchedTags:   remMatched,
		CandidateTags:       recentTags,
		HostPlatform:        svc.Platform,
		AllowImageIDCompare: isDSMAPI,
	})
	return out, "", "", curMatched
}
