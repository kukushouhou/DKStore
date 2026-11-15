// Package selfupdate 提供 DKStore 自身版本检测、镜像通道管理、断点续传下载、本地包提取及原地热切换自更新能力。
package selfupdate

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/kukushouhou/DKStore/internal/hostauth"
	"github.com/kukushouhou/DKStore/internal/i18n"
	"github.com/kukushouhou/DKStore/internal/notify"
	"github.com/kukushouhou/DKStore/internal/store"
	"github.com/kukushouhou/DKStore/internal/version"
)

// ManifestAsset 描述 latest.json 中单个平台的发行包信息。
type ManifestAsset struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// ReleaseManifest 是随 GitHub Releases 一并发布的静态版本元数据文件（latest.json）。
type ReleaseManifest struct {
	Version     string                   `json:"version"`
	PublishedAt string                   `json:"publishedAt,omitempty"`
	NotesZh     string                   `json:"notesZh,omitempty"`
	NotesEn     string                   `json:"notesEn,omitempty"`
	Assets      map[string]ManifestAsset `json:"assets,omitempty"`
}

// ApplyJob 描述正在执行或刚结束的自更新任务实时进度。
type ApplyJob struct {
	State           string `json:"state"`           // idle | running | restarting | succeeded | failed
	Phase           string `json:"phase"`           // downloading | verifying | installing | restarting | done
	Percent         int    `json:"percent"`         // 0..100
	DownloadedBytes int64  `json:"downloadedBytes"` // 已下载字节
	TotalBytes      int64  `json:"totalBytes"`      // 总字节
	TargetVersion   string `json:"targetVersion,omitempty"`
	Error           string `json:"error,omitempty"`
	UpdatedAt       int64  `json:"updatedAt"`
}

// ProbeResult 描述连通性探测结果。
type ProbeResult struct {
	State         string `json:"state"` // probing | ok | unreachable
	OK            bool   `json:"ok"`
	LatencyMs     int64  `json:"latencyMs,omitempty"`
	LatestVersion string `json:"latestVersion,omitempty"`
	EffectiveURL  string `json:"effectiveUrl,omitempty"`
	Error         string `json:"error,omitempty"`
}

// Status 描述 DKStore 当前版本、最新可用版本、通道配置及升级状态。
type Status struct {
	CurrentVersion  string    `json:"currentVersion"`
	Commit          string    `json:"commit"`
	LatestVersion   string    `json:"latestVersion"`
	HasUpdate       bool      `json:"hasUpdate"`
	InstallMode     string    `json:"installMode"`
	ReleaseURL      string    `json:"releaseUrl"`
	ReleaseNotes    string    `json:"releaseNotes,omitempty"`
	AssetName       string    `json:"assetName,omitempty"`
	AssetSize       int64     `json:"assetSize,omitempty"`
	PreserveNote    string    `json:"preserveNote"`
	CommandHint     string    `json:"commandHint"`
	Config          Config    `json:"config"`
	EffectiveURL    string    `json:"effectiveUrl"`
	DefaultRepo     string    `json:"defaultRepo"`
	OfficialMirror  string    `json:"officialMirror"`
	PresetMirrors   []string  `json:"presetMirrors"`
	RolledBackError string    `json:"rolledBackError,omitempty"`
	CheckError      string    `json:"checkError,omitempty"`
	Job             *ApplyJob `json:"job,omitempty"`
}

type resolvedRelease struct {
	TagName      string
	HTMLURL      string
	NotesZh      string
	NotesEn      string
	NotesFallback string
	AssetName    string
	AssetSize    int64
	AssetSHA256  string
}

// Service 管理商店自更新配置、探测缓存与在线/离线热更新流程。
type Service struct {
	log        *slog.Logger
	st         *store.Store
	dataDir    string
	httpClient *http.Client
	reloadFn   func(execPath string)
	ntf        *notify.Service
	isBusy     func() bool
	wakeupCh   chan struct{}

	schedMu        sync.Mutex
	autoFailVer    string
	autoFailAt     time.Time

	cacheMu     sync.Mutex
	cachedRel   *resolvedRelease
	cachedKey   string
	cachedUntil time.Time

	probeMu         sync.Mutex
	probeRunning    bool
	probeState      string // "" | "probing" | "ok" | "unreachable"
	probeLatencyMs  int64
	probeLatestVer  string
	probeFinishedAt time.Time

	jobMu sync.Mutex
	job   ApplyJob
}

// New 创建自更新服务。
func New(log *slog.Logger, st *store.Store, dataDir string, reloadFn func(execPath string)) *Service {
	return &Service{
		log:        log,
		st:         st,
		dataDir:    dataDir,
		httpClient: &http.Client{Timeout: 12 * time.Second},
		reloadFn:   reloadFn,
		wakeupCh:   make(chan struct{}, 1),
		job:        ApplyJob{State: "idle"},
	}
}

// SetAutoUpdateDeps 注入通知推送服务与系统忙碌判定回调（用于自动更新策略与仅推送提醒策略）。
func (s *Service) SetAutoUpdateDeps(ntf *notify.Service, isBusy func() bool) {
	s.ntf = ntf
	s.isBusy = isBusy
}

// LoadConfig 从 SQLite setting 表读取当前自更新配置（不存在时返回默认 GitHub 配置）。
func (s *Service) LoadConfig(ctx context.Context) Config {
	def := DefaultConfig()
	if s == nil || s.st == nil || s.st.DB == nil {
		return def
	}
	var raw string
	err := s.st.DB.QueryRowContext(ctx, `SELECT v FROM setting WHERE k=?`, settingKeyConfig).Scan(&raw)
	if err != nil || strings.TrimSpace(raw) == "" {
		return def
	}
	var cfg Config
	if json.Unmarshal([]byte(raw), &cfg) != nil {
		return def
	}
	norm, err := NormalizeAndValidate(cfg, false)
	if err != nil {
		return def
	}
	return norm
}

