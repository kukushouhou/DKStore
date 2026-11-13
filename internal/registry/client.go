// Package registry 实现 OCI/Docker Registry 只读访问、Bearer 鉴权、0 配额损耗检测、多架构 Manifest 复核、
// 有限窗口标签查询、按需标签搜索与 Digest/ConfigDigest 反查版本号（实施文档 §5 / 附录 D.1 / D.3）。
package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/kukushouhou/DKStore/internal/i18n"
	"github.com/kukushouhou/DKStore/internal/secret"
	"github.com/kukushouhou/DKStore/internal/store"
	"github.com/kukushouhou/DKStore/internal/version"
	"github.com/kukushouhou/DKStore/rules"
)

// ErrorKind 表示标准化的 Registry 错误类别（实施文档 §5.7）。
type ErrorKind string

const (
	ErrNetwork      ErrorKind = "network"
	ErrAuthRequired ErrorKind = "auth_required"
	ErrNotFound     ErrorKind = "not_found"
	ErrRateLimited  ErrorKind = "rate_limited"
	ErrRegistry     ErrorKind = "registry_error"
)

// RegistryError 携带可向用户展示的分类与原因。
type RegistryError struct {
	Kind    ErrorKind
	Message string
}

// Error 返回默认语言（zh-CN）文案；Message 可能是 i18n.EngineDetail 编码，展示时应使用 i18n.LocalizeDetail 按请求语言还原。
func (e *RegistryError) Error() string { return i18n.LocalizeDetail(i18n.ZhCN, e.Message) }

// ManifestResult 是对远端 tag 的 Manifest/Index 检查结果。
type ManifestResult struct {
	RepoDigest     string `json:"repoDigest,omitempty"`     // 顶层 Docker-Content-Digest（Index 或单架构 Manifest）
	PlatformDigest string `json:"platformDigest,omitempty"` // 对应主机 os/arch/variant 的具体 Manifest Digest（若顶层是 Index）
	ConfigDigest   string `json:"configDigest,omitempty"`   // 对应主机架构的镜像 Config Digest（可与本地 ImageInspect.Id 比对）
	VersionLabel   string `json:"versionLabel,omitempty"`   // OCI Manifest/Index annotations 中的版本号
}

// ManifestCheckOptions 控制 CheckManifestEx 的按需请求行为（避免消耗不必要的拉取配额）。
type ManifestCheckOptions struct {
	HostPlatform    string
	BypassCache     bool
	LocalRepoDigest string // 本地已知的 RepoDigest；存在时优先走 HEAD 比对免 GET
	NeedConfig      bool   // 本地缺少 RepoDigest（如群晖 dsm_api 模式）需要比对 ConfigDigest 时设为 true
}

// TagDigestEntry 表示仓库中一个标签及其关联的 Digest 与更新时间。
type TagDigestEntry struct {
	Name            string            `json:"name"`
	RepoDigest      string            `json:"repoDigest,omitempty"`
	ConfigDigest    string            `json:"configDigest,omitempty"`
	PlatformDigests map[string]string `json:"platformDigests,omitempty"` // key: "linux/amd64" 或 "linux/arm/v7"
	UpdatedAt       int64             `json:"updatedAt,omitempty"`       // Unix 秒（来自 Docker Hub last_updated）
}

// Creds 是私有仓库的用户名与密码/Token。
type Creds struct {
	Registry string `json:"registry"`
	Username string `json:"username"`
	Secret   string `json:"secret,omitempty"`
}

// Client 访问各大镜像仓库并利用 SQLite 做缓存。
type Client struct {
	st         *store.Store
	box        *secret.Box
	httpClient *http.Client
	// BaseURLOverride 仅供单元测试把域名（如 docker.io / hub.docker.com / github.com）指向 httptest.Server。
	BaseURLOverride map[string]string

	tokenMu sync.Mutex
	tokens  map[string]cachedToken
}

type cachedToken struct {
	token     string
	expiresAt time.Time
}

const (
	acceptManifestHeaders = "application/vnd.oci.image.index.v1+json, " +
		"application/vnd.docker.distribution.manifest.list.v2+json, " +
		"application/vnd.oci.image.manifest.v1+json, " +
		"application/vnd.docker.distribution.manifest.v2+json"
)

// New 创建 Registry 客户端。
func New(st *store.Store, box *secret.Box) *Client {
	return &Client{
		st:  st,
		box: box,
		httpClient: &http.Client{
			Timeout: 20 * time.Second,
		},
		tokens: make(map[string]cachedToken),
	}
}

// CheckManifest 查询指定镜像引用（tag）在远端的最新 Digest（兼容简化调用签名）。
func (c *Client) CheckManifest(ctx context.Context, ref version.ParsedRef, hostPlatform string, bypassCache bool) (*ManifestResult, error) {
	return c.CheckManifestEx(ctx, ref, ManifestCheckOptions{
		HostPlatform: hostPlatform,
		BypassCache:  bypassCache,
	})
}

