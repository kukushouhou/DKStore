// Package classify 实现服务级更新判定与风险分类（普通 vs 需谨慎，数据库保守规则，实施文档 §5.3 / §6 / §10.4）。
package classify

import (
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
	CategoryCaution Category = "caution" // 需谨慎更新（默认折叠）
)

// Input 是判定单个 Service 更新情况所需的全部输入。
type Input struct {
	Ref                version.ParsedRef
	LocalRepoDigest    string
	LocalImageID       string
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
}

// Evaluate 根据实施文档 §5.3 与 §6 的规则计算 Finding。
func Evaluate(in Input) Output {
	lang := in.Lang
	if lang == "" {
		lang = i18n.ZhCN
	}
	ruleSet := rules.Default()
	dbRule, dbExact := ruleSet.MatchDatabase(in.Ref.Registry, in.Ref.Repository)
	out := Output{
		Kind:       KindNone,
		IsDatabase: dbRule != nil,
	}
	if dbRule != nil {
		out.DatabaseName = dbRule.Name
	}

	// 1. 固定摘要 image@sha256:...
	if in.Ref.IsPinnedDigest() {
		out.Links = resolveLinks(ruleSet, in.Ref, dbRule, "")
		if in.RemoteManifest != nil && in.Ref.Tag != "" &&
			in.RemoteManifest.RepoDigest != "" && in.RemoteManifest.RepoDigest != in.Ref.Digest {
			out.Kind = KindDigestUpdate
			out.Category = CategoryCaution
			out.Reason = "pinned_digest"
			out.CurrentVersion = shortDigest(in.Ref.Digest)
			out.TargetVersion = in.Ref.Tag
			out.TargetRef = in.Ref.WithTag(in.Ref.Tag)
			out.TargetDigest = in.RemoteManifest.RepoDigest
			out.ReasonText = i18n.FormatReason(lang, out.Reason, out.DatabaseName, out.CurrentVersion, out.TargetVersion)
		}
		return out
	}

	// 2. 本地无 RepoDigest 时，只有来源为群晖 dsm_api（AllowImageIDCompare）且远端取到了对应平台的 ConfigDigest，
	//    才允许用 ImageID 与 ConfigDigest 比对；其他来源（本地构建镜像、docker load 导入等）一律无法比较（审计 M2）
	localRepoDigest := registry.NormalizeDigest(in.LocalRepoDigest)
	localImageID := registry.NormalizeDigest(in.LocalImageID)
	var remoteRepoDigest, remotePlatformDigest, remoteConfigDigest string
	if in.RemoteManifest != nil {
		remoteRepoDigest = registry.NormalizeDigest(in.RemoteManifest.RepoDigest)
		remotePlatformDigest = registry.NormalizeDigest(in.RemoteManifest.PlatformDigest)
		remoteConfigDigest = registry.NormalizeDigest(in.RemoteManifest.ConfigDigest)
	}
	canCompareImageID := in.AllowImageIDCompare && localImageID != "" && remoteConfigDigest != ""
	if localRepoDigest == "" && !canCompareImageID {
		out.Kind = KindUncomparable
		out.Links = resolveLinks(ruleSet, in.Ref, dbRule, "")
		return out
	}

	// 3. 判断当前标签是否有 Digest 变化（含多架构平台级复核及纯 DSM WebAPI 下的 ConfigDigest/ImageID 比对，实施文档 §5.2-A）
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

	curTag := version.ParseTag(in.Ref.Tag)
	curDisplay := version.SelectBestDisplayTag(in.Ref.Tag, in.CurrentMatchedTags)
	if curDisplay == "" && curTag.Scheme != version.SchemeUnknown {
		curDisplay = in.Ref.Tag
	}
	out.CurrentVersion = curDisplay

	// 4. 在候选标签中寻找同变体、同精度的更高版本（实施文档 §5.2-B）
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
		for i := range in.CandidateTags {
			cand := version.ParseTag(in.CandidateTags[i].Name)
			if !version.CompatibleCandidate(curTag, cand) {
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

	// 5. 数据库专用保守规则（实施文档 §5 / §6.2）
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
			out.ReasonText = i18n.FormatReason(lang, out.Reason, out.DatabaseName, out.CurrentVersion, out.TargetVersion)
			out.Links = resolveLinks(ruleSet, in.Ref, dbRule, out.TargetVersion)
			return out
		}
		if digestChanged {
			out.Kind = KindDigestUpdate
			out.Category = CategoryCaution
			out.Reason = dbReason
			remoteBest := version.SelectBestDisplayTag(in.Ref.Tag, in.RemoteMatchedTags)
			if remoteBest == in.Ref.Tag && curDisplay != "" && curDisplay != in.Ref.Tag {
				remoteBest = ""
			}
			if remoteBest != "" {
				out.TargetVersion = remoteBest
			} else {
				out.TargetVersion = "@new_digest"
			}
			out.TargetRef = in.Ref.WithTag(in.Ref.Tag)
			out.TargetDigest = remoteRepoDigest
			out.TargetTags = in.RemoteMatchedTags
			if curDisplay != "" && remoteBest != "" {
				pCur := version.ParseTag(curDisplay)
				pRem := version.ParseTag(remoteBest)
				if pCur.Scheme != version.SchemeUnknown && pRem.Scheme != version.SchemeUnknown &&
					!version.SameSeries(pCur, pRem, seriesMode) {
					out.Reason = "database_cross_series"
				}
			}
			out.ReasonText = i18n.FormatReason(lang, out.Reason, out.DatabaseName, curDisplay, remoteBest)
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

	// 6. 普通应用：版本标签更新优先（实施文档 §5.3）
	if curTag.Scheme != version.SchemeUnknown && curTag.Precision == 3 && latestOverall != nil {
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

	// 7. 滚动标签（latest 等）、浮动版本（1.2 / 1）或同版本重推的 Digest 变化
	if digestChanged {
		out.Kind = KindDigestUpdate
		out.Category = CategoryNormal
		remoteBest := version.SelectBestDisplayTag(in.Ref.Tag, in.RemoteMatchedTags)
		if remoteBest == in.Ref.Tag && curDisplay != "" && curDisplay != in.Ref.Tag {
			remoteBest = ""
		}
		if remoteBest != "" {
			out.TargetVersion = remoteBest
		} else if curTag.Scheme != version.SchemeUnknown && curTag.Precision == 3 {
			out.TargetVersion = in.Ref.Tag + "@republished"
			out.Reason = "republished"
			out.ReasonText = i18n.FormatReason(lang, out.Reason, "", in.Ref.Tag, in.Ref.Tag)
		} else {
			out.TargetVersion = "@new_digest"
		}
		out.TargetRef = in.Ref.WithTag(in.Ref.Tag)
		out.TargetDigest = remoteRepoDigest
		out.TargetTags = in.RemoteMatchedTags
		if latestOverall != nil && latestOverall.Raw != in.Ref.Tag {
			out.Hint = "higher_tag:" + latestOverall.Raw
		}
		out.Links = resolveLinks(ruleSet, in.Ref, nil, remoteBest)
		return out
	}

	// 8. 浮动标签（如 1.27）自身 Digest 未变，但存在更高的浮动标签（如 1.28 或 2.0）
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
