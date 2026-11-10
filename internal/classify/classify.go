// Package classify 实现服务级更新判定与风险分类（普通 vs 需谨慎，数据库/消息队列/有状态中间件保守规则，实施文档 §5.3 / §6 / 附录 D.1 / D.2）。
package classify

import (
	"strings"

	"github.com/kukushouhou/DKStore/internal/i18n"
	"github.com/kukushouhou/DKStore/internal/registry"
	"github.com/kukushouhou/DKStore/internal/version"
	"github.com/kukushouhou/DKStore/rules"
)

// Kind 表示单条 Finding 的检测结论类型。
type Kind string

const (
	KindNone          Kind = "none"           // 已是最新
	KindDigestUpdate  Kind = "digest_update"  // 当前标签指向的镜像内容已改变
	KindVersionUpdate Kind = "version_update" // 仓库发布了更高版本标签
	KindCheckFailed   Kind = "check_failed"   // 网络/认证/限流等原因无法检查
	KindUncomparable  Kind = "uncomparable"   // 本地构建或无 RepoDigest，无法比较
)

// Category 表示更新风险分组。
type Category string

const (
	CategoryNormal  Category = "normal"  // 可更新（默认展开）
	CategoryCaution Category = "caution" // 需谨慎更新
)

// Input 是判定单个 Service 更新情况所需的全部输入。
type Input struct {
	Ref                version.ParsedRef
	ServiceName        string
	LocalRepoDigest    string
	LocalImageID       string
	LocalVersionLabel  string // 本地容器/镜像 OCI 版本标签（org.opencontainers.image.version / org.label-schema.version）
	RemoteVersionLabel string // 远端 Manifest/镜像 OCI 版本标签
	RemoteManifest     *registry.ManifestResult
	CurrentMatchedTags []string
	RemoteMatchedTags  []string
	CandidateTags      []registry.TagDigestEntry
	HostPlatform       string
	// AllowImageIDCompare 仅在主机传输方式为群晖 dsm_api（拿不到 RepoDigest）时为 true，
	// 允许在无 RepoDigest 时用本地 ImageID 与远端对应平台的 ConfigDigest 比对。
	AllowImageIDCompare bool
	Lang                i18n.Lang
}

// Output 是分类器输出的 Finding 结果。
// Reason 与 Hint 保存语言无关的编码（供入库后按任意语言实时渲染），ReasonText 与 HintText 为按 Input.Lang 渲染好的本地化文本。
type Output struct {
	Kind           Kind           `json:"kind"`
	Category       Category       `json:"category,omitempty"`
	Reason         string         `json:"reason,omitempty"` // major_bump | database | database_suspected | database_cross_series | pinned_digest | republished
	ReasonText     string         `json:"reasonText,omitempty"`
	CurrentVersion string         `json:"currentVersion,omitempty"`
	TargetVersion  string         `json:"targetVersion,omitempty"`
	TargetRef      string         `json:"targetRef,omitempty"`
	TargetDigest   string         `json:"targetDigest,omitempty"`
	TargetTags     []string       `json:"targetTags,omitempty"`
	Hint           string         `json:"hint,omitempty"`
	Links          rules.RepoLink `json:"links"`
	IsDatabase     bool           `json:"isDatabase"`
	DatabaseName   string         `json:"databaseName,omitempty"`
	RoleKind       string         `json:"roleKind,omitempty"` // database | mq | middleware
}

// ExtractVersionLabel 从容器/镜像 Labels 中提取有效的版本字符串。
func ExtractVersionLabel(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	if v := ValidVersionLabel(labels["org.opencontainers.image.version"]); v != "" {
		return v
	}
	return ValidVersionLabel(labels["org.label-schema.version"])
}

// ValidVersionLabel 过滤掉无区分度的通用通道名（如 latest/dev/main），仅保留有意义的版本标签。
func ValidVersionLabel(raw string) string {
	v := strings.TrimSpace(raw)
	if v == "" {
		return ""
	}
	low := strings.ToLower(v)
	if strings.HasPrefix(low, "sha256:") {
		return ""
	}
	switch low {
	case "latest", "dev", "devel", "develop", "main", "master", "edge", "nightly", "stable", "release", "prod", "production", "unknown", "none", "n/a":
		return ""
	}
	if rules.Default().IsRollingTag(low) {
		return ""
	}
	return v
}