// CheckManifestEx 执行 0 配额优先的 Manifest 检查（附录 D.1）：
// 1. Docker Hub 标签优先调用 hub.docker.com/v2/repositories/.../tags/{tag}（0 Registry 拉取配额，同时拿到 RepoDigest 与各架构 PlatformDigest）；
//    仅当调用方明确需要 ConfigDigest（NeedConfig）或网页端接口不可用/未命中（如私有镜像）时，才回退到 Registry v2。
// 2. Registry v2 在本地已有 RepoDigest 且无需 ConfigDigest 时优先发免费的 HEAD 请求核对 Docker-Content-Digest，一致则直接返回，不消耗拉取配额。
func (c *Client) CheckManifestEx(ctx context.Context, ref version.ParsedRef, opts ManifestCheckOptions) (*ManifestResult, error) {
	tag := ref.Tag
	if tag == "" {
		tag = ref.Digest
	}
	repoKey := ref.Registry + "/" + ref.Repository
	cacheKey := fmt.Sprintf("manifest:%s/%s:%s@%s", ref.Registry, ref.Repository, tag, opts.HostPlatform)
	if !opts.BypassCache {
		if b, ok := c.getCache(ctx, cacheKey); ok {
			var res ManifestResult
			if json.Unmarshal(b, &res) == nil && res.RepoDigest != "" && (!opts.NeedConfig || res.ConfigDigest != "") {
				return &res, nil
			}
		}
	}

	localDig := NormalizeDigest(opts.LocalRepoDigest)
	var hubFallback *ManifestResult

	// 1. Docker Hub 镜像优先走网页端 Tags 接口（0 拉取配额）
	if ref.Registry == "docker.io" && !strings.HasPrefix(tag, "sha256:") {
		if fbRes, fbErr := c.fetchDockerHubTagManifest(ctx, ref.Repository, tag, opts.HostPlatform); fbErr == nil && fbRes.RepoDigest != "" {
			hubFallback = fbRes
			sameAsLocal := localDig != "" && (fbRes.RepoDigest == localDig || (fbRes.PlatformDigest != "" && fbRes.PlatformDigest == localDig))
			if !opts.NeedConfig || sameAsLocal {
				if b, mErr := json.Marshal(fbRes); mErr == nil {
					c.putCache(ctx, cacheKey, b, 30*time.Minute)
				}
				return fbRes, nil
			}
		}
	}

	baseURL := c.registryAPIBase(ref.Registry)
	manifestURL := fmt.Sprintf("%s/v2/%s/manifests/%s", baseURL, ref.Repository, url.PathEscape(tag))

	// 2. 当本地已有 RepoDigest 且无需强制获取 ConfigDigest 时，先发 HEAD 免费核对 Docker-Content-Digest
	if localDig != "" && !opts.NeedConfig {
		hResp, _, hErr := c.doRegistryRequest(ctx, ref.Registry, ref.Repository, http.MethodHead, manifestURL, acceptManifestHeaders)
		if hErr == nil {
			headDig := NormalizeDigest(hResp.Header.Get("Docker-Content-Digest"))
			if headDig != "" && headDig == localDig {
				res := &ManifestResult{RepoDigest: headDig}
				if b, mErr := json.Marshal(res); mErr == nil {
					c.putCache(ctx, cacheKey, b, 30*time.Minute)
				}
				return res, nil
			}
		} else {
			var re *RegistryError
			if errors.As(hErr, &re) && (re.Kind == ErrNotFound || re.Kind == ErrRateLimited || re.Kind == ErrAuthRequired) {
				if hubFallback != nil {
					return hubFallback, nil
				}
				return nil, hErr
			}
		}
	}

	// 3. 发 GET 获取完整 Manifest/Index（必要时进一步解析对应架构的 PlatformDigest 与 ConfigDigest）
	resp, body, err := c.doRegistryRequest(ctx, ref.Registry, ref.Repository, http.MethodGet, manifestURL, acceptManifestHeaders)
	if err != nil {
		if hubFallback != nil {
			if b, mErr := json.Marshal(hubFallback); mErr == nil {
				c.putCache(ctx, cacheKey, b, 30*time.Minute)
			}
			return hubFallback, nil
		}
		if ref.Registry == "docker.io" && !strings.HasPrefix(tag, "sha256:") {
			if fbRes, fbErr := c.fetchDockerHubTagManifest(ctx, ref.Repository, tag, opts.HostPlatform); fbErr == nil && fbRes.RepoDigest != "" {
				if b, mErr := json.Marshal(fbRes); mErr == nil {
					c.putCache(ctx, cacheKey, b, 30*time.Minute)
				}
				return fbRes, nil
			}
		}
		return nil, err
	}

	res := &ManifestResult{
		RepoDigest:   NormalizeDigest(resp.Header.Get("Docker-Content-Digest")),
		VersionLabel: parseAnnotationVersion(body),
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))

	if strings.Contains(ct, "manifest.list") || strings.Contains(ct, "image.index") {
		var idx struct {
			Manifests []struct {
				Digest      string            `json:"digest"`
				Annotations map[string]string `json:"annotations"`
				Platform    struct {
					OS      string `json:"os"`
					Arch    string `json:"architecture"`
					Variant string `json:"variant"`
				} `json:"platform"`
			} `json:"manifests"`
		}
		// 平台未知时跳过平台级比对（不默认 amd64，审计 M2/H5）
		if opts.HostPlatform != "" && json.Unmarshal(body, &idx) == nil {
			plats := make([]string, len(idx.Manifests))
			for i, m := range idx.Manifests {
				plats[i] = formatPlatform(m.Platform.OS, m.Platform.Arch, m.Platform.Variant)
			}
			if i := selectPlatformIndex(opts.HostPlatform, plats); i >= 0 {
				res.PlatformDigest = NormalizeDigest(idx.Manifests[i].Digest)
				if res.VersionLabel == "" {
					res.VersionLabel = pickAnnotationVersion(idx.Manifests[i].Annotations)
				}
			}
		}
		// 多架构 Index：仅在平台摘要与本地摘要不同或明确需要 ConfigDigest 时拉取平台子 Manifest
		samePlat := localDig != "" && res.PlatformDigest != "" && res.PlatformDigest == localDig
		if res.PlatformDigest != "" && (!samePlat || opts.NeedConfig) {
			pURL := fmt.Sprintf("%s/v2/%s/manifests/%s", baseURL, ref.Repository, res.PlatformDigest)
			if _, pBody, pErr := c.doRegistryRequest(ctx, ref.Registry, ref.Repository, http.MethodGet, pURL, acceptManifestHeaders); pErr == nil {
				res.ConfigDigest = parseConfigDigest(pBody)
				if res.VersionLabel == "" {
					res.VersionLabel = parseAnnotationVersion(pBody)
				}
			}
		}
	} else {
		res.ConfigDigest = parseConfigDigest(body)
	}

	if res.ConfigDigest != "" && tag != "" && !strings.HasPrefix(tag, "sha256:") {
		c.mergeDigestTagCache(ctx, repoKey, res.ConfigDigest, tag)
	}
	if res.RepoDigest != "" {
		if b, err := json.Marshal(res); err == nil {
			c.putCache(ctx, cacheKey, b, 30*time.Minute)
		}
	}
	return res, nil
}

// FetchRecentTags 按实施文档 §5.4 的有限策略获取候选标签列表（含可用 Digest 与更新时间）。
// Docker Hub 最多拉取 maxPages（默认 3，每页 100）按更新时间倒序的标签。
func (c *Client) FetchRecentTags(ctx context.Context, ref version.ParsedRef, maxPages int, bypassCache bool) ([]TagDigestEntry, error) {
	if maxPages <= 0 {
		maxPages = 3
	}
	if maxPages > 5 {
		maxPages = 5
	}
	cacheKey := fmt.Sprintf("tags:%s/%s", ref.Registry, ref.Repository)
	if !bypassCache {
		if b, ok := c.getCache(ctx, cacheKey); ok {
			var list []TagDigestEntry
			if json.Unmarshal(b, &list) == nil {
				return list, nil
			}
		}
	}

	var (
		entries []TagDigestEntry
		err     error
	)
	if ref.Registry == "docker.io" {
		entries, err = c.fetchDockerHubTags(ctx, ref.Repository, maxPages)
	} else {
		entries, err = c.fetchOCITagsList(ctx, ref.Registry, ref.Repository, 5)
	}
	if err != nil {
		return nil, err
	}
	if b, err := json.Marshal(entries); err == nil {
		c.putCache(ctx, cacheKey, b, 6*time.Hour)
	}
	return entries, nil
}

func mergeUniqueTagEntries(groups ...[]TagDigestEntry) []TagDigestEntry {
	total := 0
	for _, g := range groups {
		total += len(g)
	}
	seen := make(map[string]int, total)
	out := make([]TagDigestEntry, 0, total)
	for _, g := range groups {
		for _, e := range g {
			if e.Name == "" {
				continue
			}
			if idx, exists := seen[e.Name]; exists {
				if out[idx].RepoDigest == "" && e.RepoDigest != "" {
					out[idx].RepoDigest = e.RepoDigest
				}
				if out[idx].UpdatedAt == 0 && e.UpdatedAt > 0 {
					out[idx].UpdatedAt = e.UpdatedAt
				}
				continue
			}
			seen[e.Name] = len(out)
			out = append(out, e)
		}
	}
	return out
}

// SearchTags 支持弹窗内版本展示（query=""）与按需检索（query!=""），
// 默认按「当前标签系列优先 + 语义化版本号从高到低 + 同变体过滤」输出最多 20 条干净版本，并返回作用域内最高版本标签 highestTag。
func (c *Client) SearchTags(ctx context.Context, ref version.ParsedRef, query string, remote bool) ([]TagDigestEntry, string, error) {
	q := strings.TrimSpace(query)
	qLower := strings.ToLower(q)

	recent, err := c.FetchRecentTags(ctx, ref, 5, false)
	if err != nil && (!remote || q == "" || ref.Registry != "docker.io") {
		return nil, "", err
	}

	combined := recent
	curTag := version.ParseTag(ref.Tag)
	scopePrefix := computeServiceScopePrefix(ref)

	if ref.Registry == "docker.io" {
		if remote && q != "" {
			remoteHits, rErr := c.searchDockerHubTagsByNameWithSize(ctx, ref.Repository, q, 50)
			if rErr != nil && len(recent) == 0 {
				return nil, "", rErr
			}
			if len(remoteHits) > 0 {
				combined = mergeUniqueTagEntries(remoteHits, combined)
			}
		} else if q == "" && curTag.Scheme != version.SchemeUnknown {
			// 对 mysql:8.4 / postgres:17 / registry:2 等多变体或长周期大仓库，补充拉取本系列标签缓存，防止正式版本被海量变体挤出前几页
			seriesQuery := scopePrefix
			if seriesQuery == "" {
				seriesQuery = strconv.Itoa(curTag.Major)
			}
			if curTag.Precision == 1 {
				seriesQuery = curTag.CoreVersion() + "."
			}
			if seriesQuery != "" {
				if seriesHits, sErr := c.fetchCachedHubTagsByName(ctx, ref.Repository, seriesQuery, 100); sErr == nil && len(seriesHits) > 0 {
					combined = mergeUniqueTagEntries(combined, seriesHits)
				}
			}
			if ref.Tag != "" && !containsTagEntry(combined, ref.Tag) {
				if single, tErr := c.fetchCachedHubSingleTagEntry(ctx, ref.Repository, ref.Tag); tErr == nil && single.Name != "" && single.UpdatedAt > 0 {
					combined = mergeUniqueTagEntries([]TagDigestEntry{single}, combined)
				}
			}
		}
	} else if remote && q != "" && len(combined) == 0 {
		list, lErr := c.fetchOCITagsList(ctx, ref.Registry, ref.Repository, 5)
		if lErr != nil {
			return nil, "", lErr
		}
		combined = list
	}

	cleanedDefault := buildCleanDefaultTags(combined, ref, scopePrefix)
	highest := pickScopedHighestTag(cleanedDefault, combined, ref, scopePrefix)

	if qLower != "" {
		matched := make([]TagDigestEntry, 0, len(combined))
		for _, e := range combined {
			if e.Name != "" && strings.Contains(strings.ToLower(e.Name), qLower) {
				matched = append(matched, e)
			}
		}
		sortSearchedTags(matched, qLower)
		if len(matched) > 20 {
			matched = matched[:20]
		}
		return matched, highest, nil
	}

	if len(cleanedDefault) > 20 {
		cleanedDefault = cleanedDefault[:20]
	}
	return cleanedDefault, highest, nil
}

