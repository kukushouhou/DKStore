package version

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/kukushouhou/DKStore/rules"
)

// Scheme 表示版本编号规则类别。
type Scheme string

const (
	SchemeUnknown Scheme = "unknown"
	SchemeSemVer  Scheme = "semver"
	SchemeCalVer  Scheme = "calver"
)

// ParsedTag 是对镜像标签的结构化解析结果（实施文档 §5.2 / §5.6）。
type ParsedTag struct {
	Raw        string
	Scheme     Scheme
	HasVPrefix bool
	Major      int
	Minor      int
	Patch      int
	Build      int    // 如 linuxserver 的 -ls123
	Precision  int    // 1="8", 2="8.0", 3="8.0.40"
	Variant    string // 归一化的变体后缀，如 "alpine"、"alpine3.20"、"bookworm"，不含前置 "-"
	Prerelease bool   // 是否包含 alpha/beta/rc 等预发布标记
}

var (
	// linuxserver 格式：1.2.3-ls45 或 v1.2.3-ls45 或 2024.10.1-ls12
	reLinuxServer = regexp.MustCompile(`^(?i)(v)?(\d+)\.(\d+)\.(\d+)[.-]ls(\d+)$`)
	// 通用数字版本前缀：v?1(.2)?(.3)?(-suffix)?
	reVersion = regexp.MustCompile(`^(?i)(v)?(\d+)(?:\.(\d+))?(?:\.(\d+))?(?:[-+_](.+))?$`)
)

// ParseTag 解析标签字符串。
func ParseTag(tag string) ParsedTag {
	raw := strings.TrimSpace(tag)
	if raw == "" {
		return ParsedTag{Raw: raw, Scheme: SchemeUnknown}
	}
	if rules.Default().IsRollingTag(raw) {
		return ParsedTag{Raw: raw, Scheme: SchemeUnknown}
	}

	if m := reLinuxServer.FindStringSubmatch(raw); m != nil {
		maj, _ := strconv.Atoi(m[2])
		min, _ := strconv.Atoi(m[3])
		pat, _ := strconv.Atoi(m[4])
		bld, _ := strconv.Atoi(m[5])
		scheme := SchemeSemVer
		if isCalVer(maj, min) {
			scheme = SchemeCalVer
		}
		return ParsedTag{
			Raw:        raw,
			Scheme:     scheme,
			HasVPrefix: m[1] != "",
			Major:      maj,
			Minor:      min,
			Patch:      pat,
			Build:      bld,
			Precision:  3,
			Variant:    "ls",
		}
	}

	m := reVersion.FindStringSubmatch(raw)
	if m == nil {
		return ParsedTag{Raw: raw, Scheme: SchemeUnknown}
	}
	maj, err := strconv.Atoi(m[2])
	if err != nil {
		return ParsedTag{Raw: raw, Scheme: SchemeUnknown}
	}
	prec := 1
	min, pat := 0, 0
	if m[3] != "" {
		min, err = strconv.Atoi(m[3])
		if err != nil {
			return ParsedTag{Raw: raw, Scheme: SchemeUnknown}
		}
		prec = 2
	}
	if m[4] != "" {
		pat, err = strconv.Atoi(m[4])
		if err != nil {
			return ParsedTag{Raw: raw, Scheme: SchemeUnknown}
		}
		prec = 3
	}
	// 单纯一个超大整数（如日期戳 20241001 或短 git sha 纯数字）不当作 SemVer 主版本
	if prec == 1 && maj > 1000 {
		return ParsedTag{Raw: raw, Scheme: SchemeUnknown}
	}

	suffix := strings.ToLower(m[5])
	prerelease, variant := classifySuffix(suffix)

	scheme := SchemeSemVer
	if prec >= 2 && isCalVer(maj, min) {
		scheme = SchemeCalVer
	}

	return ParsedTag{
		Raw:        raw,
		Scheme:     scheme,
		HasVPrefix: m[1] != "",
		Major:      maj,
		Minor:      min,
		Patch:      pat,
		Precision:  prec,
		Variant:    variant,
		Prerelease: prerelease,
	}
}

