package checker

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"github.com/kukushouhou/DKStore/internal/classify"
	"github.com/kukushouhou/DKStore/internal/host"
	"github.com/kukushouhou/DKStore/internal/i18n"
	"github.com/kukushouhou/DKStore/internal/ignore"
	"github.com/kukushouhou/DKStore/internal/platform"
	"github.com/kukushouhou/DKStore/internal/registry"
	"github.com/kukushouhou/DKStore/internal/version"
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

	if out.CurrentVersion == "" {
		out.CurrentVersion = fallbackCurrentVersion(svc.ImageRef, curTags, svc.LabelsJSON)
	}
	if out.CurrentResolvedTag == "" {
		out.CurrentResolvedTag = fallbackCurrentResolvedTag(svc.ImageRef, curTags, svc.LabelsJSON)
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

	// Reason 保存 "reason|databaseName|roleKind"，便于查询时按任意语言格式化并还原组件角色
	reasonStored := out.Reason
	if out.DatabaseName != "" {
		reasonStored = out.Reason + "|" + out.DatabaseName
		if out.RoleKind != "" {
			reasonStored += "|" + out.RoleKind
		}
	}
	targetTagsJSON, _ := json.Marshal(out.TargetTags)
	linksJSON, _ := json.Marshal(out.Links)

	_, _ = s.st.DB.ExecContext(ctx, `
		INSERT INTO finding(
			service_key, kind, category, reason, current_version, current_resolved_tag,
			target_version, target_resolved_tag,
			target_ref, target_digest, target_tags_json, hint, error_kind, error_msg,
			links_json, first_seen_at, checked_at, notified_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(service_key) DO UPDATE SET
			kind=excluded.kind,
			category=excluded.category,
			reason=excluded.reason,
			current_version=excluded.current_version,
			current_resolved_tag=excluded.current_resolved_tag,
			target_version=excluded.target_version,
			target_resolved_tag=excluded.target_resolved_tag,
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
		out.CurrentVersion, out.CurrentResolvedTag,
		out.TargetVersion, out.TargetResolvedTag,
		out.TargetRef, out.TargetDigest,
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
	Key              string         `json:"key"`
	Name             string         `json:"name"`
	State            string         `json:"state"`
	ImageRef         string         `json:"imageRef"`
	IconURL          string         `json:"iconUrl,omitempty"`
	RepoDigest       string         `json:"repoDigest,omitempty"`
	CurrentTags      []string       `json:"currentTags,omitempty"`
	Kind             string         `json:"kind"`               // none | digest_update | version_update | check_failed | uncomparable
	Category         string         `json:"category,omitempty"` // normal | caution
	Reason           string         `json:"reason,omitempty"`
	IsDatabase       bool           `json:"isDatabase,omitempty"`
	DatabaseName     string         `json:"databaseName,omitempty"`
	RoleKind         string         `json:"roleKind,omitempty"` // database | mq | middleware
	ReasonText       string         `json:"reasonText,omitempty"`
	CurrentVersion   string         `json:"currentVersion,omitempty"`
	TargetVersion    string         `json:"targetVersion,omitempty"`
	RawTargetVer     string         `json:"rawTargetVersion,omitempty"`
	TargetRef        string         `json:"targetRef,omitempty"`
	TargetDigest     string         `json:"targetDigest,omitempty"`
	TargetTags       []string       `json:"targetTags,omitempty"`
	CurrentUpdatedAt int64          `json:"currentUpdatedAt,omitempty"`
	TargetUpdatedAt  int64          `json:"targetUpdatedAt,omitempty"`
	Hint             string         `json:"hint,omitempty"`
	ErrorKind        string         `json:"errorKind,omitempty"`
	ErrorMsg         string         `json:"errorMsg,omitempty"`
	Links            rules.RepoLink `json:"links"`
	Ignored          bool           `json:"ignored"`
	IgnoreMode       string         `json:"ignoreMode,omitempty"`
	IgnorePrefix     string         `json:"ignorePrefix,omitempty"`
	IgnoreTargetTag  string         `json:"ignoreTargetTag,omitempty"`
	CheckedAt        int64          `json:"checkedAt,omitempty"`
}

// AppCard 是首页三组卡片（可更新 / 需谨慎更新 / 无需更新）及详情抽屉的数据结构（附录 D.2）。
type AppCard struct {
	Key               string         `json:"key"`
	HostID            string         `json:"hostId"`
	HostName          string         `json:"hostName"`
	HostStatus        string         `json:"hostStatus"`
	Name              string         `json:"name"`
	AppName           string         `json:"appName"`
	ProjectName       string         `json:"projectName,omitempty"`
	TotalServiceCount int            `json:"totalServiceCount"`
	Source            string         `json:"source"` // standalone | compose | dsm_project | vendor_managed
	Group             string         `json:"group"`  // normal | caution | no_update
	IconURL           string         `json:"iconUrl,omitempty"`
	SummaryCurrent    string         `json:"summaryCurrent,omitempty"`
	SummaryTarget     string         `json:"summaryTarget,omitempty"`
	ReasonText        string         `json:"reasonText,omitempty"`
	IsDatabase        bool           `json:"isDatabase"`
	DatabaseName      string         `json:"databaseName,omitempty"`
	RoleKind          string         `json:"roleKind,omitempty"` // database | mq | middleware
	Links             rules.RepoLink `json:"links"`
	UpdateSvcKeys     []string       `json:"updateServiceKeys,omitempty"`
	Ignored           bool           `json:"ignored"`
	IgnoreMode        string         `json:"ignoreMode,omitempty"`
	IgnorePrefix      string         `json:"ignorePrefix,omitempty"`
	IgnoreTargetTag   string         `json:"ignoreTargetTag,omitempty"`
	ComposeEditable   bool           `json:"composeEditable"`
	Services          []ServiceItem  `json:"services"`
	AllServices       []ServiceItem  `json:"allServices,omitempty"`
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

// OverviewResponse 是 GET /api/v1/overview 的返回体（附录 D.2 三分组：Normal / Caution / NoUpdate）。
type OverviewResponse struct {
	Hosts         []host.Host  `json:"hosts"`
	Normal        []AppCard    `json:"normal"`
	Caution       []AppCard    `json:"caution"`
	NoUpdate      []AppCard    `json:"noUpdate"`
	Ignored       []AppCard    `json:"ignored"`
	UpToDateCount int          `json:"upToDateCount"`
	TotalAppCount int          `json:"totalAppCount"`
	Failed        []FailedItem `json:"failed"`
	LastCheckedAt int64        `json:"lastCheckedAt,omitempty"`
	Progress      Progress     `json:"progress"`
}

func fallbackCurrentVersion(imageRef string, curTags []string, labelsJSON string) string {
	var labels map[string]string
	if labelsJSON != "" {
		_ = json.Unmarshal([]byte(labelsJSON), &labels)
	}
	labelVer := classify.ExtractVersionLabel(labels)
	pr, err := version.ParseImageRef(imageRef)
	if err != nil {
		if labelVer != "" {
			return labelVer
		}
		return strings.TrimSpace(imageRef)
	}
	if best := version.SelectBestDisplayTag(pr.Tag, curTags); best != "" {
		return best
	}
	if labelVer != "" {
		return labelVer
	}
	if pr.Tag != "" {
		return pr.Tag
	}
	if pr.Digest != "" {
		if len(pr.Digest) > 19 {
			return pr.Digest[:19]
		}
		return pr.Digest
	}
	return imageRef
}

func fallbackCurrentResolvedTag(imageRef string, curTags []string, labelsJSON string) string {
	var labels map[string]string
	if labelsJSON != "" {
		_ = json.Unmarshal([]byte(labelsJSON), &labels)
	}
	labelVer := classify.ExtractVersionLabel(labels)
	pr, err := version.ParseImageRef(imageRef)
	if err == nil {
		if best := version.SelectBestDisplayTag(pr.Tag, curTags); best != "" && version.ParseTag(best).Scheme != version.SchemeUnknown {
			return best
		}
	}
	if labelVer != "" && version.ParseTag(labelVer).Scheme != version.SchemeUnknown {
		return labelVer
	}
	if err == nil && pr.Tag != "" && version.ParseTag(pr.Tag).Scheme != version.SchemeUnknown {
		return pr.Tag
	}
	return ""
}

func resolveServiceRoleAndLinks(svcName, imageRef, storedReason string, storedLinks rules.RepoLink) (reasonCode, dbName, roleKind string, links rules.RepoLink) {
	parts := strings.Split(storedReason, "|")
	if len(parts) > 0 {
		reasonCode = parts[0]
	}
	if len(parts) > 1 {
		dbName = parts[1]
	}
	if len(parts) > 2 {
		roleKind = parts[2]
	}
	links = storedLinks

	pr, err := version.ParseImageRef(imageRef)
	if err == nil {
		rs := rules.Default()
		if dbRule, _ := rs.MatchDatabase(pr.Registry, pr.Repository); dbRule != nil {
			if dbName == "" {
				dbName = dbRule.Name
			}
			if roleKind == "" {
				roleKind = dbRule.Kind
			}
			if links.Releases == "" && dbRule.Docs != "" {
				links.Releases = dbRule.Docs
			}
		}
		if links.Repo == "" && links.Releases == "" {
			resolved := rs.ResolveLinks(pr.Registry, pr.Repository, "")
			links.Repo = resolved.Repo
			if links.Releases == "" {
				links.Releases = resolved.Releases
			}
		}
	}
	if dbName == "" {
		if hName, hKind, ok := classify.InferStatefulRole(svcName); ok {
			dbName = hName
			roleKind = hKind
		}
	}
	if dbName != "" && roleKind == "" {
		roleKind = "database"
	}
	return
}

func isComposeEditable(appSource, hostTransport, projJSON string) bool {
	if appSource == string(platform.SourceVendorManaged) || appSource == string(platform.SourceStandalone) {
		return false
	}
	var proj platform.ProjectInfo
	if projJSON != "" {
		_ = json.Unmarshal([]byte(projJSON), &proj)
	}
	if appSource == string(platform.SourceDSMProject) && strings.TrimSpace(proj.DSMProjectID) != "" {
		return true
	}
	if appSource == string(platform.SourceCompose) && hostTransport != "dsm_api" {
		return proj.SourceReadable || len(proj.ConfigFiles) > 0
	}
	return false
}

func evaluateServiceIgnoreState(rule ignore.Rule, hasRule bool, fKind, curVer, tgtDigest, tgtVer string) (ignored bool, mode, prefix, targetTag string) {
	if !hasRule {
		return false, "", "", ""
	}
	isUpd := fKind == string(classify.KindDigestUpdate) || fKind == string(classify.KindVersionUpdate)
	if isUpd {
		if ignore.Matches(rule, fKind, tgtDigest, tgtVer) {
			tTag := rule.TargetTag
			if tTag == "" {
				tTag = tgtVer
			}
			return true, rule.Mode, rule.Prefix, tTag
		}
		return false, "", rule.Prefix, ""
	}
	// 当前无未抑制的新版本时（卡片位于 no_update）：保留用户已配置的永久忽略、前缀锁定或未过期的跳过版本标记
	switch rule.Mode {
	case ignore.ModeForever:
		return true, rule.Mode, "", ""
	case ignore.ModePrefix:
		if rule.Prefix != "" {
			return true, rule.Mode, rule.Prefix, ""
		}
	case ignore.ModeThisVersion:
		if rule.TargetTag != "" && rule.TargetTag != curVer {
			return true, rule.Mode, "", rule.TargetTag
		}
		if rule.TargetTag == "" && rule.TargetDigest != "" {
			return true, rule.Mode, "", ""
		}
	}
	return false, "", "", ""
}

func iconURLForService(svcKey string) string {
	if svcKey == "" {
		return ""
	}
	return "/api/v1/icons?serviceKey=" + url.QueryEscape(svcKey)
}

func (s *Service) lookupTagTimestamps(ctx context.Context, cache map[string]map[string]int64, imageRef, curVer, tgtVer string) (curTS, tgtTS int64) {
	if s.reg == nil || imageRef == "" {
		return 0, 0
	}
	pr, err := version.ParseImageRef(imageRef)
	if err != nil {
		return 0, 0
	}
	repoKey := pr.Registry + "/" + pr.Repository
	tsByTag, ok := cache[repoKey]
	if !ok {
		tsByTag = make(map[string]int64)
		if raw, hit := s.reg.GetCache(ctx, "tags:"+repoKey); hit {
			var entries []registry.TagDigestEntry
			if json.Unmarshal(raw, &entries) == nil {
				for _, e := range entries {
					if e.UpdatedAt > 0 && e.Name != "" {
						tsByTag[e.Name] = e.UpdatedAt
					}
				}
			}
		}
		cache[repoKey] = tsByTag
	}
	lookupTS := func(v string) int64 {
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return 0
		}
		if unwrapped := version.UnwrapDisplayVersion(trimmed); unwrapped != "" {
			if ts := tsByTag[unwrapped]; ts > 0 {
				return ts
			}
		}
		if base, _, has := strings.Cut(trimmed, " ("); has {
			if ts := tsByTag[strings.TrimSpace(base)]; ts > 0 {
				return ts
			}
		}
		return tsByTag[trimmed]
	}
	curTS = lookupTS(curVer)
	if curTS == 0 && pr.Tag != "" {
		curTS = tsByTag[pr.Tag]
	}
	tgtTS = lookupTS(tgtVer)
	return
}

func normalizeStoredVersions(imageRef, fKind, curVer, tgtVer, repoDigest, tgtDigest string, curTags, tgtTags []string) (string, string) {
	pr, err := version.ParseImageRef(imageRef)
	if err != nil || pr.IsPinnedDigest() {
		return curVer, tgtVer
	}
	fbCur := ""
	if fKind == string(classify.KindDigestUpdate) {
		fbCur = repoDigest
	}
	if curVer != "" {
		bestCur := version.SelectBestDisplayTag(pr.Tag, curTags)
		if bestCur != "" && (strings.EqualFold(curVer, pr.Tag) || strings.HasPrefix(curVer, pr.Tag+" (")) {
			curVer = version.FormatTagVersionDisplay(pr.Tag, bestCur, fbCur)
		} else {
			curVer = version.FormatTagVersionDisplay(pr.Tag, curVer, fbCur)
		}
	}
	if fKind == string(classify.KindDigestUpdate) && tgtVer != "" && !strings.HasSuffix(tgtVer, "@republished") {
		bestTgt := version.SelectBestDisplayTag(pr.Tag, tgtTags)
		if bestTgt != "" && (strings.EqualFold(tgtVer, pr.Tag) || strings.HasPrefix(tgtVer, pr.Tag+" (")) {
			tgtVer = version.FormatTagVersionDisplay(pr.Tag, bestTgt, tgtDigest)
		} else {
			tgtVer = version.FormatTagVersionDisplay(pr.Tag, tgtVer, tgtDigest)
		}
	}
	return curVer, tgtVer
}

// Overview 按请求语言生成首页三组卡片数据（Normal / Caution / NoUpdate）。
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
		SELECT a.key, a.host_id, a.name, a.source, COALESCE(a.project_json,''),
		       s.key, s.name, COALESCE(s.state,''), COALESCE(s.image_ref,''), COALESCE(s.repo_digest,''), COALESCE(s.current_tags_json,''), COALESCE(s.labels_json,''),
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
	failedList := []FailedItem{}
	var maxChecked int64

	for rows.Next() {
		var (
			appKey, hostID, appName, appSource, projJSON                        string
			svcKey, svcName, state, imageRef, repoDigest, curTagsJ, labelsJSON  string
			fKind, fCat, fReason, curVer, tgtVer, tgtRef                        string
			tgtDigest, tgtTagsJ, fHint, errKind, errMsg, linksJ                 string
			checkedAt                                                           int64
		)
		if err := rows.Scan(
			&appKey, &hostID, &appName, &appSource, &projJSON,
			&svcKey, &svcName, &state, &imageRef, &repoDigest, &curTagsJ, &labelsJSON,
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
				Key:             appKey,
				HostID:          hostID,
				HostName:        h.Name,
				HostStatus:      h.Status,
				Name:            appName,
				AppName:         appName,
				Source:          appSource,
				ComposeEditable: isComposeEditable(appSource, h.Transport, projJSON),
			}
			appMap[appKey] = card
			appOrder = append(appOrder, appKey)
		}

		var curTags, tgtTags []string
		_ = json.Unmarshal([]byte(curTagsJ), &curTags)
		_ = json.Unmarshal([]byte(tgtTagsJ), &tgtTags)
		var storedLinks rules.RepoLink
		_ = json.Unmarshal([]byte(linksJ), &storedLinks)

		if curVer == "" {
			curVer = fallbackCurrentVersion(imageRef, curTags, labelsJSON)
		}
		curVer, tgtVer = normalizeStoredVersions(imageRef, fKind, curVer, tgtVer, repoDigest, tgtDigest, curTags, tgtTags)
		reasonCode, dbName, roleKind, links := resolveServiceRoleAndLinks(svcName, imageRef, fReason, storedLinks)
		isUpd := fKind == string(classify.KindDigestUpdate) || fKind == string(classify.KindVersionUpdate)
		displayTarget := ""
		if isUpd {
			displayTarget = i18n.FormatTargetVersion(lang, tgtVer)
		}
		reasonText := i18n.FormatReasonWithRole(lang, reasonCode, dbName, roleKind, curVer, displayTarget)
		hintText := i18n.FormatHint(lang, fHint)
		localErrMsg := i18n.FormatRegistryError(lang, errKind, errMsg)

		rule, hasRule := ignoreRules[svcKey]
		isIgnored, ignMode, ignPrefix, ignTargetTag := evaluateServiceIgnoreState(rule, hasRule, fKind, curVer, tgtDigest, tgtVer)

		item := ServiceItem{
			Key:             svcKey,
			Name:            svcName,
			State:           state,
			ImageRef:        imageRef,
			IconURL:         iconURLForService(svcKey),
			RepoDigest:      repoDigest,
			CurrentTags:     curTags,
			Kind:            fKind,
			Category:        fCat,
			Reason:          reasonCode,
			IsDatabase:      dbName != "",
			DatabaseName:    dbName,
			RoleKind:        roleKind,
			ReasonText:      reasonText,
			CurrentVersion:  curVer,
			TargetVersion:   displayTarget,
			RawTargetVer:    tgtVer,
			TargetRef:       tgtRef,
			TargetDigest:    tgtDigest,
			TargetTags:      tgtTags,
			Hint:            hintText,
			ErrorKind:       errKind,
			ErrorMsg:        localErrMsg,
			Links:           links,
			Ignored:         isIgnored,
			IgnoreMode:      ignMode,
			IgnorePrefix:    ignPrefix,
			IgnoreTargetTag: ignTargetTag,
			CheckedAt:       checkedAt,
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

	rows.Close()

	tagTSCache := make(map[string]map[string]int64)
	for _, key := range appOrder {
		card := appMap[key]
		for i := range card.Services {
			sv := &card.Services[i]
			sv.CurrentUpdatedAt, sv.TargetUpdatedAt = s.lookupTagTimestamps(ctx, tagTSCache, sv.ImageRef, sv.CurrentVersion, sv.RawTargetVer)
		}
	}

	resp := &OverviewResponse{
		Hosts:         hosts,
		Normal:        []AppCard{},
		Caution:       []AppCard{},
		NoUpdate:      []AppCard{},
		Ignored:       []AppCard{},
		Failed:        failedList,
		LastCheckedAt: maxChecked,
		Progress:      s.CurrentProgress(),
	}

	for _, key := range appOrder {
		card := appMap[key]
		resp.TotalAppCount++
		normalCards, cautionCards, noUpdateCards, isUpToDate := splitAppCards(card)
		if isUpToDate {
			resp.UpToDateCount++
		}
		if len(normalCards) > 0 {
			resp.Normal = append(resp.Normal, normalCards...)
		}
		if len(cautionCards) > 0 {
			resp.Caution = append(resp.Caution, cautionCards...)
		}
		if len(noUpdateCards) > 0 {
			resp.NoUpdate = append(resp.NoUpdate, noUpdateCards...)
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
		SELECT a.key, a.host_id, a.name, a.source, COALESCE(a.project_json,''),
		       s.key, s.name, COALESCE(s.state,''), COALESCE(s.image_ref,''), COALESCE(s.repo_digest,''), COALESCE(s.current_tags_json,''), COALESCE(s.labels_json,''),
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
			aKey, hostID, appName, appSource, projJSON                          string
			svcKey, svcName, state, imageRef, repoDigest, curTagsJ, labelsJSON  string
			fKind, fCat, fReason, curVer, tgtVer, tgtRef                        string
			tgtDigest, tgtTagsJ, fHint, errKind, errMsg, linksJ                 string
			checkedAt                                                           int64
		)
		if err := rows.Scan(
			&aKey, &hostID, &appName, &appSource, &projJSON,
			&svcKey, &svcName, &state, &imageRef, &repoDigest, &curTagsJ, &labelsJSON,
			&fKind, &fCat, &fReason, &curVer, &tgtVer, &tgtRef,
			&tgtDigest, &tgtTagsJ, &fHint, &errKind, &errMsg, &linksJ, &checkedAt,
		); err != nil {
			continue
		}
		h := hostMap[hostID]
		if card == nil {
			card = &AppCard{
				Key:             aKey,
				HostID:          hostID,
				HostName:        h.Name,
				HostStatus:      h.Status,
				Name:            appName,
				AppName:         appName,
				Source:          appSource,
				Group:           "no_update",
				ComposeEditable: isComposeEditable(appSource, h.Transport, projJSON),
			}
		}

		var curTags, tgtTags []string
		_ = json.Unmarshal([]byte(curTagsJ), &curTags)
		_ = json.Unmarshal([]byte(tgtTagsJ), &tgtTags)
		var storedLinks rules.RepoLink
		_ = json.Unmarshal([]byte(linksJ), &storedLinks)

		if curVer == "" {
			curVer = fallbackCurrentVersion(imageRef, curTags, labelsJSON)
		}
		curVer, tgtVer = normalizeStoredVersions(imageRef, fKind, curVer, tgtVer, repoDigest, tgtDigest, curTags, tgtTags)
		reasonCode, dbName, roleKind, links := resolveServiceRoleAndLinks(svcName, imageRef, fReason, storedLinks)
		isUpd := fKind == string(classify.KindDigestUpdate) || fKind == string(classify.KindVersionUpdate)
		displayTarget := ""
		if isUpd {
			displayTarget = i18n.FormatTargetVersion(lang, tgtVer)
		}
		reasonText := i18n.FormatReasonWithRole(lang, reasonCode, dbName, roleKind, curVer, displayTarget)
		hintText := i18n.FormatHint(lang, fHint)
		localErrMsg := i18n.FormatRegistryError(lang, errKind, errMsg)

		rule, hasRule := ignoreRules[svcKey]
		isIgnored, ignMode, ignPrefix, ignTargetTag := evaluateServiceIgnoreState(rule, hasRule, fKind, curVer, tgtDigest, tgtVer)

		item := ServiceItem{
			Key:             svcKey,
			Name:            svcName,
			State:           state,
			ImageRef:        imageRef,
			IconURL:         iconURLForService(svcKey),
			RepoDigest:      repoDigest,
			CurrentTags:     curTags,
			Kind:            fKind,
			Category:        fCat,
			Reason:          reasonCode,
			IsDatabase:      dbName != "",
			DatabaseName:    dbName,
			RoleKind:        roleKind,
			ReasonText:      reasonText,
			CurrentVersion:  curVer,
			TargetVersion:   displayTarget,
			RawTargetVer:    tgtVer,
			TargetRef:       tgtRef,
			TargetDigest:    tgtDigest,
			TargetTags:      tgtTags,
			Hint:            hintText,
			ErrorKind:       errKind,
			ErrorMsg:        localErrMsg,
			Links:           links,
			Ignored:         isIgnored,
			IgnoreMode:      ignMode,
			IgnorePrefix:    ignPrefix,
			IgnoreTargetTag: ignTargetTag,
			CheckedAt:       checkedAt,
		}
		card.Services = append(card.Services, item)
	}
	rows.Close()
	if card == nil {
		return nil, nil
	}

	tagTSCache := make(map[string]map[string]int64)
	for i := range card.Services {
		sv := &card.Services[i]
		sv.CurrentUpdatedAt, sv.TargetUpdatedAt = s.lookupTagTimestamps(ctx, tagTSCache, sv.ImageRef, sv.CurrentVersion, sv.RawTargetVer)
	}

	card.TotalServiceCount = len(card.Services)
	card.AllServices = append([]ServiceItem(nil), card.Services...)
	if len(card.Services) > 1 {
		card.ProjectName = card.AppName
	}

	normalCards, cautionCards, noUpdateCards, _ := splitAppCards(card)
	switch {
	case len(cautionCards) > 0:
		c0 := cautionCards[0]
		card.Group = "caution"
		card.IconURL = c0.IconURL
		card.SummaryCurrent = c0.SummaryCurrent
		card.SummaryTarget = c0.SummaryTarget
		card.ReasonText = c0.ReasonText
		card.IsDatabase = c0.IsDatabase
		card.DatabaseName = c0.DatabaseName
		card.RoleKind = c0.RoleKind
		card.Links = c0.Links
	case len(normalCards) > 0:
		n0 := normalCards[0]
		card.Group = "normal"
		card.IconURL = n0.IconURL
		card.SummaryCurrent = n0.SummaryCurrent
		card.SummaryTarget = n0.SummaryTarget
		card.ReasonText = n0.ReasonText
		card.Links = n0.Links
	case len(noUpdateCards) > 0:
		nc := noUpdateCards[0]
		card.Group = "no_update"
		card.IconURL = nc.IconURL
		card.Ignored = nc.Ignored
		card.IgnoreMode = nc.IgnoreMode
		card.IgnorePrefix = nc.IgnorePrefix
		card.IgnoreTargetTag = nc.IgnoreTargetTag
		card.SummaryCurrent = nc.SummaryCurrent
		card.SummaryTarget = nc.SummaryTarget
		card.ReasonText = nc.ReasonText
		card.IsDatabase = nc.IsDatabase
		card.DatabaseName = nc.DatabaseName
		card.RoleKind = nc.RoleKind
		card.Links = nc.Links
	}
	for _, nc := range normalCards {
		card.UpdateSvcKeys = append(card.UpdateSvcKeys, nc.UpdateSvcKeys...)
	}
	for _, cc := range cautionCards {
		card.UpdateSvcKeys = append(card.UpdateSvcKeys, cc.UpdateSvcKeys...)
	}
	return card, nil
}

// splitAppCards 将一个 App 下的服务按 (normal / caution / no_update) 切分为对应分组的卡片（附录 D.2）。
// 单容器项目始终保留项目名（避免显示内部 compose service 名如 app/web）；多容器项目下每个待更新组件独立成卡并标明所属项目。
func splitAppCards(base *AppCard) (normalCards, cautionCards, noUpdateCards []AppCard, upToDate bool) {
	var normalSvcs, cautionSvcs, ignoredSvcs []ServiceItem
	for _, svc := range base.Services {
		isUpd := svc.Kind == string(classify.KindDigestUpdate) || svc.Kind == string(classify.KindVersionUpdate)
		if svc.Ignored {
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

	isMultiSvcApp := len(base.Services) > 1

	makeCard := func(group string, svcs []ServiceItem) AppCard {
		primary := svcs[0]
		c := *base
		c.AppName = base.Name
		c.TotalServiceCount = len(base.Services)
		c.AllServices = append([]ServiceItem(nil), base.Services...)
		c.Services = append([]ServiceItem(nil), svcs...)
		if isMultiSvcApp && len(svcs) == 1 && svcs[0].Name != "main" {
			c.Name = svcs[0].Name
			c.ProjectName = base.Name
		} else {
			c.Name = base.Name
			if isMultiSvcApp {
				c.ProjectName = base.Name
			} else {
				c.ProjectName = ""
			}
		}
		c.Group = group
		c.IconURL = primary.IconURL
		c.SummaryCurrent = primary.CurrentVersion
		c.SummaryTarget = primary.TargetVersion
		c.ReasonText = primary.ReasonText
		c.DatabaseName = primary.DatabaseName
		c.RoleKind = primary.RoleKind
		c.IsDatabase = primary.DatabaseName != ""
		c.Links = primary.Links
		c.UpdateSvcKeys = nil
		return c
	}

	for _, svc := range normalSvcs {
		c := makeCard("normal", []ServiceItem{svc})
		c.UpdateSvcKeys = []string{svc.Key}
		normalCards = append(normalCards, c)
	}

	for _, svc := range cautionSvcs {
		c := makeCard("caution", []ServiceItem{svc})
		c.UpdateSvcKeys = []string{svc.Key}
		cautionCards = append(cautionCards, c)
	}

	if isMultiSvcApp {
		for _, svc := range ignoredSvcs {
			c := makeCard("no_update", []ServiceItem{svc})
			c.Ignored = true
			c.IgnoreMode = svc.IgnoreMode
			c.IgnorePrefix = svc.IgnorePrefix
			c.IgnoreTargetTag = svc.IgnoreTargetTag
			noUpdateCards = append(noUpdateCards, c)
		}
	} else if len(ignoredSvcs) > 0 {
		c := makeCard("no_update", ignoredSvcs)
		primary := ignoredSvcs[0]
		c.Ignored = true
		c.IgnoreMode = primary.IgnoreMode
		c.IgnorePrefix = primary.IgnorePrefix
		c.IgnoreTargetTag = primary.IgnoreTargetTag
		noUpdateCards = append(noUpdateCards, c)
	}

	hasFailed := false
	for _, svc := range base.Services {
		if svc.Kind == string(classify.KindCheckFailed) || svc.Kind == string(classify.KindUncomparable) {
			hasFailed = true
			break
		}
	}
	noActiveUpdates := len(normalSvcs) == 0 && len(cautionSvcs) == 0 && len(ignoredSvcs) == 0
	upToDate = noActiveUpdates && !hasFailed
	if noActiveUpdates && len(base.Services) > 0 {
		c := makeCard("no_update", base.Services)
		c.SummaryTarget = ""
		noUpdateCards = append(noUpdateCards, c)
	}
	return
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