// SaveConfig 校验并保存自更新配置，同时清空版本检查缓存以便下次立即按新源获取。
func (s *Service) SaveConfig(ctx context.Context, in Config) (Config, error) {
	norm, err := NormalizeAndValidate(in, true)
	if err != nil {
		return Config{}, err
	}
	if s.st != nil && s.st.DB != nil {
		b, err := json.Marshal(norm)
		if err != nil {
			return Config{}, err
		}
		if _, err := s.st.DB.ExecContext(ctx, `
			INSERT INTO setting(k, v) VALUES (?, ?)
			ON CONFLICT(k) DO UPDATE SET v=excluded.v`, settingKeyConfig, string(b)); err != nil {
			return Config{}, err
		}
	}
	s.invalidateCache()
	if s.wakeupCh != nil && (norm.Policy == PolicyAuto || norm.Policy == PolicyNotify) {
		select {
		case s.wakeupCh <- struct{}{}:
		default:
		}
	}
	return norm, nil
}

type upgradedNoticePayload struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// StartScheduler 启动商店版本后台巡检循环，按用户选定的策略执行「自动更新」或「仅推送提醒」。
func (s *Service) StartScheduler(ctx context.Context) {
	if s == nil || s.st == nil {
		return
	}
	go func() {
		// 启动后先检查上次自动更新是否已完成切换，若是则发送升级完成通知
		select {
		case <-ctx.Done():
			return
		case <-time.After(6 * time.Second):
		}
		s.flushPendingUpgradedNotice(ctx)

		// 首次后台商店版本巡检延迟 35 秒，避开启动高峰
		select {
		case <-ctx.Done():
			return
		case <-s.wakeupCh:
		case <-time.After(35 * time.Second):
		}
		nextWait := s.runScheduledPolicyCheck(ctx)

		timer := time.NewTimer(nextWait)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.wakeupCh:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				nextWait = s.runScheduledPolicyCheck(ctx)
				timer.Reset(nextWait)
			case <-timer.C:
				nextWait = s.runScheduledPolicyCheck(ctx)
				timer.Reset(nextWait)
			}
		}
	}()
}

func (s *Service) checkInterval(ctx context.Context) time.Duration {
	if s.ntf != nil {
		if h := s.ntf.LoadConfig(ctx).CheckIntervalHours; h > 0 {
			return time.Duration(h) * time.Hour
		}
	}
	return 6 * time.Hour
}

func (s *Service) flushPendingUpgradedNotice(ctx context.Context) {
	if s.st == nil || s.st.DB == nil {
		return
	}
	var raw string
	if err := s.st.DB.QueryRowContext(ctx, `SELECT v FROM setting WHERE k=?`, settingKeyUpgradedNotify).Scan(&raw); err != nil || raw == "" {
		return
	}
	var p upgradedNoticePayload
	if json.Unmarshal([]byte(raw), &p) != nil || p.To == "" {
		_, _ = s.st.DB.ExecContext(ctx, `DELETE FROM setting WHERE k=?`, settingKeyUpgradedNotify)
		return
	}
	// 仅当当前运行版本确实已切换至目标版本时才发送升级成功通知
	cur := ensureVPrefix(version.Version)
	target := ensureVPrefix(p.To)
	if cur != "" && target != "" && !strings.EqualFold(cur, target) {
		_, _ = s.st.DB.ExecContext(ctx, `DELETE FROM setting WHERE k=?`, settingKeyUpgradedNotify)
		return
	}
	if s.ntf != nil {
		err := s.ntf.SendMessage(
			ctx,
			fmt.Sprintf("DKStore 商店已自动更新至 %s", target),
			fmt.Sprintf("商店已从 %s 自动升级至 %s 并正常运行。", nonEmpty(p.From, "旧版本"), target),
			fmt.Sprintf("DKStore Auto-Updated to %s", target),
			fmt.Sprintf("DKStore has automatically updated from %s to %s and is running normally.", nonEmpty(p.From, "previous version"), target),
		)
		if errors.Is(err, notify.ErrQuietHours) {
			return
		}
	}
	_, _ = s.st.DB.ExecContext(ctx, `DELETE FROM setting WHERE k=?`, settingKeyUpgradedNotify)
}

func (s *Service) runScheduledPolicyCheck(ctx context.Context) time.Duration {
	s.flushPendingUpgradedNotice(ctx)
	interval := s.checkInterval(ctx)

	lang := i18n.ZhCN
	if s.ntf != nil {
		lang = i18n.Normalize(s.ntf.LoadConfig(ctx).Locale)
	}
	st := s.CheckStatus(ctx, lang, true)
	if !st.HasUpdate || st.LatestVersion == "" {
		return interval
	}

	switch st.Config.Policy {
	case PolicyManual:
		// 手动更新：已完成检测并更新缓存，不发通知推送、不自动升级
		return interval

	case PolicyNotify:
		if s.ntf == nil || s.st == nil {
			return interval
		}
		var notifiedVer string
		_ = s.st.DB.QueryRowContext(ctx, `SELECT v FROM setting WHERE k=?`, settingKeyNotifiedVer).Scan(&notifiedVer)
		if strings.EqualFold(strings.TrimSpace(notifiedVer), strings.TrimSpace(st.LatestVersion)) {
			return interval
		}
		err := s.ntf.SendMessage(
			ctx,
			fmt.Sprintf("DKStore 商店发现新版本 %s", st.LatestVersion),
			fmt.Sprintf("当前商店版本 %s，可前往「设置」一键更新。", st.CurrentVersion),
			fmt.Sprintf("DKStore Update Available: %s", st.LatestVersion),
			fmt.Sprintf("Current store version is %s. Open Settings to update.", st.CurrentVersion),
		)
		if errors.Is(err, notify.ErrQuietHours) {
			return 30 * time.Minute
		}
		if err == nil {
			_, _ = s.st.DB.ExecContext(ctx, `
				INSERT INTO setting(k, v) VALUES (?, ?)
				ON CONFLICT(k) DO UPDATE SET v=excluded.v`, settingKeyNotifiedVer, st.LatestVersion)
		}
		return interval

	case PolicyAuto:
		if s.isBusy != nil && s.isBusy() {
			// 当前有容器升级或巡检正在执行，延后 2 分钟待空闲后再自动升级
			return 2 * time.Minute
		}
		s.schedMu.Lock()
		recentlyFailed := strings.EqualFold(s.autoFailVer, st.LatestVersion) && time.Since(s.autoFailAt) < 2*time.Hour
		s.schedMu.Unlock()
		if recentlyFailed {
			return interval
		}
		_, _ = s.applyOnlineInternal(ctx, lang, true)
		return interval
	}
	return interval
}