func containsTagEntry(entries []TagDigestEntry, name string) bool {
	for _, e := range entries {
		if e.Name == name {
			return true
		}
	}
	return false
}

func computeServiceScopePrefix(ref version.ParsedRef) string {
	curTag := version.ParseTag(ref.Tag)
	if curTag.Scheme == version.SchemeUnknown {
		return ""
	}
	if curTag.Precision == 1 || curTag.Precision == 2 {
		return curTag.CoreVersion()
	}
	if dbRule, _ := rules.Default().MatchDatabase(ref.Registry, ref.Repository); dbRule != nil {
		if dbRule.Series == "minor" && curTag.Precision >= 2 {
			return fmt.Sprintf("%d.%d", curTag.Major, curTag.Minor)
		}
		return strconv.Itoa(curTag.Major)
	}
	return ""
}

func classifyTagVariant(variant string) (flavor string, isArchOrWin bool) {
	v := strings.TrimSpace(strings.ToLower(variant))
	if v == "" || v == "ls" {
		return "", false
	}
	tokens := strings.FieldsFunc(v, func(r rune) bool {
		return r == '-' || r == '_'
	})
	flavorSet := make(map[string]bool)
	for _, tok := range tokens {
		if tok == "" {
			continue
		}
		switch {
		case tok == "amd64" || tok == "x86_64" || tok == "x64" ||
			tok == "arm64" || tok == "arm64v8" || tok == "aarch64" ||
			tok == "arm" || tok == "armv6" || tok == "armv7" || tok == "arm32v7" || tok == "arm32v6" || tok == "armhf" ||
			tok == "386" || tok == "i386" || tok == "ppc64le" || tok == "s390x" || tok == "riscv64" || tok == "mips64le" || tok == "loong64" ||
			strings.HasPrefix(tok, "windowsservercore") || strings.HasPrefix(tok, "nanoserver") ||
			strings.HasPrefix(tok, "ltsc20") || tok == "windows" || tok == "win":
			isArchOrWin = true
		case strings.HasPrefix(tok, "alpine"):
			flavorSet["alpine"] = true
		case strings.HasPrefix(tok, "slim"):
			flavorSet["slim"] = true
		case tok == "bookworm" || tok == "bullseye" || tok == "buster" || tok == "trixie" || tok == "stretch" || tok == "jessie" || strings.HasPrefix(tok, "debian"):
			flavorSet["debian"] = true
		case tok == "noble" || tok == "jammy" || tok == "focal" || tok == "bionic" || strings.HasPrefix(tok, "ubuntu"):
			flavorSet["ubuntu"] = true
		case strings.HasPrefix(tok, "oracle"):
			flavorSet["oracle"] = true
		case strings.HasPrefix(tok, "ubi") || strings.HasPrefix(tok, "centos") || strings.HasPrefix(tok, "rocky") || strings.HasPrefix(tok, "alma") || strings.HasPrefix(tok, "fedora"):
			flavorSet["rhel"] = true
		case strings.HasPrefix(tok, "openssl"):
			flavorSet["openssl"] = true
		case strings.HasPrefix(tok, "distroless"):
			flavorSet["distroless"] = true
		case strings.HasPrefix(tok, "busybox"):
			flavorSet["busybox"] = true
		case strings.HasPrefix(tok, "fips"):
			flavorSet["fips"] = true
		case strings.HasPrefix(tok, "jmx"):
			flavorSet["jmx"] = true
		case strings.HasPrefix(tok, "management"):
			flavorSet["management"] = true
		}
	}
	if len(flavorSet) == 0 {
		return "", isArchOrWin
	}
	flavors := make([]string, 0, len(flavorSet))
	for k := range flavorSet {
		flavors = append(flavors, k)
	}
	sort.Strings(flavors)
	return strings.Join(flavors, "+"), isArchOrWin
}