// plausibleVersionLabel 防止基础镜像（如 FROM node:17.1.0）残留的 OCI version 标签污染业务镜像版本展示：
// 当仓库已有候选标签时，仅当该标签在仓库候选标签中存在或至少存在同一主版本系列标签时才采信。
func plausibleVersionLabel(raw string, candidates []registry.TagDigestEntry) string {
	v := ValidVersionLabel(raw)
	if v == "" {
		return ""
	}
	if len(candidates) == 0 {
		return v
	}
	trimV := strings.TrimPrefix(strings.TrimPrefix(v, "v"), "V")
	pv := version.ParseTag(v)
	hasAnyParsedCandidate := false
	for _, c := range candidates {
		cn := strings.TrimSpace(c.Name)
		if strings.EqualFold(cn, v) || strings.EqualFold(strings.TrimPrefix(strings.TrimPrefix(cn, "v"), "V"), trimV) {
			return v
		}
		pc := version.ParseTag(cn)
		if pc.Scheme != version.SchemeUnknown && !pc.Prerelease {
			hasAnyParsedCandidate = true
			if pv.Scheme == pc.Scheme && pv.Major == pc.Major {
				return v
			}
		}
	}
	if !hasAnyParsedCandidate {
		return v
	}
	return ""
}

// InferStatefulRole 基于服务名称特征做非白名单有状态组件兜底识别（附录 D.2）。
func InferStatefulRole(serviceName string) (name, roleKind string, matched bool) {
	switch strings.ToLower(strings.TrimSpace(serviceName)) {
	case "db", "database":
		return "Database", "database", true
	case "mysql":
		return "MySQL", "database", true
	case "postgres", "postgresql":
		return "PostgreSQL", "database", true
	case "mariadb":
		return "MariaDB", "database", true
	case "redis":
		return "Redis", "database", true
	case "valkey":
		return "Valkey", "database", true
	case "mongo", "mongodb":
		return "MongoDB", "database", true
	case "mq", "mqtt", "broker":
		return "MQ", "mq", true
	case "mosquitto":
		return "Eclipse Mosquitto", "mq", true
	case "rabbitmq":
		return "RabbitMQ", "mq", true
	case "kafka":
		return "Kafka", "mq", true
	case "rocketmq":
		return "RocketMQ", "mq", true
	case "nats":
		return "NATS", "mq", true
	case "emqx":
		return "EMQX", "mq", true
	default:
		return "", "", false
	}
}