func (s *Service) invalidateCache() {
	s.cacheMu.Lock()
	s.cachedRel = nil
	s.cachedKey = ""
	s.cachedUntil = time.Time{}
	s.cacheMu.Unlock()
}

// StartDefaultGitHubProbe 在初始化向导第 1 步加载时后台启动充裕的 GitHub 连通性探测（2 次重试、总窗口约 16s）。
func (s *Service) StartDefaultGitHubProbe() ProbeResult {
	s.probeMu.Lock()
	if s.probeRunning {
		s.probeMu.Unlock()
		return ProbeResult{
			State:        "probing",
			EffectiveURL: "https://github.com/" + DefaultRepoSlug(),
		}
	}
	if s.probeState != "" && time.Since(s.probeFinishedAt) < 3*time.Minute {
		res := ProbeResult{
			State:         s.probeState,
			OK:            s.probeState == "ok",
			LatencyMs:     s.probeLatencyMs,
			LatestVersion: s.probeLatestVer,
			EffectiveURL:  "https://github.com/" + DefaultRepoSlug(),
		}
		s.probeMu.Unlock()
		return res
	}
	s.probeRunning = true
	s.probeState = "probing"
	s.probeMu.Unlock()

	go func() {
		defCfg := DefaultConfig()
		baseURL := EffectiveBaseURL(defCfg)
		var lastErr error
		for attempt := 0; attempt < 2; attempt++ {
			if attempt > 0 {
				time.Sleep(500 * time.Millisecond)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			start := time.Now()
			rel, err := s.fetchReleaseInfo(ctx, baseURL, defCfg.Repo, defCfg.Channel, 8*time.Second)
			dur := time.Since(start).Milliseconds()
			cancel()
			if err == nil && rel != nil && rel.TagName != "" {
				s.probeMu.Lock()
				s.probeRunning = false
				s.probeState = "ok"
				s.probeLatencyMs = dur
				s.probeLatestVer = rel.TagName
				s.probeFinishedAt = time.Now()
				s.probeMu.Unlock()
				return
			}
			lastErr = err
		}
		_ = lastErr
		s.probeMu.Lock()
		s.probeRunning = false
		s.probeState = "unreachable"
		s.probeLatencyMs = 0
		s.probeFinishedAt = time.Now()
		s.probeMu.Unlock()
	}()

	return ProbeResult{
		State:        "probing",
		EffectiveURL: "https://github.com/" + DefaultRepoSlug(),
	}
}

// GetDefaultGitHubProbe 查询后台默认 GitHub 探测的当前状态（若尚未启动则立即触发）。
func (s *Service) GetDefaultGitHubProbe() ProbeResult {
	s.probeMu.Lock()
	state := s.probeState
	running := s.probeRunning
	lat := s.probeLatencyMs
	ver := s.probeLatestVer
	fin := s.probeFinishedAt
	s.probeMu.Unlock()

	if state == "" || (!running && time.Since(fin) >= 3*time.Minute) {
		return s.StartDefaultGitHubProbe()
	}
	return ProbeResult{
		State:         state,
		OK:            state == "ok",
		LatencyMs:     lat,
		LatestVersion: ver,
		EffectiveURL:  "https://github.com/" + DefaultRepoSlug(),
	}
}

// TestSource 现场测试指定自更新配置的连通性与延迟。
func (s *Service) TestSource(ctx context.Context, in Config, lang i18n.Lang) (ProbeResult, error) {
	norm, err := NormalizeAndValidate(in, true)
	if err != nil {
		return ProbeResult{}, localizeConfigError(lang, err)
	}
	baseURL := EffectiveBaseURL(norm)
	tCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	start := time.Now()
	rel, err := s.fetchReleaseInfo(tCtx, baseURL, norm.Repo, norm.Channel, 10*time.Second)
	dur := time.Since(start).Milliseconds()
	if err != nil {
		msg := localizeProbeError(lang, err)
		return ProbeResult{
			State:        "unreachable",
			OK:           false,
			EffectiveURL: baseURL,
			Error:        msg,
		}, errors.New(msg)
	}
	return ProbeResult{
		State:         "ok",
		OK:            true,
		LatencyMs:     dur,
		LatestVersion: rel.TagName,
		EffectiveURL:  baseURL,
	}, nil
}

// CheckStatus 获取完整的自更新状态（支持缓存或强制刷新）。
func (s *Service) CheckStatus(ctx context.Context, lang i18n.Lang, bypassCache bool) Status {
	cur := strings.TrimSpace(version.Version)
	if cur == "" {
		cur = "v0.3.0"
	}
	mode := detectInstallMode()
	cfg := s.LoadConfig(ctx)
	baseURL := EffectiveBaseURL(cfg)
	releaseURL := "https://github.com/" + cfg.Repo + "/releases"

	var rel *resolvedRelease
	var checkErr error

	cacheKey := baseURL + "|" + runtime.GOOS + "/" + runtime.GOARCH
	s.cacheMu.Lock()
	if !bypassCache && s.cachedRel != nil && s.cachedKey == cacheKey && time.Now().Before(s.cachedUntil) {
		cp := *s.cachedRel
		rel = &cp
	}
	s.cacheMu.Unlock()

	if rel == nil {
		cCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		fetched, err := s.fetchReleaseInfo(cCtx, baseURL, cfg.Repo, cfg.Channel, 10*time.Second)
		cancel()
		if err == nil && fetched != nil {
			rel = fetched
			s.cacheMu.Lock()
			cp := *fetched
			s.cachedRel = &cp
			s.cachedKey = cacheKey
			s.cachedUntil = time.Now().Add(5 * time.Minute)
			s.cacheMu.Unlock()
		} else {
			checkErr = err
		}
	}

	latest := cur
	hasUpdate := false
	var notes, assetName string
	var assetSize int64

	if rel != nil && rel.TagName != "" {
		latest = rel.TagName
		if rel.HTMLURL != "" {
			releaseURL = rel.HTMLURL
		} else {
			releaseURL = "https://github.com/" + cfg.Repo + "/releases/tag/" + rel.TagName
		}
		if lang == i18n.EnUS {
			notes = firstNonEmpty(rel.NotesEn, rel.NotesFallback, rel.NotesZh)
		} else {
			notes = firstNonEmpty(rel.NotesZh, rel.NotesFallback, rel.NotesEn)
		}
		assetName = rel.AssetName
		assetSize = rel.AssetSize

		pCur := version.ParseTag(cur)
		pLat := version.ParseTag(latest)
		if pCur.Scheme != version.SchemeUnknown && pLat.Scheme != version.SchemeUnknown &&
			version.Compare(pLat, pCur) > 0 {
			hasUpdate = true
		}
	}

	note, cmdHint := localizedHints(mode, lang)
	var checkErrStr string
	if checkErr != nil {
		checkErrStr = localizeProbeError(lang, checkErr)
	}

	s.jobMu.Lock()
	var jobPtr *ApplyJob
	if s.job.State != "" && s.job.State != "idle" {
		cp := s.job
		jobPtr = &cp
	}
	s.jobMu.Unlock()

	return Status{
		CurrentVersion:  cur,
		Commit:          version.Commit,
		LatestVersion:   latest,
		HasUpdate:       hasUpdate,
		InstallMode:     mode,
		ReleaseURL:      releaseURL,
		ReleaseNotes:    notes,
		AssetName:       assetName,
		AssetSize:       assetSize,
		PreserveNote:    note,
		CommandHint:     cmdHint,
		Config:          cfg,
		EffectiveURL:    baseURL,
		DefaultRepo:     DefaultRepoSlug(),
		OfficialMirror:  ActiveOfficialMirrorHost(),
		PresetMirrors:   append([]string(nil), PresetMirrors...),
		RolledBackError: RolledBackNote(s.dataDir),
		CheckError:      checkErrStr,
		Job:             jobPtr,
	}
}

// Check 保留包级快捷检查函数以兼容旧调用方与单测。
func Check(ctx context.Context, lang i18n.Lang) Status {
	svc := New(nil, nil, "", nil)
	return svc.CheckStatus(ctx, lang, true)
}

var shaLineVerRe = regexp.MustCompile(`^dkstore-(?:linux-(?:amd64|arm64|armv7)|windows-amd64)-([0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?)\.(?:tar\.gz|zip)$`)

func platformAssetKey(goos, goarch string) string {
	if goos == "windows" {
		return "windows-" + goarch
	}
	if goos == "linux" && goarch == "arm" {
		return "linux-armv7"
	}
	return goos + "-" + goarch
}

func expectedArchivePrefixAndExt(goos, goarch string) (prefix, ext string) {
	if goos == "windows" {
		return "dkstore-windows-" + goarch + "-", ".zip"
	}
	archKey := goarch
	if goarch == "arm" {
		archKey = "armv7"
	}
	return "dkstore-linux-" + archKey + "-", ".tar.gz"
}

func (s *Service) fetchReleaseInfo(ctx context.Context, baseURL, repo, channel string, timeout time.Duration) (*resolvedRelease, error) {
	hc := &http.Client{Timeout: timeout}
	platKey := platformAssetKey(runtime.GOOS, runtime.GOARCH)

	// 1. 优先尝试静态 latest.json（无 API 限流，天然兼容所有前缀/换域名镜像）
	manifestURL := baseURL + "/releases/latest/download/latest.json"
	if req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil); err == nil {
		req.Header.Set("User-Agent", "DKStore-SelfUpdate")
		if resp, err := hc.Do(req); err == nil {
			body, rErr := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
			resp.Body.Close()
			if rErr == nil && resp.StatusCode == http.StatusOK {
				var mf ReleaseManifest
				if json.Unmarshal(body, &mf) == nil && strings.TrimSpace(mf.Version) != "" {
					ver := ensureVPrefix(mf.Version)
					out := &resolvedRelease{
						TagName: ver,
						HTMLURL: "https://github.com/" + repo + "/releases/tag/" + ver,
						NotesZh: mf.NotesZh,
						NotesEn: mf.NotesEn,
					}
					if a, ok := mf.Assets[platKey]; ok {
						out.AssetName = a.Name
						out.AssetSize = a.Size
						out.AssetSHA256 = a.SHA256
					}
					return out, nil
				}
			}
		}
	}

	// 2. 兼容未包含 latest.json 的已有发行版（如 v0.3.0）或分叉仓库：直接读取 SHA256SUMS.txt
	sumsURL := baseURL + "/releases/latest/download/SHA256SUMS.txt"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sumsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "DKStore-SelfUpdate")
	resp, err := hc.Do(req)
	if err == nil {
		body, rErr := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
		resp.Body.Close()
		if rErr == nil && resp.StatusCode == http.StatusOK {
			if parsed := parseSHA256Sums(string(body), repo, runtime.GOOS, runtime.GOARCH); parsed != nil {
				// 直连 GitHub 时顺带尝试快速补全 Release Notes（失败/限流直接忽略）
				if channel == ChannelGitHub {
					s.tryEnrichGitHubReleaseNotes(ctx, repo, parsed)
				}
				return parsed, nil
			}
		}
	}

	// 3. 若自定义 Fork 仅发布了 Tag 但未生成 SHA256SUMS.txt，直连 GitHub 时兜底查询 API
	if channel == ChannelGitHub {
		apiURL := "https://api.github.com/repos/" + repo + "/releases/latest"
		if apiReq, aErr := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil); aErr == nil {
			apiReq.Header.Set("Accept", "application/vnd.github+json")
			apiReq.Header.Set("User-Agent", "DKStore-SelfUpdate")
			if apiResp, dErr := hc.Do(apiReq); dErr == nil {
				defer apiResp.Body.Close()
				if apiResp.StatusCode == http.StatusOK {
					var ghRel struct {
						TagName string `json:"tag_name"`
						HTMLURL string `json:"html_url"`
						Body    string `json:"body"`
						Assets  []struct {
							Name string `json:"name"`
							Size int64  `json:"size"`
						} `json:"assets"`
					}
					if json.NewDecoder(apiResp.Body).Decode(&ghRel) == nil && ghRel.TagName != "" {
						out := &resolvedRelease{
							TagName:       ensureVPrefix(ghRel.TagName),
							HTMLURL:       ghRel.HTMLURL,
							NotesFallback: strings.TrimSpace(ghRel.Body),
						}
						pfx, ext := expectedArchivePrefixAndExt(runtime.GOOS, runtime.GOARCH)
						for _, a := range ghRel.Assets {
							if strings.HasPrefix(a.Name, pfx) && strings.HasSuffix(a.Name, ext) {
								out.AssetName = a.Name
								out.AssetSize = a.Size
								break
							}
						}
						return out, nil
					}
				}
			}
		}
	}

	if err != nil {
		return nil, err
	}
	return nil, errors.New("unable to fetch valid release metadata from source")
}

