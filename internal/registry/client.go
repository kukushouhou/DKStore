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
	RepoDigest     string            `json:"repoDigest,omitempty"`     // 顶层 Docker-Content-Digest（Index 或单架构 Manifest）
	PlatformDigest string            `json:"platformDigest,omitempty"` // 对应主机 os/arch/variant 的具体 Manifest Digest（若顶层是 Index）
	ConfigDigest   string            `json:"configDigest,omitempty"`   // 对应主机架构的镜像 Config Digest（可与本地 ImageInspect.Id 比对）
	VersionLabel   string            `json:"versionLabel,omitempty"`   // OCI Manifest/Index annotations 中的版本号
	Annotations    map[string]string `json:"annotations,omitempty"`    // OCI 链接相关注解（source/url/documentation/changelog）
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
	flight  flightGroup
}

type cachedToken struct {
	token     string
	expiresAt time.Time
}

type flightCall struct {
	wg  sync.WaitGroup
	val any
	err error
}

type flightGroup struct {
	mu    sync.Mutex
	calls map[string]*flightCall
}

func (g *flightGroup) do(key string, fn func() (any, error)) (any, error) {
	g.mu.Lock()
	if g.calls == nil {
		g.calls = make(map[string]*flightCall)
	}
	if c, ok := g.calls[key]; ok {
		g.mu.Unlock()
		c.wg.Wait()
		return c.val, c.err
	}
	c := &flightCall{}
	c.wg.Add(1)
	g.calls[key] = c
	g.mu.Unlock()

	defer func() {
		g.mu.Lock()
		delete(g.calls, key)
		g.mu.Unlock()
		c.wg.Done()
	}()
	c.val, c.err = fn()
	return c.val, c.err
}

type checkSessionKey struct{}

type checkSession struct {
	mu        sync.RWMutex
	manifests map[string]*ManifestResult
	tags      map[string][]TagDigestEntry
}

// WithCheckSession 为单轮批量检查绑定内存级去重会话：
// 即使 bypassCache=true 绕过历史 SQLite 缓存，同一轮检查内相同的镜像 Manifest 或仓库标签列表也只向远端请求一次。
func WithCheckSession(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Value(checkSessionKey{}).(*checkSession); ok {
		return ctx
	}
	return context.WithValue(ctx, checkSessionKey{}, &checkSession{
		manifests: make(map[string]*ManifestResult),
		tags:      make(map[string][]TagDigestEntry),
	})
}

func getCheckSession(ctx context.Context) *checkSession {
	if ctx == nil {
		return nil
	}
	s, _ := ctx.Value(checkSessionKey{}).(*checkSession)
	return s
}

func cloneManifestResult(src *ManifestResult) *ManifestResult {
	if src == nil {
		return nil
	}
	cp := *src
	if len(src.Annotations) > 0 {
		cp.Annotations = make(map[string]string, len(src.Annotations))
		for k, v := range src.Annotations {
			cp.Annotations[k] = v
		}
	}
	return &cp
}

func cloneTagEntries(src []TagDigestEntry) []TagDigestEntry {
	if src == nil {
		return nil
	}
	cp := make([]TagDigestEntry, len(src))
	copy(cp, src)
	return cp
}

func (s *checkSession) getManifest(key string) (*ManifestResult, bool) {
	s.mu.RLock()
	v, ok := s.manifests[key]
	s.mu.RUnlock()
	if !ok || v == nil {
		return nil, false
	}
	return cloneManifestResult(v), true
}

func (s *checkSession) putManifest(key string, v *ManifestResult) {
	if v == nil {
		return
	}
	cp := cloneManifestResult(v)
	s.mu.Lock()
	s.manifests[key] = cp
	s.mu.Unlock()
}

func (s *checkSession) getTags(key string) ([]TagDigestEntry, bool) {
	s.mu.RLock()
	v, ok := s.tags[key]
	s.mu.RUnlock()
	if !ok {
		return nil, false
	}
	return cloneTagEntries(v), true
}

func (s *checkSession) putTags(key string, v []TagDigestEntry) {
	cp := cloneTagEntries(v)
	s.mu.Lock()
	s.tags[key] = cp
	s.mu.Unlock()
}

const (
	acceptManifestHeaders = "application/vnd.oci.image.index.v1+json, " +
		"application/vnd.docker.distribution.manifest.list.v2+json, " +
		"application/vnd.oci.image.manifest.v1+json, " +
		"application/vnd.docker.distribution.manifest.v2+json"

	ociLookupHeadConcurrency = 4
)