func buildCleanDefaultTags(entries []TagDigestEntry, ref version.ParsedRef, scopePrefix string) []TagDigestEntry {
	if len(entries) == 0 {
		return nil
	}
	curTagName := strings.TrimSpace(ref.Tag)
	curParsed := version.ParseTag(curTagName)
	curFlavor, _ := classifyTagVariant(curParsed.Variant)
	if curParsed.Scheme == version.SchemeUnknown {
		curFlavor, _ = classifyTagVariant(curTagName)
	}
	allowedFlavors := map[string]bool{curFlavor: true}

	// 若当前标签（或 latest）指向的镜像 Digest 没有无后缀标准版本，而是官方将某个变体（如 mosquitto:2 -> 2.1.2-alpine）作为默认主线，则自动纳入该同 Digest 变体
	var anchorDigest string
	for _, e := range entries {
		if e.Name == curTagName && e.RepoDigest != "" {
			anchorDigest = e.RepoDigest
			break
		}
	}
	if anchorDigest == "" {
		for _, e := range entries {
			if e.Name == "latest" && e.RepoDigest != "" {
				anchorDigest = e.RepoDigest
				break
			}
		}
	}
	if anchorDigest != "" && curFlavor == "" {
		hasPlainMate := false
		mateFlavors := make(map[string]bool)
		for _, e := range entries {
			if e.RepoDigest != anchorDigest {
				continue
			}
			pt := version.ParseTag(e.Name)
			if pt.Scheme == version.SchemeUnknown || pt.Prerelease || pt.Precision < 2 {
				continue
			}
			fl, isArch := classifyTagVariant(pt.Variant)
			if isArch {
				continue
			}
			if fl == "" {
				hasPlainMate = true
			} else {
				mateFlavors[fl] = true
			}
		}
		if !hasPlainMate {
			for fl := range mateFlavors {
				allowedFlavors[fl] = true
			}
		}
	}

	anyRepoSemVer := false
	for _, e := range entries {
		pt := version.ParseTag(e.Name)
		if pt.Scheme != version.SchemeUnknown && !pt.Prerelease {
			if _, isArch := classifyTagVariant(pt.Variant); !isArch {
				anyRepoSemVer = true
				break
			}
		}
	}

	// 非语义版本仓库（如 gcli2api:master-ecc207f / gewechat:latest）：保持原仓库顺序，仅将当前运行标签置顶、剔除异构单架构后缀
	if !anyRepoSemVer {
		out := make([]TagDigestEntry, 0, len(entries))
		for _, e := range entries {
			if e.Name == curTagName && curTagName != "" {
				out = append(out, e)
				break
			}
		}
		for _, e := range entries {
			if e.Name == "" || e.Name == curTagName {
				continue
			}
			if _, isArch := classifyTagVariant(e.Name); isArch {
				continue
			}
			out = append(out, e)
		}
		return out
	}

	filterSemVer := func(strictFlavor bool) ([]TagDigestEntry, *TagDigestEntry, *TagDigestEntry) {
		var semverItems []TagDigestEntry
		var curEntry *TagDigestEntry
		var latestEntry *TagDigestEntry
		for i := range entries {
			e := entries[i]
			if e.Name == "" {
				continue
			}
			if e.Name == curTagName {
				cp := e
				curEntry = &cp
			}
			if e.Name == "latest" {
				cp := e
				latestEntry = &cp
			}
			pt := version.ParseTag(e.Name)
			if pt.Scheme == version.SchemeUnknown {
				continue
			}
			if pt.Prerelease && !curParsed.Prerelease {
				continue
			}
			fl, isArch := classifyTagVariant(pt.Variant)
			if isArch {
				continue
			}
			if strictFlavor && !allowedFlavors[fl] {
				continue
			}
			semverItems = append(semverItems, e)
		}
		return semverItems, curEntry, latestEntry
	}

	semverItems, curEntry, latestEntry := filterSemVer(true)
	if len(semverItems) == 0 {
		semverItems, curEntry, latestEntry = filterSemVer(false)
	}

	// 计算每个主版本系列的具体版本号精度（如 mysql 为 3 段 8.4.13，postgres 为 2 段 17.11），剔除同系列内冗余的浮动短别名（始终保留当前标签本身）
	maxPrecByMajor := make(map[int]int)
	globalMaxPrec := 0
	for _, e := range semverItems {
		pt := version.ParseTag(e.Name)
		if pt.Precision > maxPrecByMajor[pt.Major] {
			maxPrecByMajor[pt.Major] = pt.Precision
		}
		if pt.Precision > globalMaxPrec {
			globalMaxPrec = pt.Precision
		}
	}

	deduped := make([]TagDigestEntry, 0, len(semverItems))
	for _, e := range semverItems {
		if e.Name == curTagName {
			deduped = append(deduped, e)
			continue
		}
		pt := version.ParseTag(e.Name)
		targetPrec := maxPrecByMajor[pt.Major]
		if targetPrec > 3 {
			targetPrec = 3
		}
		if pt.Precision < targetPrec {
			prefix := pt.CoreVersion()
			hasConcrete := false
			for _, other := range semverItems {
				if other.Name == e.Name {
					continue
				}
				po := version.ParseTag(other.Name)
				if po.Precision >= targetPrec && version.MatchesPrefix(po, prefix) {
					hasConcrete = true
					break
				}
			}
			if hasConcrete {
				continue
			}
		}
		deduped = append(deduped, e)
	}

	isCurFloating := curTagName != "" && (curParsed.Scheme == version.SchemeUnknown ||
		(globalMaxPrec >= 2 && curParsed.Precision < minInt(globalMaxPrec, 3)))

	sort.SliceStable(deduped, func(i, j int) bool {
		ni, nj := deduped[i].Name, deduped[j].Name
		if isCurFloating && (ni == curTagName) != (nj == curTagName) {
			return ni == curTagName
		}
		pi := version.ParseTag(ni)
		pj := version.ParseTag(nj)
		if scopePrefix != "" {
			iInScope := version.MatchesPrefix(pi, scopePrefix)
			jInScope := version.MatchesPrefix(pj, scopePrefix)
			if iInScope != jInScope {
				return iInScope
			}
		}
		if cmp := version.Compare(pi, pj); cmp != 0 {
			return cmp > 0
		}
		if pi.Precision != pj.Precision {
			return pi.Precision > pj.Precision
		}
		if (pi.Variant == "") != (pj.Variant == "") {
			return pi.Variant == ""
		}
		if deduped[i].UpdatedAt != deduped[j].UpdatedAt {
			return deduped[i].UpdatedAt > deduped[j].UpdatedAt
		}
		return ni > nj
	})

	out := make([]TagDigestEntry, 0, len(deduped)+2)
	if isCurFloating && curEntry != nil && !containsTagEntry(deduped, curTagName) {
		out = append(out, *curEntry)
	}
	out = append(out, deduped...)
	if latestEntry != nil && !containsTagEntry(out, "latest") {
		if len(out) >= 20 {
			out = append(out[:19], *latestEntry)
		} else {
			out = append(out, *latestEntry)
		}
	}
	return out
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func pickScopedHighestTag(cleaned []TagDigestEntry, raw []TagDigestEntry, ref version.ParsedRef, scopePrefix string) string {
	pickFrom := func(pool []TagDigestEntry, prefix string) string {
		var best version.ParsedTag
		hasBest := false
		for _, e := range pool {
			pt := version.ParseTag(e.Name)
			if pt.Scheme == version.SchemeUnknown || pt.Prerelease {
				continue
			}
			if _, isArch := classifyTagVariant(pt.Variant); isArch {
				continue
			}
			if prefix != "" && !version.MatchesPrefix(pt, prefix) {
				continue
			}
			if !hasBest || version.Compare(pt, best) > 0 || (version.Compare(pt, best) == 0 && pt.Precision > best.Precision) {
				best = pt
				hasBest = true
			}
		}
		if hasBest {
			return best.Raw
		}
		return ""
	}

	if scopePrefix != "" {
		if h := pickFrom(cleaned, scopePrefix); h != "" {
			return h
		}
		if h := pickFrom(raw, scopePrefix); h != "" {
			return h
		}
	}
	if h := pickFrom(cleaned, ""); h != "" {
		return h
	}
	if h := pickHighestRepoTag(raw); h != "" {
		pt := version.ParseTag(h)
		if pt.Scheme != version.SchemeUnknown {
			return h
		}
	}
	// 非语义版本仓库（如 gcli2api:master-ecc207f）不返回按字母排第一的无关标签作为“最高版”，避免误触发跳过提醒
	if ref.Tag != "" {
		return ref.Tag
	}
	if len(cleaned) > 0 {
		return cleaned[0].Name
	}
	return ""
}

func sortSearchedTags(entries []TagDigestEntry, qLower string) {
	sort.SliceStable(entries, func(i, j int) bool {
		li := strings.ToLower(entries[i].Name)
		lj := strings.ToLower(entries[j].Name)
		if (li == qLower) != (lj == qLower) {
			return li == qLower
		}
		pi := version.ParseTag(entries[i].Name)
		pj := version.ParseTag(entries[j].Name)
		iStd := pi.Scheme != version.SchemeUnknown && !pi.Prerelease
		jStd := pj.Scheme != version.SchemeUnknown && !pj.Prerelease
		if iStd != jStd {
			return iStd
		}
		if iStd && jStd {
			if cmp := version.Compare(pi, pj); cmp != 0 {
				return cmp > 0
			}
			if pi.Precision != pj.Precision {
				return pi.Precision > pj.Precision
			}
			if (pi.Variant == "") != (pj.Variant == "") {
				return pi.Variant == ""
			}
		}
		if entries[i].UpdatedAt != entries[j].UpdatedAt {
			return entries[i].UpdatedAt > entries[j].UpdatedAt
		}
		return entries[i].Name > entries[j].Name
	})
}

func pickHighestRepoTag(entries []TagDigestEntry) string {
	var best version.ParsedTag
	hasBest := false
	for _, e := range entries {
		pt := version.ParseTag(e.Name)
		if pt.Scheme == version.SchemeUnknown || pt.Prerelease {
			continue
		}
		if fl, isArch := classifyTagVariant(pt.Variant); isArch || fl != "" {
			continue
		}
		if !hasBest || version.Compare(pt, best) > 0 || (version.Compare(pt, best) == 0 && pt.Precision > best.Precision) {
			best = pt
			hasBest = true
		}
	}
	if hasBest {
		return best.Raw
	}
	for _, e := range entries {
		pt := version.ParseTag(e.Name)
		if pt.Scheme != version.SchemeUnknown && !pt.Prerelease {
			if _, isArch := classifyTagVariant(pt.Variant); !isArch {
				if !hasBest || version.Compare(pt, best) > 0 {
					best = pt
					hasBest = true
				}
			}
		}
	}
	if hasBest {
		return best.Raw
	}
	for _, e := range entries {
		if e.Name != "" && !rules.Default().IsRollingTag(e.Name) {
			return e.Name
		}
	}
	if len(entries) > 0 {
		return entries[0].Name
	}
	return ""
}

// LookupTagsByDigest 在有限窗口标签集内反查与 targetDigest（或 targetPlatformDigest）匹配的标签列表，并缓存结果（实施文档 §5.5）。
func (c *Client) LookupTagsByDigest(
	ctx context.Context,
	ref version.ParsedRef,
	targetDigest string,
	targetPlatformDigest string,
	hostPlatform string,
	recent []TagDigestEntry,
) []string {
	return c.LookupTagsByDigestOrConfig(ctx, ref, targetDigest, targetPlatformDigest, "", hostPlatform, recent)
}

// LookupTagsByDigestOrConfig 支持按 RepoDigest / PlatformDigest 反查标签，并在本地无 RepoDigest（如群晖 dsm_api 模式）时通过缓存的 ConfigDigest（ImageID）反查。
func (c *Client) LookupTagsByDigestOrConfig(
	ctx context.Context,
	ref version.ParsedRef,
	targetDigest string,
	targetPlatformDigest string,
	targetConfigDigest string,
	hostPlatform string,
	recent []TagDigestEntry,
) []string {
	targetDigest = NormalizeDigest(targetDigest)
	targetPlatformDigest = NormalizeDigest(targetPlatformDigest)
	targetConfigDigest = NormalizeDigest(targetConfigDigest)
	if targetDigest == "" && targetPlatformDigest == "" && targetConfigDigest == "" {
		return nil
	}
	repoKey := ref.Registry + "/" + ref.Repository
	lookupKey := targetDigest
	if lookupKey == "" {
		lookupKey = targetPlatformDigest
	}
	if lookupKey == "" {
		lookupKey = targetConfigDigest
	}
	if cached := c.getDigestTagCache(ctx, repoKey, lookupKey); len(cached) > 0 {
		return cached
	}

	var matched []string
	seen := make(map[string]bool)
	add := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			matched = append(matched, name)
		}
	}

	for _, e := range recent {
		if targetDigest != "" && e.RepoDigest == targetDigest {
			add(e.Name)
			continue
		}
		if targetPlatformDigest != "" && e.RepoDigest == targetPlatformDigest {
			add(e.Name)
			continue
		}
		if targetConfigDigest != "" && e.ConfigDigest != "" && NormalizeDigest(e.ConfigDigest) == targetConfigDigest {
			add(e.Name)
			continue
		}
		if pd := lookupPlatformDigest(e.PlatformDigests, hostPlatform); pd != "" {
			if (targetDigest != "" && pd == targetDigest) || (targetPlatformDigest != "" && pd == targetPlatformDigest) {
				add(e.Name)
			}
		}
	}

	// 对非 Docker Hub 的 OCI 仓库（recent 只有名字没有 digest），挑选最多 12 个候选标签（含标准版本标签及日期/构建别名标签）做免费 HEAD 比对
	if len(matched) == 0 && (targetDigest != "" || targetPlatformDigest != "") && ref.Registry != "docker.io" && len(recent) > 0 {
		candidates := pickCandidateTagsForLookup(recent, 12)
		baseURL := c.registryAPIBase(ref.Registry)
		for _, tagName := range candidates {
			u := fmt.Sprintf("%s/v2/%s/manifests/%s", baseURL, ref.Repository, url.PathEscape(tagName))
			resp, _, err := c.doRegistryRequest(ctx, ref.Registry, ref.Repository, http.MethodHead, u, acceptManifestHeaders)
			if err != nil {
				continue
			}
			dig := NormalizeDigest(resp.Header.Get("Docker-Content-Digest"))
			if dig != "" && ((targetDigest != "" && dig == targetDigest) || (targetPlatformDigest != "" && dig == targetPlatformDigest)) {
				add(tagName)
			}
		}
	}

	// 若仅提供 targetConfigDigest（本地无 RepoDigest），在已缓存的 manifest:* 记录与 digest_tag_cache 中匹配 ConfigDigest
	if len(matched) == 0 && targetConfigDigest != "" {
		if cached := c.getDigestTagCache(ctx, repoKey, targetConfigDigest); len(cached) > 0 {
			for _, t := range cached {
				add(t)
			}
		} else {
			for _, t := range c.lookupCachedManifestTagsByConfig(ctx, ref.Registry, ref.Repository, targetConfigDigest) {
				add(t)
			}
		}
	}

	if len(matched) > 0 {
		c.putDigestTagCache(ctx, repoKey, lookupKey, matched)
	}
	return matched
}