func parseSHA256Sums(content, repo, goos, goarch string) *resolvedRelease {
	pfx, ext := expectedArchivePrefixAndExt(goos, goarch)
	sc := bufio.NewScanner(strings.NewReader(content))
	var detectedVer, matchedName, matchedSHA string
	validLines := 0

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || len(fields[0]) != 64 || !isHex(fields[0]) {
			continue
		}
		hash := strings.ToLower(fields[0])
		name := strings.TrimPrefix(fields[1], "*")
		validLines++

		if m := shaLineVerRe.FindStringSubmatch(name); len(m) == 2 && detectedVer == "" {
			detectedVer = "v" + m[1]
		}
		if strings.HasPrefix(name, pfx) && strings.HasSuffix(name, ext) {
			matchedName = name
			matchedSHA = hash
		}
	}
	if validLines == 0 || detectedVer == "" {
		return nil
	}
	return &resolvedRelease{
		TagName:     detectedVer,
		HTMLURL:     "https://github.com/" + repo + "/releases/tag/" + detectedVer,
		AssetName:   matchedName,
		AssetSHA256: matchedSHA,
	}
}

func (s *Service) tryEnrichGitHubReleaseNotes(parentCtx context.Context, repo string, target *resolvedRelease) {
	ctx, cancel := context.WithTimeout(parentCtx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "DKStore-SelfUpdate")
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	var ghRel struct {
		Body   string `json:"body"`
		Assets []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		} `json:"assets"`
	}
	if json.NewDecoder(resp.Body).Decode(&ghRel) == nil {
		target.NotesFallback = strings.TrimSpace(ghRel.Body)
		for _, a := range ghRel.Assets {
			if a.Name == target.AssetName && a.Size > 0 {
				target.AssetSize = a.Size
				break
			}
		}
	}
}

