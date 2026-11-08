package checker

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/kukushouhou/DKStore/internal/classify"
	"github.com/kukushouhou/DKStore/internal/host"
	"github.com/kukushouhou/DKStore/internal/i18n"
	"github.com/kukushouhou/DKStore/internal/ignore"
	"github.com/kukushouhou/DKStore/rules"
)

func (s *Service) saveFinding(
	ctx context.Context,
	runID int64,
	svc svcRow,
	out classify.Output,
	errKind, errMsg string,
	curTags []string,
) (isNewUpdate bool) {
	now := time.Now().Unix()
	if len(curTags) > 0 {
		b, _ := json.Marshal(curTags)
		_, _ = s.st.DB.ExecContext(ctx, `UPDATE service SET current_tags_json=? WHERE key=?`, string(b), svc.Key)
	}

	var prevKind, prevTargetDigest, prevTargetVer string
	var firstSeen int64
	var notifiedAt sql.NullInt64
	err := s.st.DB.QueryRowContext(ctx, `
		SELECT kind, COALESCE(target_digest,''), COALESCE(target_version,''), COALESCE(first_seen_at,0), notified_at
		FROM finding WHERE service_key=?`, svc.Key).Scan(
		&prevKind, &prevTargetDigest, &prevTargetVer, &firstSeen, &notifiedAt,
	)

	targetChanged := err != nil || prevKind != string(out.Kind) ||
		prevTargetDigest != out.TargetDigest || prevTargetVer != out.TargetVersion

	if targetChanged || firstSeen == 0 {
		firstSeen = now
		notifiedAt = sql.NullInt64{}
	}
	isUpdate := out.Kind == classify.KindDigestUpdate || out.Kind == classify.KindVersionUpdate
	if isUpdate && !notifiedAt.Valid {
		isNewUpdate = true
	}

	// Reason 保存 "reason|databaseName"，便于查询时按任意语言格式化
	reasonStored := out.Reason
	if out.DatabaseName != "" {
		reasonStored = out.Reason + "|" + out.DatabaseName
	}
	targetTagsJSON, _ := json.Marshal(out.TargetTags)
	linksJSON, _ := json.Marshal(out.Links)

	_, _ = s.st.DB.ExecContext(ctx, `
		INSERT INTO finding(
			service_key, kind, category, reason, current_version, target_version,
			target_ref, target_digest, target_tags_json, hint, error_kind, error_msg,
			links_json, first_seen_at, checked_at, notified_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(service_key) DO UPDATE SET
			kind=excluded.kind,
			category=excluded.category,
			reason=excluded.reason,
			current_version=excluded.current_version,
			target_version=excluded.target_version,
			target_ref=excluded.target_ref,
			target_digest=excluded.target_digest,
			target_tags_json=excluded.target_tags_json,
			hint=excluded.hint,
			error_kind=excluded.error_kind,
			error_msg=excluded.error_msg,
			links_json=excluded.links_json,
			first_seen_at=excluded.first_seen_at,
			checked_at=excluded.checked_at,
			notified_at=excluded.notified_at`,
		svc.Key, string(out.Kind), string(out.Category), reasonStored,
		out.CurrentVersion, out.TargetVersion, out.TargetRef, out.TargetDigest,
		string(targetTagsJSON), out.Hint, errKind, errMsg, string(linksJSON),
		firstSeen, now, notifiedAt,
	)

	// 若状态发生变化，记录到 finding_history
	if runID > 0 && targetChanged && (isUpdate || prevKind == string(classify.KindDigestUpdate) || prevKind == string(classify.KindVersionUpdate)) {
		change := "appeared"
		if !isUpdate {
			change = "resolved"
		} else if prevKind != "" && prevKind != string(classify.KindNone) {
			change = "changed"
		}
		snap, _ := json.Marshal(map[string]any{
			"hostName":       svc.HostName,
			"appName":        svc.AppName,
			"serviceName":    svc.Name,
			"imageRef":       svc.ImageRef,
			"kind":           out.Kind,
			"category":       out.Category,
			"currentVersion": out.CurrentVersion,
			"targetVersion":  out.TargetVersion,
		})
		_, _ = s.st.DB.ExecContext(ctx, `
			INSERT INTO finding_history(check_run_id, service_key, change, snapshot, created_at)
			VALUES (?, ?, ?, ?, ?)`, runID, svc.Key, change, string(snap), now)
	}
	return isNewUpdate
}