func isCalVer(major, minor int) bool {
	return major >= 2015 && major <= 2099 && minor >= 1 && minor <= 12
}

var rePrereleaseToken = regexp.MustCompile(`(?i)(^|[-._])(alpha|beta|rc|dev|preview|snapshot|nightly|canary|test)\d*($|[-._])`)

func classifySuffix(suffix string) (prerelease bool, variant string) {
	if suffix == "" {
		return false, ""
	}
	if rePrereleaseToken.MatchString(suffix) {
		return true, suffix
	}
	return false, suffix
}

// CoreVersion 返回不带 v 前缀和变体后缀的核心版本号，用于界面对比展示。
func (t ParsedTag) CoreVersion() string {
	switch t.Precision {
	case 1:
		return strconv.Itoa(t.Major)
	case 2:
		return strconv.Itoa(t.Major) + "." + strconv.Itoa(t.Minor)
	case 3:
		return strconv.Itoa(t.Major) + "." + strconv.Itoa(t.Minor) + "." + strconv.Itoa(t.Patch)
	default:
		return ""
	}
}

// Compare 比较两个已解析版本的大小：a < b 返回 -1，a == b 返回 0，a > b 返回 1。
func Compare(a, b ParsedTag) int {
	if a.Major != b.Major {
		return cmpInt(a.Major, b.Major)
	}
	if a.Minor != b.Minor {
		return cmpInt(a.Minor, b.Minor)
	}
	if a.Patch != b.Patch {
		return cmpInt(a.Patch, b.Patch)
	}
	return cmpInt(a.Build, b.Build)
}

func cmpInt(a, b int) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

// CompatibleCandidate 判断 candidate 标签是否可作为 current 标签的升级候选：
// 必须同 Scheme、同 Variant、同 Precision，且当 current 不是预发布时排除预发布。
func CompatibleCandidate(current, candidate ParsedTag) bool {
	if current.Scheme == SchemeUnknown || candidate.Scheme != current.Scheme {
		return false
	}
	if candidate.Precision != current.Precision {
		return false
	}
	if candidate.Variant != current.Variant {
		return false
	}
	if !current.Prerelease && candidate.Prerelease {
		return false
	}
	return true
}

// SameSeries 判断 candidate 与 current 是否处于同一发行系列。
// seriesMode="minor" 时要求 major.minor 相同（如 MySQL 8.0.x）；默认要求 major 相同。
func SameSeries(current, candidate ParsedTag, seriesMode string) bool {
	if seriesMode == "minor" {
		return current.Major == candidate.Major && current.Minor == candidate.Minor
	}
	return current.Major == candidate.Major
}

// SelectBestDisplayTag 从同一个 Digest 匹配到的多个标签中，选出最精细、且与 currentTag 变体一致的标签用于展示（实施文档 §4.4 / §5.5）。
func SelectBestDisplayTag(currentTag string, matchedTags []string) string {
	cur := ParseTag(currentTag)
	// 若当前标签本身有明确变体（如 latest-alpine 或 1.27-alpine），要求匹配的标签变体一致；当前是 latest 时要求无变体（或 ls）。
	wantVariant := cur.Variant
	if rules.Default().IsRollingTag(currentTag) {
		wantVariant = ""
	}

	var best ParsedTag
	hasBest := false
	for _, raw := range matchedTags {
		pt := ParseTag(raw)
		if pt.Scheme == SchemeUnknown || pt.Prerelease {
			continue
		}
		if pt.Variant != wantVariant && !(wantVariant == "" && pt.Variant == "ls") {
			continue
		}
		if !hasBest || betterDisplayTag(pt, best) {
			best = pt
			hasBest = true
		}
	}
	if !hasBest {
		return ""
	}
	return best.Raw
}

func betterDisplayTag(a, b ParsedTag) bool {
	if a.Precision != b.Precision {
		return a.Precision > b.Precision
	}
	return Compare(a, b) > 0
}