// ApplyOnline 启动后台异步在线下载、SHA-256 校验并执行原地热更新。
func (s *Service) ApplyOnline(ctx context.Context, lang i18n.Lang) (ApplyJob, error) {
	return s.applyOnlineInternal(ctx, lang, false)
}

func (s *Service) applyOnlineInternal(ctx context.Context, lang i18n.Lang, isAuto bool) (ApplyJob, error) {
	s.jobMu.Lock()
	if s.job.State == "running" || s.job.State == "restarting" {
		cp := s.job
		s.jobMu.Unlock()
		return cp, nil
	}
	s.job = ApplyJob{
		State:     "running",
		Phase:     "downloading",
		Percent:   2,
		UpdatedAt: time.Now().Unix(),
	}
	cp := s.job
	s.jobMu.Unlock()

	cfg := s.LoadConfig(ctx)
	baseURL := EffectiveBaseURL(cfg)

	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		markAutoFail := func(ver string) {
			if !isAuto {
				return
			}
			s.schedMu.Lock()
			s.autoFailVer = ver
			s.autoFailAt = time.Now()
			s.schedMu.Unlock()
		}

		rel, err := s.fetchReleaseInfo(bgCtx, baseURL, cfg.Repo, cfg.Channel, 15*time.Second)
		if err != nil || rel == nil || rel.AssetName == "" {
			if rel != nil {
				markAutoFail(rel.TagName)
			}
			s.failJob(localizeApplyError(lang, firstErr(err, errors.New("no matching package for current platform"))))
			return
		}
		s.updateJob(func(j *ApplyJob) {
			j.TargetVersion = rel.TagName
			j.TotalBytes = rel.AssetSize
			j.Percent = 5
		})

		dlDir := filepath.Join(s.stagingBaseDir(), "bin")
		if mkErr := os.MkdirAll(dlDir, 0o755); mkErr != nil {
			markAutoFail(rel.TagName)
			s.failJob(mkErr.Error())
			return
		}
		partPath := filepath.Join(dlDir, ".download-"+rel.AssetName+".part")
		defer os.Remove(partPath)

		dlURL := baseURL + "/releases/download/" + rel.TagName + "/" + rel.AssetName
		if err := s.downloadWithResume(bgCtx, dlURL, partPath, rel.AssetSHA256, rel.AssetSize); err != nil {
			markAutoFail(rel.TagName)
			s.failJob(localizeApplyError(lang, err))
			return
		}

		s.updateJob(func(j *ApplyJob) {
			j.Phase = "verifying"
			j.Percent = 85
		})

		binBytes, err := ExtractBinaryFromFile(partPath, rel.AssetName)
		if err != nil {
			markAutoFail(rel.TagName)
			s.failJob(localizeApplyError(lang, err))
			return
		}
		if err := s.verifyAndInstallBinary(binBytes, rel.TagName, lang, isAuto); err != nil {
			markAutoFail(rel.TagName)
			s.failJob(err.Error())
			return
		}
	}()

	return cp, nil
}