// ServiceItem 是前端展示的服务级状态。
type ServiceItem struct {
	Key            string         `json:"key"`
	Name           string         `json:"name"`
	State          string         `json:"state"`
	ImageRef       string         `json:"imageRef"`
	RepoDigest     string         `json:"repoDigest,omitempty"`
	CurrentTags    []string       `json:"currentTags,omitempty"`
	Kind           string         `json:"kind"`               // none | digest_update | version_update | check_failed | uncomparable
	Category       string         `json:"category,omitempty"` // normal | caution
	Reason         string         `json:"reason,omitempty"`
	DatabaseName   string         `json:"databaseName,omitempty"`
	ReasonText     string         `json:"reasonText,omitempty"`
	CurrentVersion string         `json:"currentVersion,omitempty"`
	TargetVersion  string         `json:"targetVersion,omitempty"`
	RawTargetVer   string         `json:"rawTargetVersion,omitempty"`
	TargetRef      string         `json:"targetRef,omitempty"`
	TargetDigest   string         `json:"targetDigest,omitempty"`
	TargetTags     []string       `json:"targetTags,omitempty"`
	Hint           string         `json:"hint,omitempty"`
	ErrorKind      string         `json:"errorKind,omitempty"`
	ErrorMsg       string         `json:"errorMsg,omitempty"`
	Links          rules.RepoLink `json:"links"`
	Ignored        bool           `json:"ignored"`
	IgnoreMode     string         `json:"ignoreMode,omitempty"`
	CheckedAt      int64          `json:"checkedAt,omitempty"`
}

// AppCard 是首页三组卡片以及详情抽屉的数据结构。
type AppCard struct {
	Key            string         `json:"key"`
	HostID         string         `json:"hostId"`
	HostName       string         `json:"hostName"`
	HostStatus     string         `json:"hostStatus"`
	Name           string         `json:"name"`
	Source         string         `json:"source"` // standalone | compose | dsm_project | vendor_managed
	Group          string         `json:"group"`  // normal | caution | ignored | up_to_date
	SummaryCurrent string         `json:"summaryCurrent,omitempty"`
	SummaryTarget  string         `json:"summaryTarget,omitempty"`
	ReasonText     string         `json:"reasonText,omitempty"`
	IsDatabase     bool           `json:"isDatabase"`
	DatabaseName   string         `json:"databaseName,omitempty"`
	Links          rules.RepoLink `json:"links"`
	UpdateSvcKeys  []string       `json:"updateServiceKeys,omitempty"`
	IgnoreMode     string         `json:"ignoreMode,omitempty"`
	Services       []ServiceItem  `json:"services"`
}

// FailedItem 汇总无法检查的服务。
type FailedItem struct {
	ServiceKey  string `json:"serviceKey"`
	AppKey      string `json:"appKey"`
	AppName     string `json:"appName"`
	ServiceName string `json:"serviceName"`
	HostName    string `json:"hostName"`
	ImageRef    string `json:"imageRef"`
	ErrorKind   string `json:"errorKind"`
	ErrorMsg    string `json:"errorMsg"`
}

// OverviewResponse 是 GET /api/v1/overview 的返回体。
type OverviewResponse struct {
	Hosts         []host.Host  `json:"hosts"`
	Normal        []AppCard    `json:"normal"`
	Caution       []AppCard    `json:"caution"`
	Ignored       []AppCard    `json:"ignored"`
	UpToDateCount int          `json:"upToDateCount"`
	TotalAppCount int          `json:"totalAppCount"`
	Failed        []FailedItem `json:"failed"`
	LastCheckedAt int64        `json:"lastCheckedAt,omitempty"`
	Progress      Progress     `json:"progress"`
}

