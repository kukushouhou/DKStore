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

	isUpdate := out.Kind == classify.KindDigestUpdate || out.Kind == classify.KindVersionUpdate
	targetChanged := err != nil || prevKind != string(out.Kind) ||
		prevTargetDigest != out.TargetDigest || prevTargetVer != out.TargetVersion

	// 审计 M13：短暂网络抖动（check_failed）不应抹掉已有更新的 notified_at 或写入假 resolved；
	// 从 check_failed 恢复时，若之前已通知过则保留 notified_at 避免重复推送。
	if out.Kind == classify.KindCheckFailed && err == nil {
		if firstSeen == 0 {
			firstSeen = now
		}
	} else if prevKind == string(classify.KindCheckFailed) && isUpdate && notifiedAt.Valid {
		if firstSeen == 0 {
			firstSeen = now
		}
	} else if targetChanged || firstSeen == 0 {
		firstSeen = now
		notifiedAt = sql.NullInt64{}
	}
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

	// 若状态发生变化，记录到 finding_history（排除 check_failed 造成的假 resolved 与已通知过的恢复）
	if runID > 0 && targetChanged && out.Kind != classify.KindCheckFailed &&
		!(prevKind == string(classify.KindCheckFailed) && notifiedAt.Valid) &&
		(isUpdate || prevKind == string(classify.KindDigestUpdate) || prevKind == string(classify.KindVersionUpdate)) {
		change := "appeared"
		if !isUpdate {
			change = "resolved"
		} else if prevKind != "" && prevKind != string(classify.KindNone) && prevKind != string(classify.KindCheckFailed) {
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
		resp.TotalAppCount++
		normalCard, cautionCard, ignoredCard, isUpToDate := splitAppCards(card)
		if isUpToDate {
			resp.UpToDateCount++
		}
		if normalCard != nil {
			resp.Normal = append(resp.Normal, *normalCard)
		}
		if cautionCard != nil {
			resp.Caution = append(resp.Caution, *cautionCard)
		}
		if ignoredCard != nil {
			resp.Ignored = append(resp.Ignored, *ignoredCard)
		}
	}
	return resp, nil
}