// Evaluate 根据实施文档 §5.3、§6 与附录 D.1/D.2 的规则计算 Finding。
func Evaluate(in Input) Output {
	lang := in.Lang
	if lang == "" {
		lang = i18n.ZhCN
	}
	ruleSet := rules.Default()
	dbRule, dbExact := ruleSet.MatchDatabase(in.Ref.Registry, in.Ref.Repository)
	if dbRule == nil {
		if heurName, heurKind, ok := InferStatefulRole(in.ServiceName); ok {
			dbRule = &rules.DatabaseRule{
				Name:   heurName,
				Kind:   heurKind,
				Series: "minor",
			}
			dbExact = false
		}
	}

	out := Output{
		Kind:       KindNone,
		IsDatabase: dbRule != nil,
	}
	if dbRule != nil {
		out.DatabaseName = dbRule.Name
		out.RoleKind = dbRule.Kind
		if out.RoleKind == "" {
			out.RoleKind = "database"
		}
	}

	// 1. 固定摘要 image@sha256:...
	if in.Ref.IsPinnedDigest() {
		out.Links = resolveLinks(ruleSet, in.Ref, dbRule, "")
		out.CurrentVersion = shortDigest(in.Ref.Digest)
		if in.RemoteManifest != nil && in.Ref.Tag != "" &&
			in.RemoteManifest.RepoDigest != "" && in.RemoteManifest.RepoDigest != in.Ref.Digest {
			out.Kind = KindDigestUpdate
			out.Category = CategoryCaution
			out.Reason = "pinned_digest"
			out.TargetVersion = in.Ref.Tag
			out.TargetRef = in.Ref.WithTag(in.Ref.Tag)
			out.TargetDigest = in.RemoteManifest.RepoDigest
			out.ReasonText = i18n.FormatReasonWithRole(lang, out.Reason, out.DatabaseName, out.RoleKind, out.CurrentVersion, out.TargetVersion)
		}
		return out
	}

	curTag := version.ParseTag(in.Ref.Tag)
	curDisplay := version.SelectBestDisplayTag(in.Ref.Tag, in.CurrentMatchedTags)
	if curDisplay == "" {
		curDisplay = plausibleVersionLabel(in.LocalVersionLabel, in.CandidateTags)
	}
	if curDisplay == "" && curTag.Scheme != version.SchemeUnknown {
		curDisplay = in.Ref.Tag
	}
	out.CurrentVersion = curDisplay
	if out.CurrentVersion == "" {
		out.CurrentVersion = in.Ref.Tag
	}

	// 2. 本地无 RepoDigest 时，只有来源为群晖 dsm_api（AllowImageIDCompare）且远端取到了对应平台的 ConfigDigest，
	//    才允许用 ImageID 与 ConfigDigest 比对；其他来源一律无法比较（审计 M2）
	localRepoDigest := registry.NormalizeDigest(in.LocalRepoDigest)
	localImageID := registry.NormalizeDigest(in.LocalImageID)
	var remoteRepoDigest, remotePlatformDigest, remoteConfigDigest, remoteManifestVer string
	if in.RemoteManifest != nil {
		remoteRepoDigest = registry.NormalizeDigest(in.RemoteManifest.RepoDigest)
		remotePlatformDigest = registry.NormalizeDigest(in.RemoteManifest.PlatformDigest)
		remoteConfigDigest = registry.NormalizeDigest(in.RemoteManifest.ConfigDigest)
		remoteManifestVer = in.RemoteManifest.VersionLabel
	}
	canCompareImageID := in.AllowImageIDCompare && localImageID != "" && remoteConfigDigest != ""
	if localRepoDigest == "" && !canCompareImageID {
		out.Kind = KindUncomparable
		out.Links = resolveLinks(ruleSet, in.Ref, dbRule, "")
		return out
	}

	// 3. 判断当前标签是否有 Digest 变化（含多架构平台级复核及纯 DSM WebAPI 下的 ConfigDigest/ImageID 比对）
	digestChanged := false
	if in.RemoteManifest != nil {
		if localRepoDigest != "" && remoteRepoDigest != "" {
			if remoteRepoDigest != localRepoDigest {
				samePlatform := (remotePlatformDigest != "" && remotePlatformDigest == localRepoDigest) ||
					(remoteConfigDigest != "" && localImageID != "" && remoteConfigDigest == localImageID)
				if !samePlatform {
					digestChanged = true
				}
			}
		} else if localRepoDigest == "" && canCompareImageID {
			if remoteConfigDigest != localImageID {
				digestChanged = true
			}
		}
	}

	// 4. 在候选标签中寻找同变体、同精度的更高版本（实施文档 §5.2-B + 附录 D.1 配置标签位数自动定界）
	var (
		latestInSeries  *version.ParsedTag
		latestInEntry   *registry.TagDigestEntry
		latestOverall   *version.ParsedTag
		latestOverEntry *registry.TagDigestEntry
	)
	seriesMode := "major"
	if dbRule != nil && dbRule.Series != "" {
		seriesMode = dbRule.Series
	}
	if curTag.Scheme != version.SchemeUnknown {
		// 附录 D.1：单段（如 :2 / :16）严格限定在该主版本前缀内；两段（如 :8.3 / :16.4）严格限定在该 major.minor 前缀内，绝不越界推荐 3.x / 9.x / 8.4
		scopePrefix := ""
		if curTag.Precision == 1 || curTag.Precision == 2 {
			scopePrefix = curTag.CoreVersion()
		}
		for i := range in.CandidateTags {
			cand := version.ParseTag(in.CandidateTags[i].Name)
			if !version.CompatibleCandidate(curTag, cand) {
				continue
			}
			if scopePrefix != "" && !version.MatchesPrefix(cand, scopePrefix) {
				continue
			}
			if version.Compare(cand, curTag) <= 0 {
				continue
			}
			if latestOverall == nil || version.Compare(cand, *latestOverall) > 0 {
				cCopy := cand
				latestOverall = &cCopy
				latestOverEntry = &in.CandidateTags[i]
			}
			if version.SameSeries(curTag, cand, seriesMode) {
				if latestInSeries == nil || version.Compare(cand, *latestInSeries) > 0 {
					cCopy := cand
					latestInSeries = &cCopy
					latestInEntry = &in.CandidateTags[i]
				}
			}
		}
	}

	localHash := firstNonEmpty(localRepoDigest, localImageID)
	remoteHash := firstNonEmpty(remoteRepoDigest, remotePlatformDigest, remoteConfigDigest)
	if localRepoDigest == "" && canCompareImageID {
		localHash = localImageID
		remoteHash = firstNonEmpty(remoteConfigDigest, remoteRepoDigest, remotePlatformDigest)
	}

	resolveRemoteBest := func() string {
		rb := version.SelectBestDisplayTag(in.Ref.Tag, in.RemoteMatchedTags)
		if rb == "" {
			rb = ValidVersionLabel(firstNonEmpty(in.RemoteVersionLabel, remoteManifestVer))
		}
		// 严禁出现起止相同的版本号（如 2 -> 2）或回退成浮动标签自身
		if rb != "" {
			if rb == curDisplay || (rb == in.Ref.Tag && (curDisplay == "" || curDisplay == in.Ref.Tag || curDisplay != in.Ref.Tag)) {
				rb = ""
			}
		}
		return rb
	}

	applyDigestVersions := func(remoteBest string) {
		if remoteBest != "" {
			if curDisplay != "" {
				out.CurrentVersion = curDisplay
			} else {
				out.CurrentVersion = formatTagWithHash(in.Ref.Tag, localHash)
			}
			out.TargetVersion = remoteBest
			return
		}
		if curDisplay == "" {
			out.CurrentVersion = formatTagWithHash(in.Ref.Tag, localHash)
			out.TargetVersion = formatTagWithHash(in.Ref.Tag, remoteHash)
		} else if curDisplay == in.Ref.Tag {
			out.CurrentVersion = formatTagWithHash(curDisplay, localHash)
			out.TargetVersion = formatTagWithHash(curDisplay, remoteHash)
		} else {
			out.CurrentVersion = curDisplay
			out.TargetVersion = formatTagWithHash(in.Ref.Tag, remoteHash)
		}
	}

	// 5. 有状态组件（数据库 / 消息队列 / 存储与检索中间件）保守规则（实施文档 §5 / §6.2 / 附录 D.2）
	if dbRule != nil {
		dbReason := "database"
		if !dbExact {
			dbReason = "database_suspected"
		}
		if latestInSeries != nil {
			out.Kind = KindVersionUpdate
			out.Category = CategoryCaution
			out.Reason = dbReason
			out.TargetVersion = latestInSeries.Raw
			out.TargetRef = in.Ref.WithTag(latestInSeries.Raw)
			if latestInEntry != nil {
				out.TargetDigest = latestInEntry.RepoDigest
			}
			out.TargetTags = []string{latestInSeries.Raw}
			if latestOverall != nil && !version.SameSeries(curTag, *latestOverall, seriesMode) {
				out.Hint = "db_new_series:" + latestOverall.Raw
			}
			out.ReasonText = i18n.FormatReasonWithRole(lang, out.Reason, out.DatabaseName, out.RoleKind, out.CurrentVersion, out.TargetVersion)
			out.Links = resolveLinks(ruleSet, in.Ref, dbRule, out.TargetVersion)
			return out
		}
		if digestChanged {
			out.Kind = KindDigestUpdate
			out.Category = CategoryCaution
			out.Reason = dbReason
			remoteBest := resolveRemoteBest()
			applyDigestVersions(remoteBest)
			out.TargetRef = in.Ref.WithTag(in.Ref.Tag)
			out.TargetDigest = firstNonEmpty(remoteRepoDigest, remoteConfigDigest)
			out.TargetTags = in.RemoteMatchedTags
			if curDisplay != "" && remoteBest != "" {
				pCur := version.ParseTag(curDisplay)
				pRem := version.ParseTag(remoteBest)
				if pCur.Scheme != version.SchemeUnknown && pRem.Scheme != version.SchemeUnknown &&
					!version.SameSeries(pCur, pRem, seriesMode) {
					out.Reason = "database_cross_series"
				}
			}
			out.ReasonText = i18n.FormatReasonWithRole(lang, out.Reason, out.DatabaseName, out.RoleKind, out.CurrentVersion, out.TargetVersion)
			out.Links = resolveLinks(ruleSet, in.Ref, dbRule, remoteBest)
			return out
		}
		if latestOverall != nil {
			out.Kind = KindNone
			out.Hint = "db_only_new_series:" + latestOverall.Raw
		}
		out.Links = resolveLinks(ruleSet, in.Ref, dbRule, "")
		return out
	}

	// 6. 普通应用：精确版本标签（>=3 段，含四段 Chrome/Plex 等）更新优先（实施文档 §5.3 / 附录 D.1）
	if curTag.Scheme != version.SchemeUnknown && curTag.Precision >= 3 && latestOverall != nil {
		out.Kind = KindVersionUpdate
		out.TargetVersion = latestOverall.Raw
		out.TargetRef = in.Ref.WithTag(latestOverall.Raw)
		if latestOverEntry != nil {
			out.TargetDigest = latestOverEntry.RepoDigest
		}
		out.TargetTags = []string{latestOverall.Raw}
		if isMajorBump(curTag, *latestOverall) {
			out.Category = CategoryCaution
			out.Reason = "major_bump"
			out.ReasonText = i18n.FormatReason(lang, out.Reason, "", in.Ref.Tag, latestOverall.Raw)
			if latestInSeries != nil {
				out.Hint = "in_series_update:" + latestInSeries.Raw
			}
		} else {
			out.Category = CategoryNormal
		}
		out.Links = resolveLinks(ruleSet, in.Ref, nil, out.TargetVersion)
		return out
	}

	// 7. 滚动标签（latest 等）、浮动版本（:2 / :8.3）或同版本重推的 Digest 变化
	if digestChanged {
		out.Kind = KindDigestUpdate
		out.Category = CategoryNormal
		remoteBest := resolveRemoteBest()
		applyDigestVersions(remoteBest)
		if remoteBest == "" && curTag.Scheme != version.SchemeUnknown && curTag.Precision >= 3 {
			out.Reason = "republished"
			out.ReasonText = i18n.FormatReason(lang, out.Reason, "", out.CurrentVersion, out.TargetVersion)
		}
		out.TargetRef = in.Ref.WithTag(in.Ref.Tag)
		out.TargetDigest = firstNonEmpty(remoteRepoDigest, remoteConfigDigest)
		out.TargetTags = in.RemoteMatchedTags
		if latestOverall != nil && latestOverall.Raw != in.Ref.Tag {
			out.Hint = "higher_tag:" + latestOverall.Raw
		}
		out.Links = resolveLinks(ruleSet, in.Ref, nil, remoteBest)
		return out
	}

	// 8. 其他同前缀内存在更高版本候选（如 8.3-ls14 -> 8.3-ls15）
	if curTag.Scheme != version.SchemeUnknown && latestOverall != nil {
		out.Kind = KindVersionUpdate
		out.TargetVersion = latestOverall.Raw
		out.TargetRef = in.Ref.WithTag(latestOverall.Raw)
		if latestOverEntry != nil {
			out.TargetDigest = latestOverEntry.RepoDigest
		}
		out.TargetTags = []string{latestOverall.Raw}
		if isMajorBump(curTag, *latestOverall) {
			out.Category = CategoryCaution
			out.Reason = "major_bump"
			out.ReasonText = i18n.FormatReason(lang, out.Reason, "", in.Ref.Tag, latestOverall.Raw)
		} else {
			out.Category = CategoryNormal
		}
		out.Links = resolveLinks(ruleSet, in.Ref, nil, out.TargetVersion)
		return out
	}

	out.Links = resolveLinks(ruleSet, in.Ref, nil, "")
	return out
}