func pickCandidateTagsForLookup(entries []TagDigestEntry, limit int) []string {
	var parsed []version.ParsedTag
	var aliases []string
	for _, e := range entries {
		name := strings.TrimSpace(e.Name)
		if name == "" || rules.Default().IsRollingTag(name) {
			continue
		}
		pt := version.ParseTag(name)
		if pt.Scheme != version.SchemeUnknown && !pt.Prerelease {
			parsed = append(parsed, pt)
		} else if pt.Scheme == version.SchemeUnknown {
			aliases = append(aliases, name)
		}
	}
	sort.SliceStable(parsed, func(i, j int) bool {
		iMulti := parsed[i].Precision >= 2
		jMulti := parsed[j].Precision >= 2
		if iMulti != jMulti {
			return iMulti
		}
		if cmp := version.Compare(parsed[i], parsed[j]); cmp != 0 {
			return cmp > 0
		}
		return parsed[i].Precision > parsed[j].Precision
	})

	seen := make(map[string]bool, limit)
	out := make([]string, 0, limit)
	for _, pt := range parsed {
		if len(out) >= limit {
			break
		}
		if !seen[pt.Raw] {
			seen[pt.Raw] = true
			out = append(out, pt.Raw)
		}
	}
	if len(out) < limit && len(aliases) > 0 {
		sort.SliceStable(aliases, func(i, j int) bool {
			di, dj := hasAnyDigit(aliases[i]), hasAnyDigit(aliases[j])
			if di != dj {
				return di
			}
			return aliases[i] > aliases[j]
		})
		for _, a := range aliases {
			if len(out) >= limit {
				break
			}
			if !seen[a] {
				seen[a] = true
				out = append(out, a)
			}
		}
	}
	return out
}