// ApplyUpload 从用户上传的本地更新包流中提取二进制、核验架构与可执行性并原地切换。
func (s *Service) ApplyUpload(r io.Reader, filename string, lang i18n.Lang) (ApplyJob, error) {
	s.jobMu.Lock()
	if s.job.State == "running" || s.job.State == "restarting" {
		s.jobMu.Unlock()
		return ApplyJob{}, errors.New(localizedBusyMsg(lang))
	}
	s.job = ApplyJob{
		State:     "running",
		Phase:     "verifying",
		Percent:   50,
		UpdatedAt: time.Now().Unix(),
	}
	s.jobMu.Unlock()

	dlDir := filepath.Join(s.stagingBaseDir(), "bin")
	if err := os.MkdirAll(dlDir, 0o755); err != nil {
		s.failJob(err.Error())
		return ApplyJob{}, err
	}
	tmpPkg, err := os.CreateTemp(dlDir, ".upload-*")
	if err != nil {
		s.failJob(err.Error())
		return ApplyJob{}, err
	}
	tmpPkgPath := tmpPkg.Name()
	defer os.Remove(tmpPkgPath)

	written, cpErr := io.Copy(tmpPkg, io.LimitReader(r, maxExtractedBinaryBytes))
	_ = tmpPkg.Close()
	if cpErr != nil || written == 0 {
		msg := localizeApplyError(lang, firstErr(cpErr, ErrUnsupportedPackage))
		s.failJob(msg)
		return ApplyJob{}, errors.New(msg)
	}

	binBytes, err := ExtractBinaryFromFile(tmpPkgPath, filename)
	if err != nil {
		msg := localizeApplyError(lang, err)
		s.failJob(msg)
		return ApplyJob{}, errors.New(msg)
	}

	if err := s.verifyAndInstallBinary(binBytes, "", lang, false); err != nil {
		s.failJob(err.Error())
		return ApplyJob{}, err
	}

	s.jobMu.Lock()
	cp := s.job
	s.jobMu.Unlock()
	return cp, nil
}

func (s *Service) stagingBaseDir() string {
	if s.dataDir != "" {
		return s.dataDir
	}
	return os.TempDir()
}

func (s *Service) downloadWithResume(ctx context.Context, url, destPath, expectedSHA256 string, knownTotal int64) error {
	_ = os.Remove(destPath)
	hc := &http.Client{Timeout: 0} // 由请求级 Context 控制总超时，避免慢速流被固定短超时截断

	var offset int64
	total := knownTotal

	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}

		if st, err := os.Stat(destPath); err == nil {
			offset = st.Size()
		} else {
			offset = 0
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", "DKStore-SelfUpdate")
		if offset > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		}

		resp, err := hc.Do(req)
		if err != nil {
			if attempt == 3 {
				return err
			}
			continue
		}

		var out *os.File
		switch resp.StatusCode {
		case http.StatusPartialContent:
			if total <= 0 && resp.ContentLength > 0 {
				total = offset + resp.ContentLength
			}
			out, err = os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		case http.StatusOK:
			offset = 0
			if resp.ContentLength > 0 {
				total = resp.ContentLength
			}
			out, err = os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		case http.StatusRequestedRangeNotSatisfiable:
			resp.Body.Close()
			if total > 0 && offset >= total {
				attempt = 4
				continue
			}
			_ = os.Remove(destPath)
			continue
		default:
			code := resp.StatusCode
			resp.Body.Close()
			if code >= 500 && attempt < 3 {
				continue
			}
			return fmt.Errorf("download failed with HTTP %d", code)
		}
		if err != nil {
			resp.Body.Close()
			return err
		}

		buf := make([]byte, 32*1024)
		var copyErr error
		for {
			n, rErr := resp.Body.Read(buf)
			if n > 0 {
				if _, wErr := out.Write(buf[:n]); wErr != nil {
					copyErr = wErr
					break
				}
				offset += int64(n)
				s.updateJob(func(j *ApplyJob) {
					j.DownloadedBytes = offset
					if total > 0 {
						j.TotalBytes = total
						pct := int(float64(offset) / float64(total) * 78.0) + 5
						if pct > 83 {
							pct = 83
						}
						j.Percent = pct
					}
				})
			}
			if errors.Is(rErr, io.EOF) {
				break
			}
			if rErr != nil {
				copyErr = rErr
				break
			}
		}
		_ = out.Close()
		resp.Body.Close()

		if copyErr == nil {
			break
		}
		if attempt == 3 {
			return copyErr
		}
	}

	if expectedSHA256 != "" {
		actual, err := fileSHA256(destPath)
		if err != nil {
			return err
		}
		if !strings.EqualFold(actual, expectedSHA256) {
			return errors.New("SHA-256 checksum mismatch")
		}
	}
	return nil
}

