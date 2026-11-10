package version

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/kukushouhou/DKStore/rules"
)

// Scheme 表示版本编号规则类别。
type Scheme string

const (
	SchemeUnknown Scheme = "unknown"
	SchemeSemVer  Scheme = "semver"
	SchemeCalVer  Scheme = "calver"
)

// ParsedTag 是对镜像标签的结构化解析结果（实施文档 §5.2 / §5.6 / 附录 D.1）。
type ParsedTag struct {
	Raw        string
	Scheme     Scheme
	HasVPrefix bool
	Segments   []int  // 任意 N 段点分数字版本号，如 [2]、[16,4]、[8,3,2]、[131,0,6778,85]
	Major      int    // 第 1 段（向后兼容）
	Minor      int    // 第 2 段（向后兼容）
	Patch      int    // 第 3 段（向后兼容）
	Build      int    // 如 linuxserver 的 -ls123
	Precision  int    // len(Segments)：1="8", 2="8.0", 3="8.0.40", 4="131.0.6778.85"
	Variant    string // 归一化的变体后缀，如 "alpine"、"alpine3.20"、"bookworm"，不含前置 "-"
	Prerelease bool   // 是否包含 alpha/beta/rc 等预发布标记
}

var (
	// linuxserver 格式：支持任意 >=2 段数字版本带 [.-]ls123，如 1.2.3-ls45、v131.0.6778.85-ls12、2024.10.1-ls12
	reLinuxServer = regexp.MustCompile(`^(?i)(v)?(\d+(?:\.\d+)+)[.-]ls(\d+)$`)
	// 通用 N 段数字版本前缀：v?N(.N)*(-suffix)?
	reVersion = regexp.MustCompile(`^(?i)(v)?(\d+(?:\.\d+)*)(?:[-+_](.+))?$`)
)

func parseNumericSegments(dotStr string) ([]int, bool) {
	parts := strings.Split(dotStr, ".")
	if len(parts) == 0 {
		return nil, false
	}
	segs := make([]int, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			return nil, false
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, false
		}
		segs = append(segs, n)
	}
	return segs, true
}

func populateLegacyFields(pt *ParsedTag) {
	pt.Precision = len(pt.Segments)
	if len(pt.Segments) >= 1 {
		pt.Major = pt.Segments[0]
	}
	if len(pt.Segments) >= 2 {
		pt.Minor = pt.Segments[1]
	}
	if len(pt.Segments) >= 3 {
		pt.Patch = pt.Segments[2]
	}
}

func segmentsOf(t ParsedTag) []int {
	if len(t.Segments) > 0 {
		return t.Segments
	}
	switch {
	case t.Precision >= 3:
		return []int{t.Major, t.Minor, t.Patch}
	case t.Precision == 2:
		return []int{t.Major, t.Minor}
	case t.Precision == 1:
		return []int{t.Major}
	default:
		return nil
	}
}