func hasAnyDigit(s string) bool {
	for _, r := range s {
		if unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

func (c *Client) fetchDockerHubTagManifest(ctx context.Context, repository, tag, hostPlatform string) (*ManifestResult, error) {
	u := fmt.Sprintf("%s/v2/repositories/%s/tags/%s", c.hubAPIBase(), repository, url.PathEscape(tag))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, &RegistryError{Kind: ErrNetwork, Message: i18n.EngineDetail(i18n.MsgRegistryNetwork, "Docker Hub", err.Error())}
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &RegistryError{Kind: ErrNetwork, Message: i18n.EngineDetail(i18n.MsgRegistryNetwork, "Docker Hub", err.Error())}
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, classifyHTTPStatus("Docker Hub", resp.StatusCode)
	}
	var payload struct {
		Digest string `json:"digest"`
		Images []struct {
			OS      string `json:"os"`
			Arch    string `json:"architecture"`
			Variant string `json:"variant"`
			Digest  string `json:"digest"`
		} `json:"images"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, &RegistryError{Kind: ErrRegistry, Message: i18n.EngineDetail(i18n.MsgRegistryParse, "Docker Hub")}
	}
	res := &ManifestResult{RepoDigest: NormalizeDigest(payload.Digest)}
	if hostPlatform != "" {
		plats := make([]string, len(payload.Images))
		for i, im := range payload.Images {
			plats[i] = formatPlatform(im.OS, im.Arch, im.Variant)
		}
		if i := selectPlatformIndex(hostPlatform, plats); i >= 0 && payload.Images[i].Digest != "" {
			res.PlatformDigest = NormalizeDigest(payload.Images[i].Digest)
			if res.RepoDigest == "" {
				res.RepoDigest = res.PlatformDigest
			}
		}
	}
	if res.RepoDigest == "" && len(payload.Images) == 1 && payload.Images[0].Digest != "" {
		res.RepoDigest = NormalizeDigest(payload.Images[0].Digest)
	}
	return res, nil
}

type dockerHubTagsPayload struct {
	Next    string `json:"next"`
	Results []struct {
		Name        string `json:"name"`
		Digest      string `json:"digest"`
		LastUpdated string `json:"last_updated"`
		Images      []struct {
			OS      string `json:"os"`
			Arch    string `json:"architecture"`
			Variant string `json:"variant"`
			Digest  string `json:"digest"`
		} `json:"images"`
	} `json:"results"`
}

func parseHubTimestamp(raw string) int64 {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil && t.Year() > 1970 {
			return t.Unix()
		}
	}
	return 0
}

func decodeDockerHubTagEntries(body []byte) ([]TagDigestEntry, string, error) {
	var payload dockerHubTagsPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, "", err
	}
	out := make([]TagDigestEntry, 0, len(payload.Results))
	for _, r := range payload.Results {
		pMap := make(map[string]string, len(r.Images))
		for _, im := range r.Images {
			if im.Digest != "" {
				pMap[formatPlatform(im.OS, im.Arch, im.Variant)] = NormalizeDigest(im.Digest)
			}
		}
		out = append(out, TagDigestEntry{
			Name:            r.Name,
			RepoDigest:      NormalizeDigest(r.Digest),
			PlatformDigests: pMap,
			UpdatedAt:       parseHubTimestamp(r.LastUpdated),
		})
	}
	return out, payload.Next, nil
}

func (c *Client) fetchDockerHubTags(ctx context.Context, repository string, maxPages int) ([]TagDigestEntry, error) {
	hubBase := c.hubAPIBase()
	nextURL := fmt.Sprintf("%s/v2/repositories/%s/tags?page_size=100&ordering=last_updated", hubBase, repository)
	var out []TagDigestEntry

	for page := 0; page < maxPages && nextURL != ""; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, nextURL, nil)
		if err != nil {
			return nil, &RegistryError{Kind: ErrNetwork, Message: i18n.EngineDetail(i18n.MsgRegistryNetwork, "Docker Hub", err.Error())}
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, &RegistryError{Kind: ErrNetwork, Message: i18n.EngineDetail(i18n.MsgRegistryNetwork, "Docker Hub", err.Error())}
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, classifyHTTPStatus("Docker Hub", resp.StatusCode)
		}
		items, next, err := decodeDockerHubTagEntries(body)
		if err != nil {
			return nil, &RegistryError{Kind: ErrRegistry, Message: i18n.EngineDetail(i18n.MsgRegistryParse, "Docker Hub")}
		}
		out = append(out, items...)
		nextURL = next
	}
	return out, nil
}

func (c *Client) searchDockerHubTagsByNameWithSize(ctx context.Context, repository, query string, pageSize int) ([]TagDigestEntry, error) {
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 50
	}
	u := fmt.Sprintf("%s/v2/repositories/%s/tags?page_size=%d&ordering=last_updated&name=%s",
		c.hubAPIBase(), repository, pageSize, url.QueryEscape(query))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, &RegistryError{Kind: ErrNetwork, Message: i18n.EngineDetail(i18n.MsgRegistryNetwork, "Docker Hub", err.Error())}
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &RegistryError{Kind: ErrNetwork, Message: i18n.EngineDetail(i18n.MsgRegistryNetwork, "Docker Hub", err.Error())}
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, classifyHTTPStatus("Docker Hub", resp.StatusCode)
	}
	items, _, err := decodeDockerHubTagEntries(body)
	if err != nil {
		return nil, &RegistryError{Kind: ErrRegistry, Message: i18n.EngineDetail(i18n.MsgRegistryParse, "Docker Hub")}
	}
	return items, nil
}

func (c *Client) fetchCachedHubTagsByName(ctx context.Context, repository, query string, pageSize int) ([]TagDigestEntry, error) {
	cacheKey := fmt.Sprintf("hub_q:%s:%s:%d", repository, query, pageSize)
	if b, ok := c.getCache(ctx, cacheKey); ok {
		var list []TagDigestEntry
		if json.Unmarshal(b, &list) == nil {
			return list, nil
		}
	}
	items, err := c.searchDockerHubTagsByNameWithSize(ctx, repository, query, pageSize)
	if err != nil {
		return nil, err
	}
	if b, err := json.Marshal(items); err == nil {
		c.putCache(ctx, cacheKey, b, 2*time.Hour)
	}
	return items, nil
}

func (c *Client) fetchCachedHubSingleTagEntry(ctx context.Context, repository, tag string) (TagDigestEntry, error) {
	cacheKey := fmt.Sprintf("hub_tag:%s:%s", repository, tag)
	if b, ok := c.getCache(ctx, cacheKey); ok {
		var e TagDigestEntry
		if json.Unmarshal(b, &e) == nil && e.Name != "" {
			return e, nil
		}
	}
	u := fmt.Sprintf("%s/v2/repositories/%s/tags/%s", c.hubAPIBase(), repository, url.PathEscape(tag))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return TagDigestEntry{}, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return TagDigestEntry{}, err
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return TagDigestEntry{}, classifyHTTPStatus("Docker Hub", resp.StatusCode)
	}
	var payload struct {
		Name        string `json:"name"`
		Digest      string `json:"digest"`
		LastUpdated string `json:"last_updated"`
		Images      []struct {
			OS      string `json:"os"`
			Arch    string `json:"architecture"`
			Variant string `json:"variant"`
			Digest  string `json:"digest"`
		} `json:"images"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return TagDigestEntry{}, err
	}
	pMap := make(map[string]string, len(payload.Images))
	for _, im := range payload.Images {
		if im.Digest != "" {
			pMap[formatPlatform(im.OS, im.Arch, im.Variant)] = NormalizeDigest(im.Digest)
		}
	}
	name := payload.Name
	if name == "" {
		name = tag
	}
	entry := TagDigestEntry{
		Name:            name,
		RepoDigest:      NormalizeDigest(payload.Digest),
		PlatformDigests: pMap,
		UpdatedAt:       parseHubTimestamp(payload.LastUpdated),
	}
	if b, err := json.Marshal(entry); err == nil {
		c.putCache(ctx, cacheKey, b, 2*time.Hour)
	}
	return entry, nil
}

func (c *Client) fetchOCITagsList(ctx context.Context, reg, repo string, maxPages int) ([]TagDigestEntry, error) {
	if maxPages <= 0 {
		maxPages = 1
	}
	base := c.registryAPIBase(reg)
	nextURL := fmt.Sprintf("%s/v2/%s/tags/list?n=1000", base, repo)
	var out []TagDigestEntry
	seen := make(map[string]bool)

	for page := 0; page < maxPages && nextURL != ""; page++ {
		resp, body, err := c.doRegistryRequest(ctx, reg, repo, http.MethodGet, nextURL, "application/json")
		if err != nil {
			if page > 0 && len(out) > 0 {
				break
			}
			return nil, err
		}
		var payload struct {
			Tags []string `json:"tags"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, &RegistryError{Kind: ErrRegistry, Message: i18n.EngineDetail(i18n.MsgRegistryParse, reg)}
		}
		for _, t := range payload.Tags {
			if t != "" && !seen[t] {
				seen[t] = true
				out = append(out, TagDigestEntry{Name: t})
			}
		}
		nextURL = parseLinkNextURL(base, resp.Header.Get("Link"))
	}
	return out, nil
}

func parseLinkNextURL(baseURL, linkHeader string) string {
	for _, part := range strings.Split(linkHeader, ",") {
		part = strings.TrimSpace(part)
		if !strings.Contains(strings.ToLower(part), `rel="next"`) && !strings.Contains(strings.ToLower(part), `rel=next`) {
			continue
		}
		start := strings.Index(part, "<")
		end := strings.Index(part, ">")
		if start < 0 || end <= start+1 {
			continue
		}
		raw := strings.TrimSpace(part[start+1 : end])
		if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
			return raw
		}
		if strings.HasPrefix(raw, "/") {
			return strings.TrimRight(baseURL, "/") + raw
		}
		return strings.TrimRight(baseURL, "/") + "/" + raw
	}
	return ""
}

func (c *Client) doRegistryRequest(ctx context.Context, reg, repo, method, targetURL, accept string) (*http.Response, []byte, error) {
	creds, _ := c.getCredential(ctx, reg)
	tokenKey := reg + "/" + repo
	tok := c.getCachedToken(tokenKey)

	send := func(bearer string, basic *Creds) (*http.Response, []byte, error) {
		req, err := http.NewRequestWithContext(ctx, method, targetURL, nil)
		if err != nil {
			return nil, nil, &RegistryError{Kind: ErrNetwork, Message: i18n.EngineDetail(i18n.MsgRegistryNetwork, reg, err.Error())}
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		} else if basic != nil && basic.Username != "" {
			req.SetBasicAuth(basic.Username, basic.Secret)
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, nil, &RegistryError{Kind: ErrNetwork, Message: i18n.EngineDetail(i18n.MsgRegistryNetwork, reg, err.Error())}
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		_ = resp.Body.Close()
		return resp, body, nil
	}

	resp, body, err := send(tok, creds)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		www := resp.Header.Get("WWW-Authenticate")
		newTok, authErr := c.acquireBearerToken(ctx, www, creds)
		if authErr == nil && newTok != "" {
			c.setCachedToken(tokenKey, newTok)
			resp, body, err = send(newTok, nil)
			if err != nil {
				return nil, nil, err
			}
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, classifyHTTPStatus(reg, resp.StatusCode)
	}
	return resp, body, nil
}

// acquireBearerToken 解析 WWW-Authenticate: Bearer realm="...",service="...",scope="..." 并获取 token。
func (c *Client) acquireBearerToken(ctx context.Context, header string, creds *Creds) (string, error) {
	if !strings.HasPrefix(strings.ToLower(header), "bearer ") {
		return "", errors.New("not bearer challenge")
	}
	params := parseAuthHeaderParams(header[7:])
	realm := params["realm"]
	if realm == "" {
		return "", errors.New("missing realm")
	}
	u, err := url.Parse(realm)
	if err != nil {
		return "", err
	}
	q := u.Query()
	if s := params["service"]; s != "" {
		q.Set("service", s)
	}
	if sc := params["scope"]; sc != "" {
		q.Set("scope", sc)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	if creds != nil && creds.Username != "" {
		req.SetBasicAuth(creds.Username, creds.Secret)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token status %d", resp.StatusCode)
	}
	var payload struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	if payload.Token != "" {
		return payload.Token, nil
	}
	return payload.AccessToken, nil
}

func parseAuthHeaderParams(s string) map[string]string {
	out := make(map[string]string)
	for _, part := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		out[strings.ToLower(strings.TrimSpace(k))] = strings.Trim(strings.TrimSpace(v), `"`)
	}
	return out
}

func classifyHTTPStatus(reg string, status int) *RegistryError {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return &RegistryError{Kind: ErrAuthRequired, Message: i18n.EngineDetail(i18n.MsgRegistryAuth, reg, strconv.Itoa(status))}
	case http.StatusNotFound:
		return &RegistryError{Kind: ErrNotFound, Message: i18n.EngineDetail(i18n.MsgRegistryNotFound, reg)}
	case http.StatusTooManyRequests:
		return &RegistryError{Kind: ErrRateLimited, Message: i18n.EngineDetail(i18n.MsgRegistryRateLimited, reg)}
	default:
		return &RegistryError{Kind: ErrRegistry, Message: i18n.EngineDetail(i18n.MsgRegistryStatus, reg, strconv.Itoa(status))}
	}
}

func (c *Client) registryAPIBase(reg string) string {
	if c.BaseURLOverride != nil {
		if u, ok := c.BaseURLOverride[reg]; ok {
			return u
		}
	}
	if reg == "docker.io" || reg == "" {
		return "https://registry-1.docker.io"
	}
	return "https://" + reg
}

// HubAPIBase 返回 Docker Hub Web API 基础地址（支持单元测试覆盖）。
func (c *Client) HubAPIBase() string { return c.hubAPIBase() }

// GitHubBase 返回 GitHub 基础地址（支持单元测试覆盖）。
func (c *Client) GitHubBase() string {
	if c.BaseURLOverride != nil {
		if u, ok := c.BaseURLOverride["github.com"]; ok {
			return u
		}
	}
	return "https://github.com"
}

func (c *Client) hubAPIBase() string {
	if c.BaseURLOverride != nil {
		if u, ok := c.BaseURLOverride["hub.docker.com"]; ok {
			return u
		}
	}
	return "https://hub.docker.com"
}

func formatPlatform(osName, arch, variant string) string {
	p := strings.ToLower(osName) + "/" + strings.ToLower(arch)
	if variant != "" {
		p += "/" + strings.ToLower(variant)
	}
	return p
}

func (c *Client) getCachedToken(key string) string {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	t, ok := c.tokens[key]
	if !ok || time.Now().After(t.expiresAt) {
		return ""
	}
	return t.token
}

func (c *Client) setCachedToken(key, tok string) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	c.tokens[key] = cachedToken{token: tok, expiresAt: time.Now().Add(4 * time.Minute)}
}