// New 创建 Registry 客户端。
func New(st *store.Store, box *secret.Box) *Client {
	var rt http.RoundTripper = http.DefaultTransport
	if baseTr, ok := http.DefaultTransport.(*http.Transport); ok {
		tr := baseTr.Clone()
		tr.MaxIdleConns = 100
		tr.MaxIdleConnsPerHost = 16
		tr.IdleConnTimeout = 90 * time.Second
		rt = tr
	}
	return &Client{
		st:  st,
		box: box,
		httpClient: &http.Client{
			Timeout:   20 * time.Second,
			Transport: rt,
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
	localDig := NormalizeDigest(opts.LocalRepoDigest)
	sessKey := fmt.Sprintf("%s/%s:%s@%s|dig=%s|cfg=%t", ref.Registry, ref.Repository, tag, opts.HostPlatform, localDig, opts.NeedConfig)
	sess := getCheckSession(ctx)
	if sess != nil {
		if cached, ok := sess.getManifest(sessKey); ok && cached.RepoDigest != "" && (!opts.NeedConfig || cached.ConfigDigest != "") {
			return cached, nil
		}
	}
	if !opts.BypassCache {
		if b, ok := c.getCache(ctx, cacheKey); ok {
			var res ManifestResult
			if json.Unmarshal(b, &res) == nil && res.RepoDigest != "" && (!opts.NeedConfig || res.ConfigDigest != "") {
				if sess != nil {
					sess.putManifest(sessKey, &res)
				}
				return &res, nil
			}
		}
	}

	val, err := c.flight.do("mf:"+sessKey, func() (any, error) {
		return c.fetchManifestRemote(ctx, ref, tag, repoKey, cacheKey, localDig, opts)
	})
	if err != nil {
		return nil, err
	}
	res, _ := val.(*ManifestResult)
	if res == nil {
		return nil, nil
	}
	if sess != nil && res.RepoDigest != "" {
		sess.putManifest(sessKey, res)
	}
	return cloneManifestResult(res), nil
}

func (c *Client) fetchManifestRemote(
	ctx context.Context,
	ref version.ParsedRef,
	tag, repoKey, cacheKey, localDig string,
	opts ManifestCheckOptions,
) (*ManifestResult, error) {
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
		Annotations:  parseLinkAnnotations(body),
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
				res.Annotations = mergeLinkAnnotations(res.Annotations, idx.Manifests[i].Annotations)
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
				res.Annotations = mergeLinkAnnotations(res.Annotations, parseLinkAnnotations(pBody))
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

// FetchImageLayerSizes 预读目标镜像（按目标宿主机架构匹配子 Manifest）全部层的真实压缩体积，
// 返回以层 Digest 十六进制全称及前 12 位短 ID 为键的字节数映射（失败或超时时返回 nil，不阻塞拉取）。
func (c *Client) FetchImageLayerSizes(ctx context.Context, rawImageRef, hostPlatform string) map[string]int64 {
	if c == nil || strings.TrimSpace(rawImageRef) == "" {
		return nil
	}
	ref, err := version.ParseImageRef(rawImageRef)
	if err != nil {
		return nil
	}
	tag := ref.Tag
	if tag == "" {
		tag = ref.Digest
	}
	if tag == "" {
		return nil
	}

	cacheKey := fmt.Sprintf("layersizes:%s/%s:%s@%s", ref.Registry, ref.Repository, tag, hostPlatform)
	if b, ok := c.getCache(ctx, cacheKey); ok {
		var cached map[string]int64
		if json.Unmarshal(b, &cached) == nil && len(cached) > 0 {
			return cached
		}
	}

	var sizes map[string]int64

	// 1. 若镜像引用显式配置了加速镜像域名（如 docker.1ms.run/...），优先直连该加速源读取 Manifest
	if rawHost, rawRepo := extractExplicitMirrorHostRepo(rawImageRef); rawHost != "" &&
		rawHost != ref.Registry && rawHost != "index.docker.io" && rawHost != "registry-1.docker.io" {
		sizes = c.fetchOCIManifestLayerSizes(ctx, rawHost, rawRepo, tag, hostPlatform)
	}

	// 2. Docker Hub 镜像优先走网页端 /tags/{tag}/images 接口（0 拉取配额）
	if len(sizes) == 0 && ref.Registry == "docker.io" && !strings.HasPrefix(tag, "sha256:") {
		sizes = c.fetchDockerHubTagLayerSizes(ctx, ref.Repository, tag, hostPlatform)
	}

	// 3. 标准 OCI / Registry v2 Manifest 接口（支持 GHCR / LSCR / 私有仓库鉴权与多架构 Index 解析）
	if len(sizes) == 0 {
		sizes = c.fetchOCIManifestLayerSizes(ctx, ref.Registry, ref.Repository, tag, hostPlatform)
	}

	if len(sizes) > 0 {
		if b, err := json.Marshal(sizes); err == nil {
			c.putCache(ctx, cacheKey, b, 30*time.Minute)
		}
	}
	return sizes
}

func extractExplicitMirrorHostRepo(raw string) (host, repo string) {
	s := strings.TrimSpace(raw)
	if at := strings.Index(s, "@"); at >= 0 {
		s = s[:at]
	}
	first, rest, ok := strings.Cut(s, "/")
	if !ok || rest == "" {
		return "", ""
	}
	if !strings.ContainsAny(first, ".:") && !strings.EqualFold(first, "localhost") {
		return "", ""
	}
	lastSlash := strings.LastIndex(rest, "/")
	colon := strings.LastIndex(rest, ":")
	if colon > lastSlash {
		rest = rest[:colon]
	}
	if !strings.Contains(rest, "/") && strings.Contains(strings.ToLower(first), "docker") {
		rest = "library/" + rest
	}
	return strings.ToLower(first), rest
}

func recordLayerDigestSize(out map[string]int64, digest string, size int64) {
	if size <= 0 {
		return
	}
	norm := NormalizeDigest(digest)
	hexDig := strings.ToLower(strings.TrimPrefix(norm, "sha256:"))
	if len(hexDig) < 12 {
		return
	}
	out[hexDig] = size
	out[hexDig[:12]] = size
}

func parseManifestBodyLayerSizes(body []byte) map[string]int64 {
	var mf struct {
		Layers []struct {
			Digest string `json:"digest"`
			Size   int64  `json:"size"`
		} `json:"layers"`
	}
	if json.Unmarshal(body, &mf) != nil || len(mf.Layers) == 0 {
		return nil
	}
	out := make(map[string]int64, len(mf.Layers)*2)
	for _, l := range mf.Layers {
		recordLayerDigestSize(out, l.Digest, l.Size)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (c *Client) fetchOCIManifestLayerSizes(ctx context.Context, reg, repo, tag, hostPlatform string) map[string]int64 {
	baseURL := c.registryAPIBase(reg)
	manifestURL := fmt.Sprintf("%s/v2/%s/manifests/%s", baseURL, repo, url.PathEscape(tag))
	resp, body, err := c.doRegistryRequest(ctx, reg, repo, http.MethodGet, manifestURL, acceptManifestHeaders)
	if err != nil {
		return nil
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(ct, "manifest.list") || strings.Contains(ct, "image.index") {
		var idx struct {
			Manifests []struct {
				Digest   string `json:"digest"`
				Platform struct {
					OS      string `json:"os"`
					Arch    string `json:"architecture"`
					Variant string `json:"variant"`
				} `json:"platform"`
			} `json:"manifests"`
		}
		if json.Unmarshal(body, &idx) != nil || len(idx.Manifests) == 0 {
			return nil
		}
		pickedDigest := ""
		if hostPlatform != "" {
			plats := make([]string, len(idx.Manifests))
			for i, m := range idx.Manifests {
				plats[i] = formatPlatform(m.Platform.OS, m.Platform.Arch, m.Platform.Variant)
			}
			if i := selectPlatformIndex(hostPlatform, plats); i >= 0 {
				pickedDigest = NormalizeDigest(idx.Manifests[i].Digest)
			}
		} else if len(idx.Manifests) == 1 {
			pickedDigest = NormalizeDigest(idx.Manifests[0].Digest)
		}
		if pickedDigest == "" {
			return nil
		}
		pURL := fmt.Sprintf("%s/v2/%s/manifests/%s", baseURL, repo, pickedDigest)
		_, pBody, pErr := c.doRegistryRequest(ctx, reg, repo, http.MethodGet, pURL, acceptManifestHeaders)
		if pErr != nil {
			return nil
		}
		return parseManifestBodyLayerSizes(pBody)
	}
	return parseManifestBodyLayerSizes(body)
}

func (c *Client) fetchDockerHubTagLayerSizes(ctx context.Context, repository, tag, hostPlatform string) map[string]int64 {
	u := fmt.Sprintf("%s/v2/repositories/%s/tags/%s/images", c.hubAPIBase(), repository, url.PathEscape(tag))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var images []struct {
		OS      string `json:"os"`
		Arch    string `json:"architecture"`
		Variant string `json:"variant"`
		Layers  []struct {
			Digest string `json:"digest"`
			Size   int64  `json:"size"`
		} `json:"layers"`
	}
	if json.Unmarshal(body, &images) != nil || len(images) == 0 {
		return nil
	}
	picked := -1
	if hostPlatform != "" {
		plats := make([]string, len(images))
		for i, im := range images {
			plats[i] = formatPlatform(im.OS, im.Arch, im.Variant)
		}
		picked = selectPlatformIndex(hostPlatform, plats)
	} else if len(images) == 1 {
		picked = 0
	}
	if picked < 0 {
		return nil
	}
	out := make(map[string]int64, len(images[picked].Layers)*2)
	for _, l := range images[picked].Layers {
		recordLayerDigestSize(out, l.Digest, l.Size)
	}
	if len(out) == 0 {
		return nil
	}
	return out
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
	sessKey := fmt.Sprintf("%s/%s:p=%d", ref.Registry, ref.Repository, maxPages)
	sess := getCheckSession(ctx)
	if sess != nil {
		if cached, ok := sess.getTags(sessKey); ok {
			return cached, nil
		}
	}
	if !bypassCache {
		if b, ok := c.getCache(ctx, cacheKey); ok {
			var list []TagDigestEntry
			if json.Unmarshal(b, &list) == nil {
				if sess != nil {
					sess.putTags(sessKey, list)
				}
				return list, nil
			}
		}
	}

	val, err := c.flight.do("tags:"+sessKey, func() (any, error) {
		var (
			entries []TagDigestEntry
			fErr    error
		)
		if ref.Registry == "docker.io" {
			entries, fErr = c.fetchDockerHubTags(ctx, ref.Repository, maxPages)
		} else {
			entries, fErr = c.fetchOCITagsList(ctx, ref.Registry, ref.Repository, 5)
		}
		if fErr != nil {
			return nil, fErr
		}
		if b, mErr := json.Marshal(entries); mErr == nil {
			c.putCache(ctx, cacheKey, b, 6*time.Hour)
		}
		return entries, nil
	})
	if err != nil {
		return nil, err
	}
	entries, _ := val.([]TagDigestEntry)
	if sess != nil {
		sess.putTags(sessKey, entries)
	}
	return cloneTagEntries(entries), nil
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

const (
	maxTagSearchItems      = 50
	maxModalHubPages       = 20
	maxModalOCIPages       = 15
	hubModalPageConcurrent = 5
)

// SearchOptions 控制版本弹窗的标签列表查询、预发布过滤与排序方式。
type SearchOptions struct {
	Query              string
	Remote             bool
	All                bool   // true 时并发拉取完整标签池并全量返回前端（不提前过滤丢弃或截断 50 条）
	IncludePrerelease  *bool  // nil 时自动按容器当前标签是否为预发布版本决定
	SortBy             string // "version"（默认）| "time"
	CurrentResolvedTag string // 本地已识别出的精确版本标签（用于滚动/短标签计算三档候选）
	CurrentRepoDigest  string // 本地当前镜像 Digest（排除同镜像标签）
}

// TaggedSearchItem 是携带预发布标记、通道实际指向版本及本地筛选排序元数据的标签项。
type TaggedSearchItem struct {
	TagDigestEntry
	Prerelease    bool
	AliasOf       string
	StableVisible bool
	PreVisible    bool
	SearchOrder   int
}

// SearchResult 是 SearchTagsEx 的完整返回结果。
type SearchResult struct {
	Items                []TaggedSearchItem
	HighestTag           string
	HighestStableTag     string
	HighestPrereleaseTag string
	CurrentPrerelease    bool
	HasTimestamps        bool
	Candidates           *version.UpdateCandidates
}

// SearchTags 兼容简化调用签名，返回标签列表与作用域内最高版本标签 highestTag。
func (c *Client) SearchTags(ctx context.Context, ref version.ParsedRef, query string, remote bool) ([]TagDigestEntry, string, error) {
	res, err := c.SearchTagsEx(ctx, ref, SearchOptions{
		Query:  query,
		Remote: remote,
	})
	if err != nil {
		return nil, "", err
	}
	out := make([]TagDigestEntry, len(res.Items))
	for i, it := range res.Items {
		out[i] = it.TagDigestEntry
	}
	return out, res.HighestTag, nil
}

// SearchTagsEx 支持弹窗版本浏览与按需检索：
// - All=true 时直接拉取完整标签池并附带预发布/默认显隐与排序权重全量交给前端，支持前端纯本地 0ms 切换开关、排序与检索；
// - remote=true 时对所有仓库（含 Docker Hub / GHCR / LSCR / Quay / 私有仓库）一律绕过缓存直连云端实时拉取（网络异常时自动回退本地缓存）。
func (c *Client) SearchTagsEx(ctx context.Context, ref version.ParsedRef, opts SearchOptions) (*SearchResult, error) {
	q := strings.TrimSpace(opts.Query)
	qLower := strings.ToLower(q)
	curPrerelease := version.IsPrereleaseAny(ref.Tag)
	includePre := curPrerelease
	if opts.IncludePrerelease != nil {
		includePre = *opts.IncludePrerelease
	}
	sortBy := strings.ToLower(strings.TrimSpace(opts.SortBy))
	if sortBy != "time" {
		sortBy = "version"
	}

	var (
		recent []TagDigestEntry
		err    error
	)
	if opts.All {
		recent, err = c.fetchFullTagsForModal(ctx, ref, opts.Remote)
	} else {
		recent, err = c.FetchRecentTags(ctx, ref, 5, opts.Remote)
		if err != nil && opts.Remote {
			// 若远端刷新偶发网络波动但本地尚有缓存且非强制搜词，尝试降级读取缓存；搜词场景在 Docker Hub 下继续走按名检索
			if fallback, fbErr := c.FetchRecentTags(ctx, ref, 5, false); fbErr == nil && len(fallback) > 0 && q == "" {
				recent = fallback
				err = nil
			}
		}
	}
	if err != nil && (!opts.Remote || q == "" || ref.Registry != "docker.io") {
		return nil, err
	}

	combined := recent
	curTag := version.ParseTag(ref.Tag)
	scopePrefix := computeServiceScopePrefix(ref)

	if ref.Registry == "docker.io" {
		if q != "" && !opts.All {
			hubQKey := fmt.Sprintf("hub_q:%s:%s:%d", ref.Repository, qLower, 100)
			if opts.Remote {
				remoteHits, rErr := c.searchDockerHubTagsByNameWithSize(ctx, ref.Repository, q, 100)
				if rErr != nil && len(recent) == 0 {
					return nil, rErr
				}
				if len(remoteHits) > 0 {
					if b, mErr := json.Marshal(remoteHits); mErr == nil {
						c.putCache(ctx, hubQKey, b, 2*time.Hour)
					}
					combined = mergeUniqueTagEntries(remoteHits, combined)
				}
			} else if b, ok := c.getCache(ctx, hubQKey); ok {
				var cachedHits []TagDigestEntry
				if json.Unmarshal(b, &cachedHits) == nil && len(cachedHits) > 0 {
					combined = mergeUniqueTagEntries(cachedHits, combined)
				}
			}
		} else if curTag.Scheme != version.SchemeUnknown {
			// 对 mysql:8.4 / postgres:17 / registry:2 等多变体或长周期大仓库，补充拉取本系列标签，防止正式版本被海量变体挤出分页窗口
			seriesQuery := scopePrefix
			if seriesQuery == "" {
				seriesQuery = strconv.Itoa(curTag.Major)
			}
			if curTag.Precision == 1 {
				seriesQuery = curTag.CoreVersion() + "."
			}
			if seriesQuery != "" {
				var (
					seriesHits []TagDigestEntry
					sErr       error
				)
				if opts.Remote {
					seriesHits, sErr = c.searchDockerHubTagsByNameWithSize(ctx, ref.Repository, seriesQuery, 100)
					if sErr == nil && len(seriesHits) > 0 {
						if b, mErr := json.Marshal(seriesHits); mErr == nil {
							c.putCache(ctx, fmt.Sprintf("hub_q:%s:%s:%d", ref.Repository, seriesQuery, 100), b, 2*time.Hour)
						}
					}
				} else {
					seriesHits, sErr = c.fetchCachedHubTagsByName(ctx, ref.Repository, seriesQuery, 100)
				}
				if sErr == nil && len(seriesHits) > 0 {
					combined = mergeUniqueTagEntries(combined, seriesHits)
				}
			}
			if ref.Tag != "" && !containsTagEntry(combined, ref.Tag) {
				if single, tErr := c.fetchCachedHubSingleTagEntry(ctx, ref.Repository, ref.Tag); tErr == nil && single.Name != "" && single.UpdatedAt > 0 {
					combined = mergeUniqueTagEntries([]TagDigestEntry{single}, combined)
				}
			}
		}
	}

	hasTimestamps := false
	for _, e := range combined {
		if e.UpdatedAt > 0 {
			hasTimestamps = true
			break
		}
	}

	if opts.All {
		return buildFullModalSearchResult(combined, ref, scopePrefix, curPrerelease, hasTimestamps, opts.CurrentResolvedTag, opts.CurrentRepoDigest), nil
	}

	effectiveSort := sortBy
	if !hasTimestamps {
		effectiveSort = "version"
	}

	cleanedFull := buildCleanDefaultTags(combined, ref, scopePrefix, includePre, "version")
	cleanedDefault := cleanedFull
	if effectiveSort == "time" {
		cleanedDefault = buildCleanDefaultTags(combined, ref, scopePrefix, includePre, effectiveSort)
	}
	highestStable := pickScopedHighestTag(cleanedDefault, combined, ref, scopePrefix, false)
	highestPre := pickScopedHighestTag(cleanedDefault, combined, ref, scopePrefix, true)
	highest := highestStable
	if includePre || curPrerelease {
		if highestPre != "" {
			highest = highestPre
		}
	}

	curFlavor := resolveCurrentFlavor(ref.Tag)

	var chosen []TagDigestEntry
	if qLower != "" {
		matched := make([]TagDigestEntry, 0, len(combined))
		for _, e := range combined {
			if e.Name != "" && strings.Contains(strings.ToLower(e.Name), qLower) {
				matched = append(matched, e)
			}
		}
		sortSearchedTags(matched, qLower, curFlavor, effectiveSort)
		if len(matched) > maxTagSearchItems {
			matched = matched[:maxTagSearchItems]
		}
		chosen = matched
	} else {
		if len(cleanedDefault) > maxTagSearchItems {
			cleanedDefault = cleanedDefault[:maxTagSearchItems]
		}
		chosen = cleanedDefault
	}

	digestGroup := buildDigestTagGroup(combined)
	candidates := computeModalCandidates(cleanedFull, combined, ref, opts.CurrentResolvedTag, opts.CurrentRepoDigest, digestGroup)
	items := make([]TaggedSearchItem, 0, len(chosen))
	for i, e := range chosen {
		alias := resolveEntryAliasOfWithGroup(e, digestGroup)
		isPre := version.IsPrereleaseAny(e.Name)
		if isPre && alias != "" && version.ParseTag(e.Name).Scheme == version.SchemeUnknown && !version.IsPrereleaseAny(alias) {
			isPre = false
		}
		items = append(items, TaggedSearchItem{
			TagDigestEntry: e,
			Prerelease:     isPre,
			AliasOf:        alias,
			StableVisible:  !isPre,
			PreVisible:     true,
			SearchOrder:    i,
		})
	}

	return &SearchResult{
		Items:                items,
		HighestTag:           highest,
		HighestStableTag:     highestStable,
		HighestPrereleaseTag: highestPre,
		CurrentPrerelease:    curPrerelease,
		HasTimestamps:        hasTimestamps,
		Candidates:           candidates,
	}, nil
}

func (c *Client) fetchFullTagsForModal(ctx context.Context, ref version.ParsedRef, bypassCache bool) ([]TagDigestEntry, error) {
	fullCacheKey := fmt.Sprintf("tags_full:%s/%s", ref.Registry, ref.Repository)
	recentCacheKey := fmt.Sprintf("tags:%s/%s", ref.Registry, ref.Repository)

	readCached := func() ([]TagDigestEntry, bool) {
		if b, ok := c.getCache(ctx, fullCacheKey); ok {
			var list []TagDigestEntry
			if json.Unmarshal(b, &list) == nil && len(list) > 0 {
				return list, true
			}
		}
		if b, ok := c.getCache(ctx, recentCacheKey); ok {
			var list []TagDigestEntry
			if json.Unmarshal(b, &list) == nil && len(list) > 0 {
				return list, true
			}
		}
		return nil, false
	}

	if !bypassCache {
		if cached, ok := readCached(); ok {
			return cloneTagEntries(cached), nil
		}
	}

	val, err := c.flight.do("tags_full:"+ref.Registry+"/"+ref.Repository, func() (any, error) {
		var (
			entries []TagDigestEntry
			fErr    error
		)
		if ref.Registry == "docker.io" {
			entries, fErr = c.fetchDockerHubAllTags(ctx, ref.Repository, maxModalHubPages)
		} else {
			entries, fErr = c.fetchOCITagsList(ctx, ref.Registry, ref.Repository, maxModalOCIPages)
		}
		if fErr != nil {
			return nil, fErr
		}
		if b, mErr := json.Marshal(entries); mErr == nil {
			c.putCache(ctx, fullCacheKey, b, 6*time.Hour)
			c.putCache(ctx, recentCacheKey, b, 6*time.Hour)
		}
		return entries, nil
	})
	if err != nil {
		if bypassCache {
			if cached, ok := readCached(); ok {
				return cloneTagEntries(cached), nil
			}
		}
		return nil, err
	}
	entries, _ := val.([]TagDigestEntry)
	return cloneTagEntries(entries), nil
}

func buildFullModalSearchResult(
	combined []TagDigestEntry,
	ref version.ParsedRef,
	scopePrefix string,
	curPrerelease bool,
	hasTimestamps bool,
	resolvedTag string,
	localDigest string,
) *SearchResult {
	unique := mergeUniqueTagEntries(combined)
	cleanStable := buildCleanDefaultTags(unique, ref, scopePrefix, false, "version")
	cleanPre := buildCleanDefaultTags(unique, ref, scopePrefix, true, "version")

	highestStable := pickScopedHighestTag(cleanStable, unique, ref, scopePrefix, false)
	highestPre := pickScopedHighestTag(cleanPre, unique, ref, scopePrefix, true)
	highest := highestStable
	if curPrerelease && highestPre != "" {
		highest = highestPre
	}

	stableSet := make(map[string]bool, len(cleanStable))
	for _, e := range cleanStable {
		stableSet[e.Name] = true
	}
	preSet := make(map[string]bool, len(cleanPre))
	for _, e := range cleanPre {
		preSet[e.Name] = true
	}

	// 预解析全量标签以计算权威搜索排序权重（避免在 O(N log N) 比较器内重复正则解析）
	type parsedMeta struct {
		entry  TagDigestEntry
		pt     version.ParsedTag
		flavor string
		isArch bool
	}
	curFlavor := resolveCurrentFlavor(ref.Tag)
	metas := make([]parsedMeta, len(unique))
	parsedMap := make(map[string]version.ParsedTag, len(unique))
	for i, e := range unique {
		pt := version.ParseTag(e.Name)
		parsedMap[e.Name] = pt
		var fl string
		var isArch bool
		if pt.Scheme != version.SchemeUnknown {
			fl, isArch = classifyTagVariant(pt.Variant)
		} else {
			fl, isArch = classifyTagVariant(e.Name)
		}
		metas[i] = parsedMeta{entry: e, pt: pt, flavor: fl, isArch: isArch}
	}

	sort.SliceStable(metas, func(i, j int) bool {
		mi, mj := metas[i], metas[j]
		iSem := mi.pt.Scheme != version.SchemeUnknown
		jSem := mj.pt.Scheme != version.SchemeUnknown
		if iSem != jSem {
			return iSem
		}
		if iSem && jSem {
			if mi.isArch != mj.isArch {
				return !mi.isArch
			}
			if cmp := version.Compare(mi.pt, mj.pt); cmp != 0 {
				return cmp > 0
			}
			if mi.pt.Precision != mj.pt.Precision {
				return mi.pt.Precision > mj.pt.Precision
			}
			iMatchFlavor := mi.flavor == curFlavor
			jMatchFlavor := mj.flavor == curFlavor
			if iMatchFlavor != jMatchFlavor {
				return iMatchFlavor
			}
			if (mi.flavor == "") != (mj.flavor == "") {
				return mi.flavor == ""
			}
		}
		if mi.entry.UpdatedAt != mj.entry.UpdatedAt {
			return mi.entry.UpdatedAt > mj.entry.UpdatedAt
		}
		return mi.entry.Name > mj.entry.Name
	})

	searchOrderMap := make(map[string]int, len(metas))
	for idx, m := range metas {
		searchOrderMap[m.entry.Name] = idx
	}

	// 保序合并 cleanStable、cleanPre 与剩余全量标签，确保：
	// 1) 前端按 stableVisible 过滤时顺序与 cleanStable 完全一致；
	// 2) 前端按 preVisible 过滤时顺序与 cleanPre 完全一致；
	// 3) 任何变体/历史/短别名标签均保留在返回结果中，供搜索框本地即时匹配。
	orderedAll := make([]TagDigestEntry, 0, len(unique))
	seenOrdered := make(map[string]bool, len(unique))
	pushOrd := func(e TagDigestEntry) {
		if e.Name != "" && !seenOrdered[e.Name] {
			seenOrdered[e.Name] = true
			orderedAll = append(orderedAll, e)
		}
	}
	iPre := 0
	for _, stItem := range cleanStable {
		if preSet[stItem.Name] {
			for iPre < len(cleanPre) && cleanPre[iPre].Name != stItem.Name {
				pushOrd(cleanPre[iPre])
				iPre++
			}
			if iPre < len(cleanPre) {
				pushOrd(cleanPre[iPre])
				iPre++
			}
		} else {
			pushOrd(stItem)
		}
	}
	for iPre < len(cleanPre) {
		pushOrd(cleanPre[iPre])
		iPre++
	}
	for _, m := range metas {
		pushOrd(m.entry)
	}

	digestGroup := buildDigestTagGroup(unique)
	candPool := cleanStable
	if curPrerelease {
		candPool = cleanPre
	}
	candidates := computeModalCandidates(candPool, unique, ref, resolvedTag, localDigest, digestGroup)
	items := make([]TaggedSearchItem, 0, len(orderedAll))
	for _, e := range orderedAll {
		alias := resolveEntryAliasOfWithGroup(e, digestGroup)
		isPre := version.IsPrereleaseAny(e.Name)
		if isPre && alias != "" && parsedMap[e.Name].Scheme == version.SchemeUnknown && !version.IsPrereleaseAny(alias) {
			isPre = false
		}
		items = append(items, TaggedSearchItem{
			TagDigestEntry: e,
			Prerelease:     isPre,
			AliasOf:        alias,
			StableVisible:  stableSet[e.Name],
			PreVisible:     preSet[e.Name],
			SearchOrder:    searchOrderMap[e.Name],
		})
	}

	return &SearchResult{
		Items:                items,
		HighestTag:           highest,
		HighestStableTag:     highestStable,
		HighestPrereleaseTag: highestPre,
		CurrentPrerelease:    curPrerelease,
		HasTimestamps:        hasTimestamps,
		Candidates:           candidates,
	}
}

func computeModalCandidates(
	cleanPool []TagDigestEntry,
	rawPool []TagDigestEntry,
	ref version.ParsedRef,
	resolvedTag string,
	localDigest string,
	digestGroup map[string][]string,
) *version.UpdateCandidates {
	baseTag := ""
	pRef := version.ParseTag(ref.Tag)
	pRes := version.ParseTag(resolvedTag)
	switch {
	case pRef.Scheme != version.SchemeUnknown && pRef.Precision >= 3:
		baseTag = ref.Tag
	case pRes.Scheme != version.SchemeUnknown && pRes.Precision >= 2 && (pRef.Scheme == version.SchemeUnknown || pRes.Precision > pRef.Precision):
		baseTag = resolvedTag
	case pRef.Scheme != version.SchemeUnknown && pRef.Precision >= 2:
		baseTag = ref.Tag
	}
	if baseTag == "" && ref.Tag != "" {
		for _, e := range rawPool {
			if e.Name == ref.Tag && e.RepoDigest != "" {
				if alias := resolveAliasFromCandidates(e.Name, digestGroup[e.RepoDigest]); alias != "" {
					if pa := version.ParseTag(alias); pa.Scheme != version.SchemeUnknown && pa.Precision >= 2 {
						baseTag = alias
					}
				}
				break
			}
		}
	}
	if baseTag == "" {
		return nil
	}
	normLocal := NormalizeDigest(localDigest)
	names := make([]string, 0, len(cleanPool))
	for _, e := range cleanPool {
		if e.Name == "" {
			continue
		}
		if normLocal != "" && NormalizeDigest(e.RepoDigest) == normLocal {
			continue
		}
		names = append(names, e.Name)
	}
	cands := version.SelectUpdateCandidates(baseTag, names)
	if cands.Count() == 0 {
		return nil
	}
	return &cands
}

func buildDigestTagGroup(pool []TagDigestEntry) map[string][]string {
	out := make(map[string][]string)
	for _, e := range pool {
		if e.Name != "" && e.RepoDigest != "" {
			out[e.RepoDigest] = append(out[e.RepoDigest], e.Name)
		}
	}
	return out
}

func resolveCurrentFlavor(curTagName string) string {
	curTrim := strings.TrimSpace(curTagName)
	curParsed := version.ParseTag(curTrim)
	if curParsed.Scheme != version.SchemeUnknown {
		fl, _ := classifyTagVariant(curParsed.Variant)
		return fl
	}
	if _, chVar, ok := version.ExtractPrereleaseChannel(curTrim); ok {
		fl, _ := classifyTagVariant(chVar)
		return fl
	}
	fl, _ := classifyTagVariant(curTrim)
	return fl
}

func resolveEntryAliasOf(entry TagDigestEntry, pool []TagDigestEntry) string {
	if entry.RepoDigest == "" || entry.Name == "" {
		return ""
	}
	var sameDigest []string
	for _, other := range pool {
		if other.Name != "" && other.RepoDigest == entry.RepoDigest {
			sameDigest = append(sameDigest, other.Name)
		}
	}
	return resolveAliasFromCandidates(entry.Name, sameDigest)
}

func resolveEntryAliasOfWithGroup(entry TagDigestEntry, digestGroup map[string][]string) string {
	if entry.RepoDigest == "" || entry.Name == "" {
		return ""
	}
	return resolveAliasFromCandidates(entry.Name, digestGroup[entry.RepoDigest])
}

func resolveAliasFromCandidates(entryName string, sameDigest []string) string {
	if len(sameDigest) <= 1 {
		return ""
	}
	pt := version.ParseTag(entryName)
	if pt.Scheme != version.SchemeUnknown && pt.Precision > 2 {
		return ""
	}
	best := version.SelectBestDisplayTag(entryName, sameDigest)
	if best == "" || strings.EqualFold(best, entryName) {
		return ""
	}
	bp := version.ParseTag(best)
	// 仅当反查到多段具体版本号（如 2.5.12 / 3.0.0-beta.1 / 17.12）时才标注括号别名，避免单数字浮动主版本（如 "1"）造成误解
	if bp.Scheme != version.SchemeUnknown && bp.Precision < 2 {
		return ""
	}
	if pt.Scheme != version.SchemeUnknown {
		if bp.Scheme == version.SchemeUnknown || bp.Precision <= pt.Precision {
			return ""
		}
	}
	return best
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
		case tok == "rootless":
			flavorSet["rootless"] = true
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

func buildCleanDefaultTags(entries []TagDigestEntry, ref version.ParsedRef, scopePrefix string, includePrerelease bool, sortBy string) []TagDigestEntry {
	if len(entries) == 0 {
		return nil
	}
	curTagName := strings.TrimSpace(ref.Tag)
	curParsed := version.ParseTag(curTagName)
	curFlavor := resolveCurrentFlavor(curTagName)
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
			if pt.Scheme == version.SchemeUnknown || pt.Precision < 2 {
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
		if pt.Scheme != version.SchemeUnknown && (!pt.Prerelease || includePrerelease) {
			if _, isArch := classifyTagVariant(pt.Variant); !isArch {
				anyRepoSemVer = true
				break
			}
		}
	}

	// 非语义版本仓库（如 gcli2api:master-ecc207f / gewechat:latest）：剔除异构单架构后缀并置顶 latest 与当前运行标签
	if !anyRepoSemVer {
		out := make([]TagDigestEntry, 0, len(entries))
		seen := make(map[string]bool, len(entries))
		appendUnique := func(e TagDigestEntry) {
			if e.Name != "" && !seen[e.Name] {
				seen[e.Name] = true
				out = append(out, e)
			}
		}
		if sortBy != "time" {
			for _, e := range entries {
				if e.Name == "latest" && curTagName == "latest" {
					appendUnique(e)
					break
				}
			}
			for _, e := range entries {
				if e.Name == curTagName && curTagName != "" {
					appendUnique(e)
					break
				}
			}
			for _, e := range entries {
				if e.Name == "latest" {
					appendUnique(e)
					break
				}
			}
		}
		for _, e := range entries {
			if e.Name == "" {
				continue
			}
			if _, isArch := classifyTagVariant(e.Name); isArch {
				continue
			}
			if !includePrerelease && e.Name != curTagName && version.IsPrereleaseChannelTag(e.Name) {
				continue
			}
			appendUnique(e)
		}
		if sortBy == "time" {
			sortTagsByUpdatedTime(out)
		}
		return out
	}

	curPinnedFlavor := version.HasFlavorVersionPin(curParsed.Variant)
	if curParsed.Scheme == version.SchemeUnknown {
		curPinnedFlavor = version.HasFlavorVersionPin(curTagName)
	}
	filterSemVer := func(strictFlavor bool) ([]TagDigestEntry, *TagDigestEntry, *TagDigestEntry, []TagDigestEntry) {
		var semverItems []TagDigestEntry
		var curEntry *TagDigestEntry
		var latestEntry *TagDigestEntry
		var preChannels []TagDigestEntry
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
			if includePrerelease && e.Name != "latest" {
				if chVar, ok := version.IsPrimaryPrereleaseChannel(e.Name); ok {
					fl, isArch := classifyTagVariant(chVar)
					if !isArch && (!strictFlavor || allowedFlavors[fl]) {
						preChannels = append(preChannels, e)
					}
				}
			}
			pt := version.ParseTag(e.Name)
			if pt.Scheme == version.SchemeUnknown {
				continue
			}
			if pt.Prerelease && !includePrerelease && e.Name != curTagName {
				continue
			}
			fl, isArch := classifyTagVariant(pt.Variant)
			if isArch {
				continue
			}
			if strictFlavor && !allowedFlavors[fl] {
				continue
			}
			if strictFlavor && curFlavor != "" && e.Name != curTagName && version.HasFlavorVersionPin(pt.Variant) != curPinnedFlavor {
				continue
			}
			semverItems = append(semverItems, e)
		}
		return semverItems, curEntry, latestEntry, preChannels
	}

	semverItems, curEntry, latestEntry, preChannels := filterSemVer(true)
	if len(semverItems) == 0 {
		semverItems, curEntry, latestEntry, preChannels = filterSemVer(false)
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
		iExactVar := pi.Variant == curParsed.Variant
		jExactVar := pj.Variant == curParsed.Variant
		if iExactVar != jExactVar {
			return iExactVar
		}
		if (pi.Variant == "") != (pj.Variant == "") {
			return pi.Variant == ""
		}
		iPinned := version.HasFlavorVersionPin(pi.Variant)
		jPinned := version.HasFlavorVersionPin(pj.Variant)
		if iPinned != jPinned {
			return !iPinned
		}
		if deduped[i].UpdatedAt != deduped[j].UpdatedAt {
			return deduped[i].UpdatedAt > deduped[j].UpdatedAt
		}
		return ni > nj
	})

	sort.SliceStable(preChannels, func(i, j int) bool {
		if preChannels[i].UpdatedAt != preChannels[j].UpdatedAt {
			return preChannels[i].UpdatedAt > preChannels[j].UpdatedAt
		}
		return preChannels[i].Name < preChannels[j].Name
	})
	if len(preChannels) > 2 {
		preChannels = preChannels[:2]
	}

	out := make([]TagDigestEntry, 0, len(deduped)+len(preChannels)+3)
	seenOut := make(map[string]bool, len(deduped)+4)
	pushOut := func(e TagDigestEntry) {
		if e.Name != "" && !seenOut[e.Name] {
			seenOut[e.Name] = true
			out = append(out, e)
		}
	}

	// latest 始终置顶在第 1 行（不再沉底），随后是当前浮动标签与预发布主通道（如 beta），再跟语义化版本号从高到低
	if latestEntry != nil {
		pushOut(*latestEntry)
	}
	if isCurFloating && curEntry != nil {
		pushOut(*curEntry)
	}
	for _, ch := range preChannels {
		pushOut(ch)
	}
	for _, d := range deduped {
		pushOut(d)
	}

	if sortBy == "time" {
		sortTagsByUpdatedTime(out)
	}
	return out
}

func sortTagsByUpdatedTime(items []TagDigestEntry) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].UpdatedAt != items[j].UpdatedAt {
			return items[i].UpdatedAt > items[j].UpdatedAt
		}
		if (items[i].Name == "latest") != (items[j].Name == "latest") {
			return items[i].Name == "latest"
		}
		pi := version.ParseTag(items[i].Name)
		pj := version.ParseTag(items[j].Name)
		if pi.Scheme != version.SchemeUnknown && pj.Scheme != version.SchemeUnknown {
			if cmp := version.Compare(pi, pj); cmp != 0 {
				return cmp > 0
			}
		}
		return items[i].Name > items[j].Name
	})
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func pickScopedHighestTag(cleaned []TagDigestEntry, raw []TagDigestEntry, ref version.ParsedRef, scopePrefix string, includePrerelease bool) string {
	curParsed := version.ParseTag(strings.TrimSpace(ref.Tag))
	pickFrom := func(pool []TagDigestEntry, prefix string) string {
		var best version.ParsedTag
		hasBest := false
		for _, e := range pool {
			pt := version.ParseTag(e.Name)
			if pt.Scheme == version.SchemeUnknown {
				continue
			}
			if pt.Prerelease && !includePrerelease {
				continue
			}
			if _, isArch := classifyTagVariant(pt.Variant); isArch {
				continue
			}
			if prefix != "" && !version.MatchesPrefix(pt, prefix) {
				continue
			}
			better := !hasBest
			if !better {
				cmp := version.Compare(pt, best)
				if cmp > 0 || (cmp == 0 && pt.Precision > best.Precision) {
					better = true
				} else if cmp == 0 && pt.Precision == best.Precision {
					if pt.Variant == curParsed.Variant && best.Variant != curParsed.Variant {
						better = true
					} else if !version.HasFlavorVersionPin(pt.Variant) && version.HasFlavorVersionPin(best.Variant) {
						better = true
					}
				}
			}
			if better {
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
	if h := pickHighestRepoTag(raw, includePrerelease); h != "" {
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

func hasSearchQueryPrefix(tagNameLower, qLower string) bool {
	if strings.HasPrefix(tagNameLower, qLower) {
		return true
	}
	trimTag := strings.TrimPrefix(tagNameLower, "v")
	trimQ := strings.TrimPrefix(qLower, "v")
	return trimQ != "" && strings.HasPrefix(trimTag, trimQ)
}

func isExactSearchMatch(tagNameLower, qLower string) bool {
	if tagNameLower == qLower {
		return true
	}
	return strings.TrimPrefix(tagNameLower, "v") == strings.TrimPrefix(qLower, "v")
}

func sortSearchedTags(entries []TagDigestEntry, qLower, curFlavor, sortBy string) {
	sort.SliceStable(entries, func(i, j int) bool {
		li := strings.ToLower(entries[i].Name)
		lj := strings.ToLower(entries[j].Name)
		iExact := isExactSearchMatch(li, qLower)
		jExact := isExactSearchMatch(lj, qLower)
		if iExact != jExact {
			return iExact
		}
		iPref := hasSearchQueryPrefix(li, qLower)
		jPref := hasSearchQueryPrefix(lj, qLower)
		if iPref != jPref {
			return iPref
		}

		if sortBy == "time" && entries[i].UpdatedAt != entries[j].UpdatedAt {
			return entries[i].UpdatedAt > entries[j].UpdatedAt
		}

		pi := version.ParseTag(entries[i].Name)
		pj := version.ParseTag(entries[j].Name)
		iSem := pi.Scheme != version.SchemeUnknown
		jSem := pj.Scheme != version.SchemeUnknown
		if iSem != jSem {
			return iSem
		}
		if iSem && jSem {
			flI, archI := classifyTagVariant(pi.Variant)
			flJ, archJ := classifyTagVariant(pj.Variant)
			if archI != archJ {
				return !archI
			}
			if cmp := version.Compare(pi, pj); cmp != 0 {
				return cmp > 0
			}
			if pi.Precision != pj.Precision {
				return pi.Precision > pj.Precision
			}
			iMatchFlavor := flI == curFlavor
			jMatchFlavor := flJ == curFlavor
			if iMatchFlavor != jMatchFlavor {
				return iMatchFlavor
			}
			if (flI == "") != (flJ == "") {
				return flI == ""
			}
		}
		if entries[i].UpdatedAt != entries[j].UpdatedAt {
			return entries[i].UpdatedAt > entries[j].UpdatedAt
		}
		return entries[i].Name > entries[j].Name
	})
}

func pickHighestRepoTag(entries []TagDigestEntry, includePrerelease bool) string {
	var best version.ParsedTag
	hasBest := false
	for _, e := range entries {
		pt := version.ParseTag(e.Name)
		if pt.Scheme == version.SchemeUnknown || (pt.Prerelease && !includePrerelease) {
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
		if pt.Scheme != version.SchemeUnknown && (!pt.Prerelease || includePrerelease) {
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

	// 对非 Docker Hub 的 OCI 仓库（recent 只有名字没有 digest），挑选最多 12 个候选标签（含标准版本标签及日期/构建别名标签）做免费并发 HEAD 比对
	if len(matched) == 0 && (targetDigest != "" || targetPlatformDigest != "") && ref.Registry != "docker.io" && len(recent) > 0 {
		candidates := pickCandidateTagsForLookup(recent, 12)
		if len(candidates) > 0 {
			baseURL := c.registryAPIBase(ref.Registry)
			hits := make([]bool, len(candidates))
			probeHead := func(i int) {
				u := fmt.Sprintf("%s/v2/%s/manifests/%s", baseURL, ref.Repository, url.PathEscape(candidates[i]))
				resp, _, err := c.doRegistryRequest(ctx, ref.Registry, ref.Repository, http.MethodHead, u, acceptManifestHeaders)
				if err != nil {
					return
				}
				dig := NormalizeDigest(resp.Header.Get("Docker-Content-Digest"))
				if dig != "" && ((targetDigest != "" && dig == targetDigest) || (targetPlatformDigest != "" && dig == targetPlatformDigest)) {
					hits[i] = true
				}
			}

			startIdx := 0
			if len(candidates) > 1 && c.getCachedToken(ref.Registry+"/"+ref.Repository) == "" {
				probeHead(0)
				startIdx = 1
			}
			if startIdx < len(candidates) {
				sem := make(chan struct{}, ociLookupHeadConcurrency)
				var wg sync.WaitGroup
				for i := startIdx; i < len(candidates); i++ {
					if ctx.Err() != nil {
						break
					}
					acquired := false
					select {
					case sem <- struct{}{}:
						acquired = true
					case <-ctx.Done():
					}
					if !acquired {
						break
					}
					wg.Add(1)
					go func(idx int) {
						defer wg.Done()
						defer func() { <-sem }()
						probeHead(idx)
					}(i)
				}
				wg.Wait()
			}
			for i, ok := range hits {
				if ok {
					add(candidates[i])
				}
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
	var preParsed []version.ParsedTag
	var aliases []string
	for _, e := range entries {
		name := strings.TrimSpace(e.Name)
		if name == "" || rules.Default().IsRollingTag(name) || version.IsPrereleaseChannelTag(name) {
			continue
		}
		pt := version.ParseTag(name)
		if pt.Scheme != version.SchemeUnknown && !pt.Prerelease {
			parsed = append(parsed, pt)
		} else if pt.Scheme != version.SchemeUnknown && pt.Prerelease {
			preParsed = append(preParsed, pt)
		} else if pt.Scheme == version.SchemeUnknown {
			aliases = append(aliases, name)
		}
	}
	sortSem := func(list []version.ParsedTag) {
		sort.SliceStable(list, func(i, j int) bool {
			iMulti := list[i].Precision >= 2
			jMulti := list[j].Precision >= 2
			if iMulti != jMulti {
				return iMulti
			}
			if cmp := version.Compare(list[i], list[j]); cmp != 0 {
				return cmp > 0
			}
			return list[i].Precision > list[j].Precision
		})
	}
	sortSem(parsed)
	sortSem(preParsed)

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
	for _, pt := range preParsed {
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
	Count   int    `json:"count"`
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

func decodeDockerHubTagPage(body []byte) ([]TagDigestEntry, string, int, error) {
	var payload dockerHubTagsPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, "", 0, err
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
	return out, payload.Next, payload.Count, nil
}

func decodeDockerHubTagEntries(body []byte) ([]TagDigestEntry, string, error) {
	items, next, _, err := decodeDockerHubTagPage(body)
	return items, next, err
}

func (c *Client) fetchDockerHubSinglePage(ctx context.Context, pageURL string) ([]TagDigestEntry, string, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, "", 0, &RegistryError{Kind: ErrNetwork, Message: i18n.EngineDetail(i18n.MsgRegistryNetwork, "Docker Hub", err.Error())}
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", 0, &RegistryError{Kind: ErrNetwork, Message: i18n.EngineDetail(i18n.MsgRegistryNetwork, "Docker Hub", err.Error())}
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", 0, classifyHTTPStatus("Docker Hub", resp.StatusCode)
	}
	items, next, count, err := decodeDockerHubTagPage(body)
	if err != nil {
		return nil, "", 0, &RegistryError{Kind: ErrRegistry, Message: i18n.EngineDetail(i18n.MsgRegistryParse, "Docker Hub")}
	}
	return items, next, count, nil
}

func (c *Client) fetchDockerHubTags(ctx context.Context, repository string, maxPages int) ([]TagDigestEntry, error) {
	hubBase := c.hubAPIBase()
	nextURL := fmt.Sprintf("%s/v2/repositories/%s/tags?page_size=100&ordering=last_updated", hubBase, repository)
	var out []TagDigestEntry

	for page := 0; page < maxPages && nextURL != ""; page++ {
		items, next, _, err := c.fetchDockerHubSinglePage(ctx, nextURL)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
		nextURL = next
	}
	return out, nil
}

func (c *Client) fetchDockerHubAllTags(ctx context.Context, repository string, maxPages int) ([]TagDigestEntry, error) {
	if maxPages <= 0 {
		maxPages = maxModalHubPages
	}
	hubBase := c.hubAPIBase()
	firstURL := fmt.Sprintf("%s/v2/repositories/%s/tags?page_size=100&ordering=last_updated", hubBase, repository)
	firstItems, nextURL, totalCount, err := c.fetchDockerHubSinglePage(ctx, firstURL)
	if err != nil {
		return nil, err
	}
	if nextURL == "" || maxPages <= 1 {
		return firstItems, nil
	}

	// 若未返回总条数但存在下一页链接，顺序跟随 next 指针
	if totalCount <= len(firstItems) {
		out := append([]TagDigestEntry(nil), firstItems...)
		curNext := nextURL
		for page := 1; page < maxPages && curNext != ""; page++ {
			items, nxt, _, pErr := c.fetchDockerHubSinglePage(ctx, curNext)
			if pErr != nil {
				break
			}
			out = append(out, items...)
			curNext = nxt
		}
		return mergeUniqueTagEntries(out), nil
	}

	totalPages := (totalCount + 99) / 100
	if totalPages > maxPages {
		totalPages = maxPages
	}
	if totalPages <= 1 {
		return firstItems, nil
	}

	pageBuckets := make([][]TagDigestEntry, totalPages+1)
	pageBuckets[1] = firstItems

	pCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()

	sem := make(chan struct{}, hubModalPageConcurrent)
	var wg sync.WaitGroup
	for p := 2; p <= totalPages; p++ {
		if pCtx.Err() != nil {
			break
		}
		acquired := false
		select {
		case sem <- struct{}{}:
			acquired = true
		case <-pCtx.Done():
		}
		if !acquired {
			break
		}
		wg.Add(1)
		go func(pageNum int) {
			defer wg.Done()
			defer func() { <-sem }()
			u := fmt.Sprintf("%s/v2/repositories/%s/tags?page_size=100&ordering=last_updated&page=%d", hubBase, repository, pageNum)
			items, _, _, pErr := c.fetchDockerHubSinglePage(pCtx, u)
			if pErr == nil && len(items) > 0 {
				pageBuckets[pageNum] = items
			}
		}(p)
	}
	wg.Wait()

	return mergeUniqueTagEntries(pageBuckets[1:]...), nil
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
		flightKey := "tok:" + tokenKey + "|" + www
		val, authErr := c.flight.do(flightKey, func() (any, error) {
			if cur := c.getCachedToken(tokenKey); cur != "" && cur != tok {
				return cur, nil
			}
			t, aErr := c.acquireBearerToken(ctx, www, creds)
			if aErr == nil && t != "" {
				c.setCachedToken(tokenKey, t)
			}
			return t, aErr
		})
		newTok, _ := val.(string)
		if authErr == nil && newTok != "" {
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

var ociLinkAnnotationKeys = []string{
	"org.opencontainers.image.source",
	"org.opencontainers.image.url",
	"org.opencontainers.image.documentation",
	"org.opencontainers.image.changelog",
	"org.label-schema.vcs-url",
	"org.label-schema.url",
	"org.label-schema.usage",
}

func parseLinkAnnotations(body []byte) map[string]string {
	var mf struct {
		Annotations map[string]string `json:"annotations"`
	}
	if json.Unmarshal(body, &mf) != nil || len(mf.Annotations) == 0 {
		return nil
	}
	return mergeLinkAnnotations(nil, mf.Annotations)
}

func mergeLinkAnnotations(base, extra map[string]string) map[string]string {
	if len(extra) == 0 {
		return base
	}
	var out map[string]string
	if len(base) > 0 {
		out = make(map[string]string, len(base))
		for k, v := range base {
			out[k] = v
		}
	}
	for _, k := range ociLinkAnnotationKeys {
		v := strings.TrimSpace(extra[k])
		if v == "" {
			continue
		}
		if out == nil {
			out = make(map[string]string, 4)
		}
		if out[k] == "" {
			out[k] = v
		}
	}
	return out
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