// Overview 按请求语言生成首页三组卡片数据。
func (s *Service) Overview(ctx context.Context, hostIDFilter string, lang i18n.Lang) (*OverviewResponse, error) {
	hosts, err := s.hosts.List(ctx)
	if err != nil {
		return nil, err
	}
	hostMap := make(map[string]host.Host, len(hosts))
	for _, h := range hosts {
		hostMap[h.ID] = h
	}
	ignoreRules, _ := s.ign.ListAll(ctx)

	q := `
		SELECT a.key, a.host_id, a.name, a.source,
		       s.key, s.name, COALESCE(s.state,''), COALESCE(s.image_ref,''), COALESCE(s.repo_digest,''), COALESCE(s.current_tags_json,''),
		       COALESCE(f.kind,'none'), COALESCE(f.category,''), COALESCE(f.reason,''),
		       COALESCE(f.current_version,''), COALESCE(f.target_version,''), COALESCE(f.target_ref,''),
		       COALESCE(f.target_digest,''), COALESCE(f.target_tags_json,''), COALESCE(f.hint,''),
		       COALESCE(f.error_kind,''), COALESCE(f.error_msg,''), COALESCE(f.links_json,''), COALESCE(f.checked_at,0)
		FROM app a
		JOIN service s ON s.app_key = a.key
		LEFT JOIN finding f ON f.service_key = s.key
		WHERE a.gone_at IS NULL AND s.gone_at IS NULL`
	var args []any
	if hostIDFilter != "" {
		q += ` AND a.host_id = ?`
		args = append(args, hostIDFilter)
	}
	q += ` ORDER BY a.name ASC, s.name ASC`

	rows, err := s.st.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	appOrder := []string{}
	appMap := make(map[string]*AppCard)
	var failedList []FailedItem
	var maxChecked int64

	for rows.Next() {
		var (
			appKey, hostID, appName, appSource                     string
			svcKey, svcName, state, imageRef, repoDigest, curTagsJ string
			fKind, fCat, fReason, curVer, tgtVer, tgtRef           string
			tgtDigest, tgtTagsJ, fHint, errKind, errMsg, linksJ    string
			checkedAt                                              int64
		)
		if err := rows.Scan(
			&appKey, &hostID, &appName, &appSource,
			&svcKey, &svcName, &state, &imageRef, &repoDigest, &curTagsJ,
			&fKind, &fCat, &fReason, &curVer, &tgtVer, &tgtRef,
			&tgtDigest, &tgtTagsJ, &fHint, &errKind, &errMsg, &linksJ, &checkedAt,
		); err != nil {
			continue
		}
		if checkedAt > maxChecked {
			maxChecked = checkedAt
		}
		h := hostMap[hostID]
		card, exists := appMap[appKey]
		if !exists {
			card = &AppCard{
				Key:        appKey,
				HostID:     hostID,
				HostName:   h.Name,
				HostStatus: h.Status,
				Name:       appName,
				Source:     appSource,
			}
			appMap[appKey] = card
			appOrder = append(appOrder, appKey)
		}

		reasonCode, dbName, _ := strings.Cut(fReason, "|")
		displayTarget := i18n.FormatTargetVersion(lang, tgtVer)
		reasonText := i18n.FormatReason(lang, reasonCode, dbName, curVer, displayTarget)
		hintText := i18n.FormatHint(lang, fHint)
		localErrMsg := i18n.FormatRegistryError(lang, errKind, errMsg)

		var curTags, tgtTags []string
		_ = json.Unmarshal([]byte(curTagsJ), &curTags)
		_ = json.Unmarshal([]byte(tgtTagsJ), &tgtTags)
		var links rules.RepoLink
		_ = json.Unmarshal([]byte(linksJ), &links)

		rule, hasRule := ignoreRules[svcKey]
		isIgnored := hasRule && (rule.Mode == ignore.ModeForever || ignore.Matches(rule, fKind, tgtDigest, tgtVer))

		item := ServiceItem{
			Key:            svcKey,
			Name:           svcName,
			State:          state,
			ImageRef:       imageRef,
			RepoDigest:     repoDigest,
			CurrentTags:    curTags,
			Kind:           fKind,
			Category:       fCat,
			Reason:         reasonCode,
			DatabaseName:   dbName,
			ReasonText:     reasonText,
			CurrentVersion: curVer,
			TargetVersion:  displayTarget,
			RawTargetVer:   tgtVer,
			TargetRef:      tgtRef,
			TargetDigest:   tgtDigest,
			TargetTags:     tgtTags,
			Hint:           hintText,
			ErrorKind:      errKind,
			ErrorMsg:       localErrMsg,
			Links:          links,
			Ignored:        isIgnored,
			CheckedAt:      checkedAt,
		}
		if isIgnored {
			item.IgnoreMode = rule.Mode
		}
		card.Services = append(card.Services, item)

		if fKind == string(classify.KindCheckFailed) {
			failedList = append(failedList, FailedItem{
				ServiceKey:  svcKey,
				AppKey:      appKey,
				AppName:     appName,
				ServiceName: svcName,
				HostName:    h.Name,
				ImageRef:    imageRef,
				ErrorKind:   errKind,
				ErrorMsg:    localErrMsg,
			})
		}
	}

	resp := &OverviewResponse{
		Hosts:         hosts,
		Normal:        []AppCard{},
		Caution:       []AppCard{},
		Ignored:       []AppCard{},
		Failed:        failedList,
		LastCheckedAt: maxChecked,
		Progress:      s.CurrentProgress(),
	}

	for _, key := range appOrder {
		card := appMap[key]
		finalizeAppCard(card)
		resp.TotalAppCount++
		switch card.Group {
		case "normal":
			resp.Normal = append(resp.Normal, *card)
		case "caution":
			resp.Caution = append(resp.Caution, *card)
		case "ignored":
			resp.Ignored = append(resp.Ignored, *card)
		default:
			resp.UpToDateCount++
		}
	}
	return resp, nil
}