// GetApp 按请求语言返回单个 App 的完整详情（含所有服务，即使应用已是最新或被拆分为多张卡片，审计 L7）。
func (s *Service) GetApp(ctx context.Context, appKey string, lang i18n.Lang) (*AppCard, error) {
	hosts, err := s.hosts.List(ctx)
	if err != nil {
		return nil, err
	}
	hostMap := make(map[string]host.Host, len(hosts))
	for _, h := range hosts {
		hostMap[h.ID] = h
	}
	ignoreRules, _ := s.ign.ListAll(ctx)

	rows, err := s.st.DB.QueryContext(ctx, `
		SELECT a.key, a.host_id, a.name, a.source,
		       s.key, s.name, COALESCE(s.state,''), COALESCE(s.image_ref,''), COALESCE(s.repo_digest,''), COALESCE(s.current_tags_json,''),
		       COALESCE(f.kind,'none'), COALESCE(f.category,''), COALESCE(f.reason,''),
		       COALESCE(f.current_version,''), COALESCE(f.target_version,''), COALESCE(f.target_ref,''),
		       COALESCE(f.target_digest,''), COALESCE(f.target_tags_json,''), COALESCE(f.hint,''),
		       COALESCE(f.error_kind,''), COALESCE(f.error_msg,''), COALESCE(f.links_json,''), COALESCE(f.checked_at,0)
		FROM app a
		JOIN service s ON s.app_key = a.key
		LEFT JOIN finding f ON f.service_key = s.key
		WHERE a.key = ? AND a.gone_at IS NULL AND s.gone_at IS NULL
		ORDER BY s.name ASC`, appKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var card *AppCard
	for rows.Next() {
		var (
			aKey, hostID, appName, appSource                       string
			svcKey, svcName, state, imageRef, repoDigest, curTagsJ string
			fKind, fCat, fReason, curVer, tgtVer, tgtRef           string
			tgtDigest, tgtTagsJ, fHint, errKind, errMsg, linksJ    string
			checkedAt                                              int64
		)
		if err := rows.Scan(
			&aKey, &hostID, &appName, &appSource,
			&svcKey, &svcName, &state, &imageRef, &repoDigest, &curTagsJ,
			&fKind, &fCat, &fReason, &curVer, &tgtVer, &tgtRef,
			&tgtDigest, &tgtTagsJ, &fHint, &errKind, &errMsg, &linksJ, &checkedAt,
		); err != nil {
			continue
		}
		h := hostMap[hostID]
		if card == nil {
			card = &AppCard{
				Key:        aKey,
				HostID:     hostID,
				HostName:   h.Name,
				HostStatus: h.Status,
				Name:       appName,
				Source:     appSource,
				Group:      "up_to_date",
			}
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
	}
	if card == nil {
		return nil, nil
	}

	normalCard, cautionCard, ignoredCard, _ := splitAppCards(card)
	switch {
	case cautionCard != nil:
		card.Group = "caution"
		card.SummaryCurrent = cautionCard.SummaryCurrent
		card.SummaryTarget = cautionCard.SummaryTarget
		card.ReasonText = cautionCard.ReasonText
		card.IsDatabase = cautionCard.IsDatabase
		card.DatabaseName = cautionCard.DatabaseName
		card.Links = cautionCard.Links
	case normalCard != nil:
		card.Group = "normal"
		card.SummaryCurrent = normalCard.SummaryCurrent
		card.SummaryTarget = normalCard.SummaryTarget
		card.ReasonText = normalCard.ReasonText
		card.Links = normalCard.Links
	case ignoredCard != nil:
		card.Group = "ignored"
		card.IgnoreMode = ignoredCard.IgnoreMode
		card.SummaryCurrent = ignoredCard.SummaryCurrent
		card.SummaryTarget = ignoredCard.SummaryTarget
		card.ReasonText = ignoredCard.ReasonText
		card.Links = ignoredCard.Links
	}
	if normalCard != nil {
		card.UpdateSvcKeys = append(card.UpdateSvcKeys, normalCard.UpdateSvcKeys...)
	}
	if cautionCard != nil {
		card.UpdateSvcKeys = append(card.UpdateSvcKeys, cautionCard.UpdateSvcKeys...)
	}
	return card, nil
}

// splitAppCards 将一个 App 下的服务按 (normal / caution / ignored) 切分为对应分组的卡片，
// 避免同一个 Compose 项目里的 Redis/MySQL 把同项目的普通 Web 服务也拖入默认收起的“需谨慎更新”而使“可更新”列表变空。
func splitAppCards(base *AppCard) (normalCard, cautionCard, ignoredCard *AppCard, upToDate bool) {
	var normalSvcs, cautionSvcs, ignoredSvcs []ServiceItem
	for _, svc := range base.Services {
		isUpd := svc.Kind == string(classify.KindDigestUpdate) || svc.Kind == string(classify.KindVersionUpdate)
		if svc.Ignored && (isUpd || svc.IgnoreMode == ignore.ModeForever) {
			ignoredSvcs = append(ignoredSvcs, svc)
			continue
		}
		if isUpd {
			if svc.Category == string(classify.CategoryCaution) {
				cautionSvcs = append(cautionSvcs, svc)
			} else {
				normalSvcs = append(normalSvcs, svc)
			}
		}
	}

	multiGroup := b2i(len(normalSvcs) > 0) + b2i(len(cautionSvcs) > 0) + b2i(len(ignoredSvcs) > 0) > 1
	formatCardName := func(svcs []ServiceItem) string {
		if !multiGroup && len(base.Services) == 1 {
			return base.Name
		}
		if len(svcs) == 1 && svcs[0].Name != "main" && svcs[0].Name != base.Name {
			return base.Name + " / " + svcs[0].Name
		}
		return base.Name
	}

	if len(normalSvcs) > 0 {
		primary := normalSvcs[0]
		c := *base
		c.Name = formatCardName(normalSvcs)
		c.Group = "normal"
		c.SummaryCurrent = primary.CurrentVersion
		c.SummaryTarget = primary.TargetVersion
		c.ReasonText = primary.ReasonText
		c.Links = primary.Links
		c.UpdateSvcKeys = nil
		for _, s := range normalSvcs {
			c.UpdateSvcKeys = append(c.UpdateSvcKeys, s.Key)
		}
		normalCard = &c
	}

	if len(cautionSvcs) > 0 {
		primary := cautionSvcs[0]
		c := *base
		c.Name = formatCardName(cautionSvcs)
		c.Group = "caution"
		c.SummaryCurrent = primary.CurrentVersion
		c.SummaryTarget = primary.TargetVersion
		c.ReasonText = primary.ReasonText
		c.DatabaseName = primary.DatabaseName
		c.IsDatabase = primary.DatabaseName != ""
		c.Links = primary.Links
		c.UpdateSvcKeys = nil
		for _, s := range cautionSvcs {
			c.UpdateSvcKeys = append(c.UpdateSvcKeys, s.Key)
		}
		cautionCard = &c
	}

	if len(ignoredSvcs) > 0 {
		primary := ignoredSvcs[0]
		c := *base
		c.Name = formatCardName(ignoredSvcs)
		c.Group = "ignored"
		c.IgnoreMode = primary.IgnoreMode
		c.SummaryCurrent = primary.CurrentVersion
		c.SummaryTarget = primary.TargetVersion
		c.ReasonText = primary.ReasonText
		c.Links = primary.Links
		ignoredCard = &c
	}

	upToDate = len(normalSvcs) == 0 && len(cautionSvcs) == 0 && len(ignoredSvcs) == 0
	return
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
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