func (c *Client) clearCachedTokens(reg string) {
	prefix := strings.ToLower(strings.TrimSpace(reg)) + "/"
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	for k := range c.tokens {
		if strings.HasPrefix(k, prefix) {
			delete(c.tokens, k)
		}
	}
}

// GetCache 从 SQLite registry_cache 读取未过期的缓存字节。
func (c *Client) GetCache(ctx context.Context, key string) ([]byte, bool) {
	return c.getCache(ctx, key)
}

// PutCache 将字节以指定 TTL 写入 SQLite registry_cache。
func (c *Client) PutCache(ctx context.Context, key string, val []byte, ttl time.Duration) {
	c.putCache(ctx, key, val, ttl)
}

func (c *Client) getCache(ctx context.Context, key string) ([]byte, bool) {
	if c.st == nil {
		return nil, false
	}
	var val []byte
	var exp int64
	err := c.st.DB.QueryRowContext(ctx, `SELECT v, expires_at FROM registry_cache WHERE k=?`, key).Scan(&val, &exp)
	if err != nil || time.Now().Unix() > exp {
		return nil, false
	}
	return val, true
}

func (c *Client) putCache(ctx context.Context, key string, val []byte, ttl time.Duration) {
	if c.st == nil {
		return
	}
	exp := time.Now().Add(ttl).Unix()
	_, _ = c.st.DB.ExecContext(ctx, `
		INSERT INTO registry_cache(k, v, expires_at) VALUES (?, ?, ?)
		ON CONFLICT(k) DO UPDATE SET v=excluded.v, expires_at=excluded.expires_at`,
		key, val, exp)
}

func (c *Client) getDigestTagCache(ctx context.Context, repo, digest string) []string {
	if c.st == nil || digest == "" {
		return nil
	}
	var raw string
	if err := c.st.DB.QueryRowContext(ctx, `SELECT tags_json FROM digest_tag_cache WHERE repo=? AND digest=?`, repo, digest).Scan(&raw); err != nil {
		return nil
	}
	var tags []string
	_ = json.Unmarshal([]byte(raw), &tags)
	return tags
}

func (c *Client) putDigestTagCache(ctx context.Context, repo, digest string, tags []string) {
	if c.st == nil || digest == "" || len(tags) == 0 {
		return
	}
	b, _ := json.Marshal(tags)
	_, _ = c.st.DB.ExecContext(ctx, `
		INSERT INTO digest_tag_cache(repo, digest, tags_json) VALUES (?, ?, ?)
		ON CONFLICT(repo, digest) DO UPDATE SET tags_json=excluded.tags_json`,
		repo, digest, string(b))
}

func (c *Client) mergeDigestTagCache(ctx context.Context, repo, digest, tag string) {
	if c.st == nil || digest == "" || tag == "" {
		return
	}
	existing := c.getDigestTagCache(ctx, repo, digest)
	for _, t := range existing {
		if t == tag {
			return
		}
	}
	existing = append(existing, tag)
	c.putDigestTagCache(ctx, repo, digest, existing)
}