// GetApp 按请求语言返回单个 App 的完整详情。
func (s *Service) GetApp(ctx context.Context, appKey string, lang i18n.Lang) (*AppCard, error) {
	ov, err := s.Overview(ctx, "", lang)
	if err != nil {
		return nil, err
	}
	for _, list := range [][]AppCard{ov.Normal, ov.Caution, ov.Ignored} {
		for i := range list {
			if list[i].Key == appKey {
				return &list[i], nil
			}
		}
	}
	return nil, nil
}

func finalizeAppCard(card *AppCard) {
	var activeUpdates []ServiceItem
	var ignoredUpdates []ServiceItem
	hasCaution := false

	for _, svc := range card.Services {
		if card.Links.Repo == "" && svc.Links.Repo != "" {
			card.Links = svc.Links
		}
		isUpd := svc.Kind == string(classify.KindDigestUpdate) || svc.Kind == string(classify.KindVersionUpdate)
		if svc.Ignored && (isUpd || svc.IgnoreMode == ignore.ModeForever) {
			ignoredUpdates = append(ignoredUpdates, svc)
			continue
		}
		if isUpd {
			activeUpdates = append(activeUpdates, svc)
			card.UpdateSvcKeys = append(card.UpdateSvcKeys, svc.Key)
			if svc.Category == string(classify.CategoryCaution) {
				hasCaution = true
			}
		}
	}

	if len(activeUpdates) > 0 {
		primary := activeUpdates[0]
		card.SummaryCurrent = primary.CurrentVersion
		card.SummaryTarget = primary.TargetVersion
		card.Links = primary.Links
		if hasCaution {
			card.Group = "caution"
			for _, u := range activeUpdates {
				if u.Category == string(classify.CategoryCaution) {
					card.ReasonText = u.ReasonText
					card.DatabaseName = u.DatabaseName
					card.IsDatabase = u.DatabaseName != ""
					break
				}
			}
		} else {
			card.Group = "normal"
			card.ReasonText = primary.ReasonText
		}
		return
	}

	if len(ignoredUpdates) > 0 {
		primary := ignoredUpdates[0]
		card.Group = "ignored"
		card.IgnoreMode = primary.IgnoreMode
		card.SummaryCurrent = primary.CurrentVersion
		card.SummaryTarget = primary.TargetVersion
		card.ReasonText = primary.ReasonText
		card.Links = primary.Links
		return
	}

	card.Group = "up_to_date"
}

// CheckRunItem 是检测历史记录。
type CheckRunItem struct {
	ID         int64            `json:"id"`
	Trigger    string           `json:"trigger"`
	StartedAt  int64            `json:"startedAt"`
	FinishedAt int64            `json:"finishedAt"`
	Status     string           `json:"status"`
	Summary    map[string]int   `json:"summary"`
	Changes    []map[string]any `json:"changes"`
}

// ListCheckRuns 返回最近的检测历史（实施文档 §10.1）。
func (s *Service) ListCheckRuns(ctx context.Context, limit int) ([]CheckRunItem, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	rows, err := s.st.DB.QueryContext(ctx, `
		SELECT id, trigger, started_at, COALESCE(finished_at,0), status, COALESCE(summary_json,'{}')
		FROM check_run ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []CheckRunItem
	for rows.Next() {
		var r CheckRunItem
		var sumStr string
		if err := rows.Scan(&r.ID, &r.Trigger, &r.StartedAt, &r.FinishedAt, &r.Status, &sumStr); err != nil {
			continue
		}
		_ = json.Unmarshal([]byte(sumStr), &r.Summary)
		runs = append(runs, r)
	}
	rows.Close()

	for i := range runs {
		cRows, err := s.st.DB.QueryContext(ctx, `
			SELECT change, snapshot FROM finding_history WHERE check_run_id=? ORDER BY id ASC`, runs[i].ID)
		if err != nil {
			continue
		}
		for cRows.Next() {
			var chg, snapStr string
			if cRows.Scan(&chg, &snapStr) == nil {
				var snap map[string]any
				if json.Unmarshal([]byte(snapStr), &snap) == nil {
					snap["change"] = chg
					runs[i].Changes = append(runs[i].Changes, snap)
				}
			}
		}
		cRows.Close()
	}
	return runs, nil
}

// StartScheduler 启动后台周期检查协程。
func (s *Service) StartScheduler(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cfg := s.ntf.LoadConfig(ctx)
				if cfg.CheckIntervalHours <= 0 {
					continue
				}
				var lastStart int64
				_ = s.st.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(started_at),0) FROM check_run`).Scan(&lastStart)
				if time.Since(time.Unix(lastStart, 0)) >= time.Duration(cfg.CheckIntervalHours)*time.Hour {
					s.Trigger("schedule", "", false)
				}
			}
		}
	}()
}