// ParseTag 解析标签字符串，支持任意多段数字版本号（1..N 段）及 -ls 构建号。
func ParseTag(tag string) ParsedTag {
	raw := strings.TrimSpace(tag)
	if raw == "" {
		return ParsedTag{Raw: raw, Scheme: SchemeUnknown}
	}
	if rules.Default().IsRollingTag(raw) {
		return ParsedTag{Raw: raw, Scheme: SchemeUnknown}
	}

	if m := reLinuxServer.FindStringSubmatch(raw); m != nil {
		segs, ok := parseNumericSegments(m[2])
		bld, bErr := strconv.Atoi(m[3])
		if ok && bErr == nil {
			pt := ParsedTag{
				Raw:        raw,
				Scheme:     SchemeSemVer,
				HasVPrefix: m[1] != "",
				Segments:   segs,
				Build:      bld,
				Variant:    "ls",
			}
			populateLegacyFields(&pt)
			if pt.Precision >= 2 && isCalVer(pt.Major, pt.Minor) {
				pt.Scheme = SchemeCalVer
			}
			return pt
		}
	}

	m := reVersion.FindStringSubmatch(raw)
	if m == nil {
		return ParsedTag{Raw: raw, Scheme: SchemeUnknown}
	}
	segs, ok := parseNumericSegments(m[2])
	if !ok {
		return ParsedTag{Raw: raw, Scheme: SchemeUnknown}
	}
	// 单纯一个超大整数（如日期戳 20241001、20261110-3 或短 git sha 纯数字）不当作 SemVer 主版本，保留给别名标签逻辑处理
	if len(segs) == 1 && segs[0] > 1000 {
		return ParsedTag{Raw: raw, Scheme: SchemeUnknown}
	}

	suffix := strings.ToLower(m[3])
	prerelease, variant := classifySuffix(suffix)

	pt := ParsedTag{
		Raw:        raw,
		Scheme:     SchemeSemVer,
		HasVPrefix: m[1] != "",
		Segments:   segs,
		Variant:    variant,
		Prerelease: prerelease,
	}
	populateLegacyFields(&pt)
	if pt.Precision >= 2 && isCalVer(pt.Major, pt.Minor) {
		pt.Scheme = SchemeCalVer
	}
	return pt
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

// CoreVersion 返回不带 v 前缀和变体后缀的核心点分数字版本号，用于界面对比展示与前缀匹配。
func (t ParsedTag) CoreVersion() string {
	segs := segmentsOf(t)
	if len(segs) == 0 {
		return ""
	}
	parts := make([]string, len(segs))
	for i, n := range segs {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ".")
}

// Compare 逐段比较两个已解析版本的大小：a < b 返回 -1，a == b 返回 0，a > b 返回 1。
func Compare(a, b ParsedTag) int {
	sa := segmentsOf(a)
	sb := segmentsOf(b)
	maxLen := len(sa)
	if len(sb) > maxLen {
		maxLen = len(sb)
	}
	for i := 0; i < maxLen; i++ {
		va, vb := 0, 0
		if i < len(sa) {
			va = sa[i]
		}
		if i < len(sb) {
			vb = sb[i]
		}
		if va != vb {
			return cmpInt(va, vb)
		}
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

// MatchesPrefix 判断已解析标签的前导数字段是否精确等于 prefix 的点分数字段（如 131.0.6778.108 匹配 "131"、"131.0"、"131.0.6778"）。
func MatchesPrefix(tag ParsedTag, prefix string) bool {
	if tag.Scheme == SchemeUnknown {
		return false
	}
	p := strings.TrimSpace(prefix)
	if strings.HasPrefix(p, "v") || strings.HasPrefix(p, "V") {
		p = p[1:]
	}
	p = strings.Trim(p, ".")
	if p == "" {
		return false
	}
	pSegs, ok := parseNumericSegments(p)
	if !ok {
		return false
	}
	tSegs := segmentsOf(tag)
	if len(pSegs) > len(tSegs) {
		return false
	}
	for i := range pSegs {
		if tSegs[i] != pSegs[i] {
			return false
		}
	}
	return true
}

// CompatibleCandidate 判断 candidate 标签是否可作为 current 标签的升级候选：
// 必须同 Scheme、同 Variant、同 Precision，且当 current 不是预发布时排除预发布。
func CompatibleCandidate(current, candidate ParsedTag) bool {
	if current.Scheme == SchemeUnknown || candidate.Scheme != current.Scheme {
		return false
	}
	if len(segmentsOf(candidate)) != len(segmentsOf(current)) {
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
// seriesMode="minor" 时要求前两段（major.minor）相同；默认要求第 1 段（major）相同。
func SameSeries(current, candidate ParsedTag, seriesMode string) bool {
	sa := segmentsOf(current)
	sb := segmentsOf(candidate)
	at := func(s []int, idx int) int {
		if idx < len(s) {
			return s[idx]
		}
		return 0
	}
	if seriesMode == "minor" {
		return at(sa, 0) == at(sb, 0) && at(sa, 1) == at(sb, 1)
	}
	return at(sa, 0) == at(sb, 0)
}

// SelectBestDisplayTag 从同一个 Digest 匹配到的多个标签中，选出最精细的标签用于展示（实施文档 §4.4 / §5.5 / 附录 D.1）。
// 优先选择非预发布的标准数字版本标签（Precision 最高、其次版本最高）；
// 若没有任何标准数字版本标签，则保留非滚动别名标签（如 20261110-3、build-512、sha-8a3f1c2），绝不丢弃真实版本别名。
func SelectBestDisplayTag(currentTag string, matchedTags []string) string {
	curTrim := strings.TrimSpace(currentTag)
	cur := ParseTag(curTrim)
	wantVariant := cur.Variant
	if rules.Default().IsRollingTag(curTrim) {
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
	if hasBest {
		return best.Raw
	}

	// 若未命中同变体的标准数字版本，且当前是滚动标签，放宽变体限制再尝试一次非预发布数字版本
	if rules.Default().IsRollingTag(curTrim) {
		for _, raw := range matchedTags {
			pt := ParseTag(raw)
			if pt.Scheme == SchemeUnknown || pt.Prerelease {
				continue
			}
			if !hasBest || betterDisplayTag(pt, best) {
				best = pt
				hasBest = true
			}
		}
		if hasBest {
			return best.Raw
		}
	}

	// 非标准数字版本兜底：挑选最佳非滚动别名标签（如 20261110-3、build-512、sha-8a3f1c2）
	bestAlias := ""
	for _, raw := range matchedTags {
		t := strings.TrimSpace(raw)
		if t == "" || strings.EqualFold(t, curTrim) || rules.Default().IsRollingTag(t) {
			continue
		}
		if bestAlias == "" || betterAliasTag(t, bestAlias) {
			bestAlias = t
		}
	}
	return bestAlias
}

func betterDisplayTag(a, b ParsedTag) bool {
	if a.Precision != b.Precision {
		return a.Precision > b.Precision
	}
	return Compare(a, b) > 0
}

func betterAliasTag(cand, cur string) bool {
	cArch, bArch := isArchOnlyAlias(cand), isArchOnlyAlias(cur)
	if cArch != bArch {
		return !cArch
	}
	cDigit, bDigit := containsDigit(cand), containsDigit(cur)
	if cDigit != bDigit {
		return cDigit
	}
	if len(cand) != len(cur) {
		return len(cand) > len(cur)
	}
	return cand > cur
}

func containsDigit(s string) bool {
	for _, r := range s {
		if unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

func isArchOnlyAlias(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "amd64", "x86_64", "arm64", "arm64v8", "aarch64", "armv7", "armhf", "386":
		return true
	default:
		return false
	}
}