func (s *Service) verifyAndInstallBinary(binBytes []byte, knownVersion string, lang i18n.Lang, isAuto bool) error {
	if err := verifyBinaryArch(binBytes, runtime.GOOS, runtime.GOARCH); err != nil {
		return errors.New(localizeApplyError(lang, err))
	}

	s.updateJob(func(j *ApplyJob) {
		j.Phase = "installing"
		j.Percent = 92
	})

	sum := sha256.Sum256(binBytes)
	binHash := hex.EncodeToString(sum[:])

	binDir := filepath.Join(s.stagingBaseDir(), "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}

	// 先落临时可执行文件并试运行 --print-version；若数据目录挂载了 noexec，自动回退至系统/容器内可执行目录校验并运行
	probeTmp := filepath.Join(binDir, fmt.Sprintf(".dkstore-probe-%d", time.Now().UnixNano()))
	if runtime.GOOS == "windows" {
		probeTmp += ".exe"
	}
	if err := os.WriteFile(probeTmp, binBytes, 0o755); err != nil {
		return err
	}
	detectedVer, execErr := probeBinaryVersion(probeTmp)
	_ = os.Remove(probeTmp)

	var noexecFallbackPath string
	if execErr != nil && runtime.GOOS != "windows" && errors.Is(execErr, os.ErrPermission) {
		outerExe := os.Getenv(envBaseExecutable)
		if outerExe == "" {
			outerExe, _ = os.Executable()
		}
		if fbPath, fbVer, fbErr := stageExecFallbackCopy(binBytes, outerExe); fbErr == nil {
			noexecFallbackPath = fbPath
			detectedVer = fbVer
			execErr = nil
		}
	}
	if execErr != nil {
		return errors.New(localizeApplyError(lang, execErr))
	}
	if knownVersion == "" && detectedVer != "" {
		knownVersion = detectedVer
	}

	execTarget, err := s.commitBinaryToDisk(binBytes, binHash, knownVersion, noexecFallbackPath)
	if err != nil {
		return errors.New(localizeApplyError(lang, err))
	}

	if isAuto && s.st != nil && s.st.DB != nil && knownVersion != "" {
		if b, mErr := json.Marshal(upgradedNoticePayload{
			From: strings.TrimSpace(version.Version),
			To:   knownVersion,
		}); mErr == nil {
			_, _ = s.st.DB.ExecContext(context.Background(), `
				INSERT INTO setting(k, v) VALUES (?, ?)
				ON CONFLICT(k) DO UPDATE SET v=excluded.v`, settingKeyUpgradedNotify, string(b))
		}
	}

	s.updateJob(func(j *ApplyJob) {
		j.State = "restarting"
		j.Phase = "restarting"
		j.Percent = 100
		if knownVersion != "" {
			j.TargetVersion = knownVersion
		}
	})

	if s.reloadFn != nil {
		fn := s.reloadFn
		go func() {
			time.Sleep(450 * time.Millisecond)
			fn(execTarget)
		}()
	}
	return nil
}

func probeBinaryVersion(binPath string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binPath, "--print-version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		var pathErr *os.PathError
		if errors.As(err, &pathErr) {
			return "", fmt.Errorf("binary cannot be executed on this filesystem: %w", err)
		}
		// 兼容未实现 --print-version 的旧版二进制（如回退测试 v0.3.0）：只要成功拉起进程并打印 flag 提示即代表可执行
		outStr := string(out)
		if strings.Contains(outStr, "flag provided but not defined") {
			return "", nil
		}
		return "", fmt.Errorf("binary preflight check failed: %w", err)
	}
	line := strings.TrimSpace(strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n")[0])
	return line, nil
}

func (s *Service) commitBinaryToDisk(binBytes []byte, binHash, targetVersion, noexecFallbackPath string) (string, error) {
	mode := detectInstallMode()
	exePath, _ := os.Executable()

	// Windows：重命名当前运行中的 .exe 为 .exe.old，写入新版并返回新路径由外部拉起
	if runtime.GOOS == "windows" && exePath != "" {
		oldPath := exePath + ".old"
		_ = os.Remove(oldPath)
		tmpNew := exePath + ".new"
		if err := os.WriteFile(tmpNew, binBytes, 0o755); err != nil {
			return "", err
		}
		if err := os.Rename(exePath, oldPath); err != nil {
			_ = os.Remove(tmpNew)
			return "", err
		}
		if err := os.Rename(tmpNew, exePath); err != nil {
			_ = os.Rename(oldPath, exePath)
			_ = os.Remove(tmpNew)
			return "", err
		}
		return exePath, nil
	}

	// 飞牛 / OpenWrt / Linux 独立服务：优先原地原子替换主程序（节省小闪存空间）；若无写权限则自动回落至 <DataDir>/bin
	if mode != hostauth.ModeDocker && mode != hostauth.ModeSynology && exePath != "" {
		realExe := exePath
		if resolved, err := filepath.EvalSymlinks(exePath); err == nil && resolved != "" {
			realExe = resolved
		}
		// 若当前本身跑在 <DataDir>/bin/dkstore，则继续更新 <DataDir>/bin/dkstore
		overrideBin := OverrideBinaryPath(s.stagingBaseDir())
		if !sameFilePath(realExe, overrideBin) {
			tmpInPlace := filepath.Join(filepath.Dir(realExe), fmt.Sprintf(".dkstore-new-%d", os.Getpid()))
			if err := os.WriteFile(tmpInPlace, binBytes, 0o755); err == nil {
				if err := os.Rename(tmpInPlace, realExe); err == nil {
					_ = os.Remove(overrideBin)
					_ = os.Remove(overrideStatePath(s.stagingBaseDir()))
					return realExe, nil
				}
				_ = os.Remove(tmpInPlace)
			}
		}
	}

	// Docker 容器 & 群晖非 root 套件（以及主程序目录只读环境）：写入持久化 <DataDir>/bin/dkstore 并记录状态
	baseDir := s.stagingBaseDir()
	overrideBin := OverrideBinaryPath(baseDir)
	if err := os.MkdirAll(filepath.Dir(overrideBin), 0o755); err != nil {
		return "", err
	}
	tmpOverride := overrideBin + ".new"
	if err := os.WriteFile(tmpOverride, binBytes, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(tmpOverride, overrideBin); err != nil {
		_ = os.Remove(tmpOverride)
		return "", err
	}

	outerExe := os.Getenv(envBaseExecutable)
	if outerExe == "" {
		outerExe = exePath
	}
	var baseHash string
	if outerExe != "" && !sameFilePath(outerExe, overrideBin) {
		baseHash, _ = fileSHA256(outerExe)
	} else if prev, err := readOverrideState(baseDir); err == nil {
		baseHash = prev.BaseExeSHA256
	}

	_ = writeOverrideState(baseDir, OverrideState{
		Version:       targetVersion,
		SHA256:        binHash,
		BaseExeSHA256: baseHash,
		BootAttempts:  0,
		Healthy:       true,
		UpdatedAt:     time.Now().Unix(),
	})
	if noexecFallbackPath != "" {
		return noexecFallbackPath, nil
	}
	return overrideBin, nil
}

func (s *Service) updateJob(fn func(*ApplyJob)) {
	s.jobMu.Lock()
	fn(&s.job)
	s.job.UpdatedAt = time.Now().Unix()
	s.jobMu.Unlock()
}

func (s *Service) failJob(errMsg string) {
	s.jobMu.Lock()
	s.job.State = "failed"
	s.job.Error = errMsg
	s.job.UpdatedAt = time.Now().Unix()
	s.jobMu.Unlock()
}

func detectInstallMode() string {
	return hostauth.DetectInstallMode(hostauth.OSFS())
}

func localizedHints(mode string, lang i18n.Lang) (note, cmdHint string) {
	note = "自更新过程中会自动保留数据目录（dkstore.db 与 secret.key），已连接的远程设备凭证、忽略规则与更新历史不会丢失。"
	if lang == i18n.EnUS {
		note = "Self-update preserves your data directory (dkstore.db & secret.key); connected host credentials, ignore rules, and update history remain intact."
	}
	switch mode {
	case hostauth.ModeDocker:
		cmdHint = "docker compose pull dkstore && docker compose up -d dkstore"
	case hostauth.ModeOpenWrt:
		if lang == i18n.EnUS {
			cmdHint = "Upload new .ipk in LuCI -> System -> Software (or .run in iStore)"
		} else {
			cmdHint = "在 LuCI「系统 → 软件包 → 上传软件包」上传新版 .ipk（或在 iStore 上传新版 .run）"
		}
	case hostauth.ModeSynology:
		if lang == i18n.EnUS {
			cmdHint = "Upgrade directly here or via Synology DSM Package Center -> Manual Install"
		} else {
			cmdHint = "可直接在此一键更新，或在群晖 DSM「套件中心 → 手动安装」上传新版 .spk"
		}
	case hostauth.ModeFnOS:
		if lang == i18n.EnUS {
			cmdHint = "Upgrade directly here or via fnOS App Center -> Manual Install"
		} else {
			cmdHint = "可直接在此一键更新，或在飞牛 fnOS「应用中心 → 手动安装」上传新版 .fpk"
		}
	default:
		cmdHint = "dkstore --version"
	}
	return note, cmdHint
}

func localizeConfigError(lang i18n.Lang, err error) error {
	if lang == i18n.EnUS {
		return err
	}
	switch {
	case errors.Is(err, ErrInvalidRepo):
		return errors.New("源码仓库格式不正确，请填写 owner/repo 格式（如 kukushouhou/DKStore）")
	case errors.Is(err, ErrCustomHostEmpty):
		return errors.New("请选择或输入镜像地址")
	case errors.Is(err, ErrCustomHostInvalid):
		return errors.New("镜像地址格式不正确，请输入有效的 http/https 域名")
	case errors.Is(err, ErrMirrorRepoLocked):
		return errors.New("官方镜像源仅支持官方主仓库 kukushouhou/DKStore")
	default:
		return err
	}
}

func localizeProbeError(lang i18n.Lang, err error) string {
	if err == nil {
		return ""
	}
	if lang == i18n.EnUS {
		return "Unable to reach update source: " + err.Error()
	}
	return "无法连接该更新源，请检查网络或切换镜像地址"
}

func localizeApplyError(lang i18n.Lang, err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, ErrUnsupportedPackage) {
		if lang == i18n.EnUS {
			return "Invalid update package: could not find DKStore executable inside file"
		}
		return "无法识别的更新包格式：未在文件中找到有效的 DKStore 主程序"
	}
	if errors.Is(err, ErrArchMismatch) {
		if lang == i18n.EnUS {
			return fmt.Sprintf("Architecture mismatch: please select a package built for %s/%s", runtime.GOOS, runtime.GOARCH)
		}
		return fmt.Sprintf("更新包架构不匹配：请上传适用于当前系统（%s/%s）的安装包", runtime.GOOS, runtime.GOARCH)
	}
	if strings.Contains(err.Error(), "SHA-256") {
		if lang == i18n.EnUS {
			return "Downloaded package SHA-256 verification failed; please retry"
		}
		return "下载的更新包 SHA-256 完整性校验未通过，请重试"
	}
	if lang == i18n.EnUS {
		return "Self-update failed: " + err.Error()
	}
	return "自更新失败：" + err.Error()
}

func localizedBusyMsg(lang i18n.Lang) string {
	if lang == i18n.EnUS {
		return "Self-update is already in progress"
	}
	return "正在执行自更新任务，请稍候"
}

func ensureVPrefix(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "v") && !strings.HasPrefix(v, "V") {
		return "v" + v
	}
	return "v" + v[1:]
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return sql.ErrNoRows
}