func isMajorBump(cur, target version.ParsedTag) bool {
	if cur.Scheme != version.SchemeSemVer || target.Scheme != version.SchemeSemVer {
		return false
	}
	if cur.Major == 0 && target.Major == 0 {
		return target.Minor > cur.Minor
	}
	return target.Major > cur.Major
}

func resolveLinks(rs *rules.Set, ref version.ParsedRef, dbRule *rules.DatabaseRule, targetVer string) rules.RepoLink {
	link := rs.ResolveLinks(ref.Registry, ref.Repository, targetVer)
	if dbRule != nil && dbRule.Docs != "" && link.Releases == "" {
		link.Releases = dbRule.Docs
	}
	return link
}

func shortDigest(d string) string {
	if len(d) > 19 {
		return d[:19]
	}
	return d
}

func shortHash6(d string) string {
	s := strings.TrimSpace(d)
	if _, hexPart, ok := strings.Cut(s, ":"); ok {
		s = hexPart
	}
	if len(s) > 6 {
		return s[:6]
	}
	return s
}

func formatTagWithHash(tag, hash string) string {
	t := strings.TrimSpace(tag)
	if t == "" {
		t = "latest"
	}
	h := shortHash6(hash)
	if h == "" {
		return t
	}
	return t + " (" + h + ")"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