func (c *Client) lookupCachedManifestTagsByConfig(ctx context.Context, reg, repo, configDigest string) []string {
	if c.st == nil || configDigest == "" {
		return nil
	}
	prefix := fmt.Sprintf("manifest:%s/%s:", reg, repo)
	rows, err := c.st.DB.QueryContext(ctx, `SELECT k, v FROM registry_cache WHERE k LIKE ?`, prefix+"%")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	seen := make(map[string]bool)
	for rows.Next() {
		var k string
		var v []byte
		if rows.Scan(&k, &v) != nil {
			continue
		}
		var mr ManifestResult
		if json.Unmarshal(v, &mr) != nil || NormalizeDigest(mr.ConfigDigest) != configDigest {
			continue
		}
		rest := strings.TrimPrefix(k, prefix)
		tag, _, _ := strings.Cut(rest, "@")
		if tag != "" && !strings.HasPrefix(tag, "sha256:") && !seen[tag] {
			seen[tag] = true
			out = append(out, tag)
		}
	}
	return out
}

// ListCredentials 返回已配置的私有仓库凭证（不含密码明文）。
func (c *Client) ListCredentials(ctx context.Context) ([]Creds, error) {
	rows, err := c.st.DB.QueryContext(ctx, `SELECT registry, username FROM registry_credential ORDER BY registry ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Creds
	for rows.Next() {
		var cr Creds
		if err := rows.Scan(&cr.Registry, &cr.Username); err != nil {
			return nil, err
		}
		out = append(out, cr)
	}
	return out, rows.Err()
}

// SaveCredential 保存私有仓库账号密码。
func (c *Client) SaveCredential(ctx context.Context, in Creds) error {
	reg := version.NormalizeRegistry(in.Registry)
	if reg == "" || in.Username == "" || in.Secret == "" {
		return i18n.NewEngineError(i18n.MsgRegistryCredIncomplete)
	}
	nonce, sealed, err := c.box.Seal([]byte(in.Secret))
	if err != nil {
		return err
	}
	secID := "regsec_" + reg
	if _, err := c.st.DB.ExecContext(ctx, `
		INSERT INTO secret(id, nonce, cipher) VALUES (?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET nonce=excluded.nonce, cipher=excluded.cipher`,
		secID, nonce, sealed); err != nil {
		return err
	}
	_, err = c.st.DB.ExecContext(ctx, `
		INSERT INTO registry_credential(registry, username, secret_ref) VALUES (?, ?, ?)
		ON CONFLICT(registry) DO UPDATE SET username=excluded.username, secret_ref=excluded.secret_ref`,
		reg, in.Username, secID)
	if err == nil {
		c.clearCachedTokens(reg)
	}
	return err
}

// DeleteCredential 删除私有仓库凭证。
func (c *Client) DeleteCredential(ctx context.Context, reg string) error {
	reg = version.NormalizeRegistry(reg)
	_, _ = c.st.DB.ExecContext(ctx, `DELETE FROM secret WHERE id=?`, "regsec_"+reg)
	_, err := c.st.DB.ExecContext(ctx, `DELETE FROM registry_credential WHERE registry=?`, reg)
	c.clearCachedTokens(reg)
	return err
}

func (c *Client) getCredential(ctx context.Context, reg string) (*Creds, error) {
	if c.st == nil || c.box == nil {
		return nil, nil
	}
	norm := version.NormalizeRegistry(reg)
	var user, secRef string
	err := c.st.DB.QueryRowContext(ctx, `SELECT username, secret_ref FROM registry_credential WHERE registry=?`, norm).Scan(&user, &secRef)
	if err != nil && norm != strings.ToLower(strings.TrimSpace(reg)) {
		err = c.st.DB.QueryRowContext(ctx, `SELECT username, secret_ref FROM registry_credential WHERE registry=?`, strings.ToLower(strings.TrimSpace(reg))).Scan(&user, &secRef)
	}
	if err != nil {
		return nil, err
	}
	var nonce, cipher []byte
	if err := c.st.DB.QueryRowContext(ctx, `SELECT nonce, cipher FROM secret WHERE id=?`, secRef).Scan(&nonce, &cipher); err != nil {
		return nil, err
	}
	plain, err := c.box.Open(nonce, cipher)
	if err != nil {
		return nil, err
	}
	return &Creds{Registry: reg, Username: user, Secret: string(plain)}, nil
}

// DockerAuthHeader 生成供 Docker Engine POST /images/create 使用的 X-Registry-Auth Base64 编码字符串。
func (c *Client) DockerAuthHeader(ctx context.Context, reg string) string {
	creds, err := c.getCredential(ctx, reg)
	if err != nil || creds == nil || creds.Username == "" {
		return ""
	}
	b, _ := json.Marshal(map[string]string{
		"username":      creds.Username,
		"password":      creds.Secret,
		"serveraddress": reg,
	})
	return base64.URLEncoding.EncodeToString(b)
}

// NormalizeDigest 统一摘要格式：去除空白并在 64 位十六进制摘要前补全 "sha256:" 前缀（审计 M2）。
func NormalizeDigest(d string) string {
	d = strings.TrimSpace(d)
	if d == "" || strings.Contains(d, ":") {
		return d
	}
	if len(d) == 64 && isHex(d) {
		return "sha256:" + strings.ToLower(d)
	}
	return d
}

func isHex(s string) bool {
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

func parseConfigDigest(body []byte) string {
	var mf struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}
	if json.Unmarshal(body, &mf) != nil {
		return ""
	}
	return NormalizeDigest(mf.Config.Digest)
}

func parseAnnotationVersion(body []byte) string {
	var mf struct {
		Annotations map[string]string `json:"annotations"`
	}
	if json.Unmarshal(body, &mf) != nil {
		return ""
	}
	return pickAnnotationVersion(mf.Annotations)
}

func pickAnnotationVersion(ann map[string]string) string {
	if len(ann) == 0 {
		return ""
	}
	if v := strings.TrimSpace(ann["org.opencontainers.image.version"]); v != "" {
		return v
	}
	return strings.TrimSpace(ann["org.label-schema.version"])
}

type platformParts struct{ os, arch, variant string }

func splitPlatform(p string) platformParts {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(p)), "/")
	var pp platformParts
	if len(parts) > 0 {
		pp.os = parts[0]
	}
	if len(parts) > 1 {
		pp.arch = parts[1]
	}
	if len(parts) > 2 {
		pp.variant = parts[2]
	}
	if pp.arch == "arm64" && pp.variant == "v8" {
		pp.variant = "" // linux/arm64 与 linux/arm64/v8 视为同一平台（审计 L6）
	}
	return pp
}

// IsKnownPlatform 判断平台字符串是否可用于平台级比对（为空或架构未知时返回 false）。
func IsKnownPlatform(p string) bool {
	pp := splitPlatform(p)
	return pp.os != "" && pp.arch != "" && pp.arch != "unknown"
}

// PlatformMatches 判断候选平台是否与主机平台精确匹配（arm64 兼容 v8 变体）。
func PlatformMatches(host, cand string) bool {
	if !IsKnownPlatform(host) {
		return false
	}
	h, c := splitPlatform(host), splitPlatform(cand)
	return h.os == c.os && h.arch == c.arch && h.variant == c.variant
}

// selectPlatformIndex 返回与主机平台最匹配的候选下标：优先精确匹配，其次同 os/arch 而变体缺省的候选；无匹配返回 -1。
func selectPlatformIndex(host string, cands []string) int {
	if !IsKnownPlatform(host) {
		return -1
	}
	for i, c := range cands {
		if PlatformMatches(host, c) {
			return i
		}
	}
	h := splitPlatform(host)
	for i, c := range cands {
		cp := splitPlatform(c)
		if cp.os == h.os && cp.arch == h.arch && (cp.variant == "" || h.variant == "") {
			return i
		}
	}
	return -1
}

func lookupPlatformDigest(m map[string]string, host string) string {
	if len(m) == 0 {
		return ""
	}
	if d, ok := m[host]; ok && IsKnownPlatform(host) {
		return d
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if i := selectPlatformIndex(host, keys); i >= 0 {
		return m[keys[i]]
	}
	return ""
}
