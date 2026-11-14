// Package host 负责被管理主机的增删改查、连接测试、指纹记录与适配器构造（实施文档 §3）。
package host

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/kukushouhou/DKStore/internal/hostauth"
	"github.com/kukushouhou/DKStore/internal/i18n"
	"github.com/kukushouhou/DKStore/internal/platform"
	"github.com/kukushouhou/DKStore/internal/platform/docker"
	"github.com/kukushouhou/DKStore/internal/platform/synology"
	"github.com/kukushouhou/DKStore/internal/secret"
	"github.com/kukushouhou/DKStore/internal/store"
	"github.com/kukushouhou/DKStore/internal/transport"
)

// Host 是对外展示与持久化的主机记录（不含任何密码或私钥明文）。
type Host struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`      // docker | synology
	Transport  string `json:"transport"` // local | ssh | dsm_api
	Address    string `json:"address,omitempty"`
	Port       int    `json:"port,omitempty"`
	SSHUser    string `json:"sshUser,omitempty"`
	SSHAuth    string `json:"sshAuth,omitempty"` // password | key
	SSHHostKey string `json:"sshHostKey,omitempty"`
	// SSHPrevHostKey / SSHHostKeyChangedAt 非空表示指纹曾自动更新（可能重装过系统），卡片据此提示。
	SSHPrevHostKey      string             `json:"sshPrevHostKey,omitempty"`
	SSHHostKeyChangedAt int64              `json:"sshHostKeyChangedAt,omitempty"`
	TransportMode       string             `json:"transportMode,omitempty"`
	DSMURL              string             `json:"dsmUrl,omitempty"`
	DSMUser             string             `json:"dsmUser,omitempty"`
	Info                *platform.HostInfo `json:"info,omitempty"`
	Status              string             `json:"status"` // online | offline | unknown
	LastSeenAt          int64              `json:"lastSeenAt,omitempty"`
	Sort                int                `json:"sort"`
	CreatedAt           int64              `json:"createdAt"`
}

// CreateOrUpdateInput 是新建、编辑主机或测试连接时的输入。
type CreateOrUpdateInput struct {
	// HostID 仅用于 POST /hosts/test：编辑已有主机时复用其已保存的凭据。
	HostID        string `json:"hostId,omitempty"`
	Name          string `json:"name"`
	Kind          string `json:"kind"`      // docker | synology
	Transport     string `json:"transport"` // local | ssh | dsm_api
	Address       string `json:"address"`
	Port          int    `json:"port"`
	SSHUser       string `json:"sshUser"`
	SSHAuth       string `json:"sshAuth"`   // password | key
	SSHSecret     string `json:"sshSecret"` // 密码或私钥 PEM；编辑时留空表示不修改
	SSHPassphrase string `json:"sshPassphrase"`
	DSMURL        string `json:"dsmUrl"`
	DSMUser       string `json:"dsmUser"`
	DSMSecret     string `json:"dsmSecret"`
	DSMOTPCode    string `json:"dsmOtpCode"`
	// DeviceTokenNonce 为“测试连接”返回的设备令牌暂存凭证，保存时带回以复用令牌（避免同一动态码用两次）。
	DeviceTokenNonce string `json:"deviceTokenNonce,omitempty"`

	// 以下字段只在服务端内部使用，不接受客户端传入。
	SSHHostKey     string `json:"-"` // 已记录的指纹（来自数据库）
	DSMDeviceToken string `json:"-"`
}

// StepResult 表示连接测试中单步的诊断结果。
type StepResult struct {
	Step    string `json:"step"`
	Status  string `json:"status"` // ok | error | skipped
	Message string `json:"message"`
}

// TestResult 是 POST /hosts/test 的返回结构。
type TestResult struct {
	OK               bool               `json:"ok"`
	NeedOTP          bool               `json:"needOtp,omitempty"`
	DeviceToken      string             `json:"-"`
	DeviceTokenNonce string             `json:"deviceTokenNonce,omitempty"`
	Steps            []StepResult       `json:"steps"`
	PresentedHostKey string             `json:"presentedHostKey,omitempty"`
	HostKeyChanged   bool               `json:"hostKeyChanged,omitempty"`
	PreviousHostKey  string             `json:"previousHostKey,omitempty"`
	TransportMode    string             `json:"transportMode,omitempty"`
	Info             *platform.HostInfo `json:"info,omitempty"`
}

// SaveVerifyError 表示保存主机时强制重连验证未通过。
type SaveVerifyError struct {
	Code       string
	Message    string
	TestResult *TestResult
}

func (e *SaveVerifyError) Error() string { return e.Message }

// deviceTokenTTL 为测试连接所得 DSM 设备令牌的暂存时长。
const deviceTokenTTL = 10 * time.Minute

type stashedToken struct {
	token   string
	url     string
	user    string
	expires time.Time
}

const runtimeAdapterIdleTTL = 90 * time.Second

type pooledRuntimeAdapter struct {
	ad       platform.Adapter
	refs     int
	evicted  bool
	lastUsed time.Time
}

type dialRuntimeCall struct {
	done chan struct{}
	ent  *pooledRuntimeAdapter
	err  error
}

// Registry 管理主机与凭证。
type Registry struct {
	st  *store.Store
	box *secret.Box

	tokMu  sync.Mutex
	tokens map[string]stashedToken

	rtMu          sync.Mutex
	rtPool        map[string]*pooledRuntimeAdapter
	rtInflight    map[string]*dialRuntimeCall
	bgEnabled     bool
	bgCtx         context.Context
	bgCancel      context.CancelFunc
	bgRefreshing  map[string]bool
	bgWg          sync.WaitGroup
}

// NewRegistry 创建主机注册表。
func NewRegistry(st *store.Store, box *secret.Box) *Registry {
	return &Registry{
		st:           st,
		box:          box,
		tokens:       make(map[string]stashedToken),
		rtPool:       make(map[string]*pooledRuntimeAdapter),
		rtInflight:   make(map[string]*dialRuntimeCall),
		bgRefreshing: make(map[string]bool),
	}
}

// EnableBackgroundCollector 开启后台运行态快照预收录，并在启动后立即异步收录全部设备快照到本地 SQLite。
func (r *Registry) EnableBackgroundCollector(parent context.Context) {
	r.rtMu.Lock()
	if r.bgEnabled {
		r.rtMu.Unlock()
		return
	}
	r.bgEnabled = true
	r.bgCtx, r.bgCancel = context.WithCancel(parent)
	r.rtMu.Unlock()

	r.RefreshRuntimeAsync("")
}

// RefreshRuntimeAsync 在后台异步采集指定主机（为空表示全部主机）的最新容器运行态并落库到 host_runtime_snapshot。
// 仅在 EnableBackgroundCollector 开启后执行，相同 hostID 并发触发自动合并去重。
func (r *Registry) RefreshRuntimeAsync(hostID string) {
	key := strings.TrimSpace(hostID)
	r.rtMu.Lock()
	if !r.bgEnabled || r.bgCtx == nil || r.bgCtx.Err() != nil || r.bgRefreshing[key] {
		r.rtMu.Unlock()
		return
	}
	r.bgRefreshing[key] = true
	ctx := r.bgCtx
	r.bgWg.Add(1)
	r.rtMu.Unlock()

	go func() {
		defer func() {
			r.rtMu.Lock()
			delete(r.bgRefreshing, key)
			r.rtMu.Unlock()
			r.bgWg.Done()
		}()
		cCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		_, _ = r.ListRuntimeContainers(cCtx, key, true)
		_, _ = r.ListHostMetrics(cCtx, key)
	}()
}

// Close 关闭后台收录协程并清理运行态适配器连接池。
func (r *Registry) Close() error {
	r.rtMu.Lock()
	cancel := r.bgCancel
	r.bgEnabled = false
	var toClose []platform.Adapter
	for id, ent := range r.rtPool {
		ent.evicted = true
		delete(r.rtPool, id)
		if ent.refs <= 0 {
			toClose = append(toClose, ent.ad)
		}
	}
	r.rtMu.Unlock()
	if cancel != nil {
		cancel()
	}
	r.bgWg.Wait()
	for _, ad := range toClose {
		_ = ad.Close()
	}
	return nil
}

func (r *Registry) evictRuntimeAdapter(hostID string) {
	r.rtMu.Lock()
	ent := r.rtPool[hostID]
	var toClose platform.Adapter
	if ent != nil {
		ent.evicted = true
		delete(r.rtPool, hostID)
		if ent.refs <= 0 {
			toClose = ent.ad
		}
	}
	r.rtMu.Unlock()
	if toClose != nil {
		_ = toClose.Close()
	}
}

func (r *Registry) makeReleaseFn(hostID string, ent *pooledRuntimeAdapter) func(bool) {
	var once sync.Once
	return func(failed bool) {
		once.Do(func() {
			var toClose platform.Adapter
			r.rtMu.Lock()
			ent.refs--
			ent.lastUsed = time.Now()
			if failed {
				ent.evicted = true
				if r.rtPool[hostID] == ent {
					delete(r.rtPool, hostID)
				}
			}
			if ent.evicted && ent.refs <= 0 {
				toClose = ent.ad
			}
			r.rtMu.Unlock()
			if toClose != nil {
				_ = toClose.Close()
			}
		})
	}
}

func (r *Registry) acquireRuntimeAdapter(ctx context.Context, hostID string) (platform.Adapter, bool, func(bool), error) {
	now := time.Now()
	var stale []platform.Adapter

	r.rtMu.Lock()
	for id, e := range r.rtPool {
		if e.refs <= 0 && now.Sub(e.lastUsed) > runtimeAdapterIdleTTL {
			e.evicted = true
			delete(r.rtPool, id)
			stale = append(stale, e.ad)
		}
	}
	if ent := r.rtPool[hostID]; ent != nil && !ent.evicted {
		ent.refs++
		ent.lastUsed = now
		rel := r.makeReleaseFn(hostID, ent)
		r.rtMu.Unlock()
		for _, ad := range stale {
			_ = ad.Close()
		}
		return ent.ad, true, rel, nil
	}
	if dc := r.rtInflight[hostID]; dc != nil {
		r.rtMu.Unlock()
		for _, ad := range stale {
			_ = ad.Close()
		}
		select {
		case <-dc.done:
			if dc.err != nil {
				return nil, false, nil, dc.err
			}
			r.rtMu.Lock()
			ent := dc.ent
			if ent != nil && !ent.evicted {
				ent.refs++
				ent.lastUsed = time.Now()
				rel := r.makeReleaseFn(hostID, ent)
				r.rtMu.Unlock()
				return ent.ad, true, rel, nil
			}
			r.rtMu.Unlock()
		case <-ctx.Done():
			return nil, false, nil, ctx.Err()
		}
		// 若并发拨号所得适配器已被驱逐，重新走一次新建
		r.rtMu.Lock()
	}

	dc := &dialRuntimeCall{done: make(chan struct{})}
	r.rtInflight[hostID] = dc
	r.rtMu.Unlock()
	for _, ad := range stale {
		_ = ad.Close()
	}

	dialCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	ad, _, err := r.OpenAdapter(dialCtx, hostID)
	cancel()

	r.rtMu.Lock()
	delete(r.rtInflight, hostID)
	if err != nil {
		dc.err = err
		close(dc.done)
		r.rtMu.Unlock()
		return nil, false, nil, err
	}
	ent := &pooledRuntimeAdapter{
		ad:       ad,
		refs:     1,
		lastUsed: time.Now(),
	}
	r.rtPool[hostID] = ent
	dc.ent = ent
	close(dc.done)
	rel := r.makeReleaseFn(hostID, ent)
	r.rtMu.Unlock()
	return ad, false, rel, nil
}

// dbtx 同时被 *sql.DB 与 *sql.Tx 实现。
type dbtx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// EnsureLocalHost 在尚未完成过自动本机初始化、主机表为空且本机 /var/run/docker.sock 可访问时，自动添加本机设备记录。
// 支持在启动时及后续 Docker 服务延迟就绪（如飞牛首次开启 Docker 后）自动挂载本机设备。
func (r *Registry) EnsureLocalHost(ctx context.Context) error {
	_, err := r.EnsureLocalHostAdded(ctx)
	return err
}

// EnsureLocalHostAdded 与 EnsureLocalHost 相同，若本次新创建了本机设备则返回该主机记录（供上层自动触发首次扫描）。
func (r *Registry) EnsureLocalHostAdded(ctx context.Context) (*Host, error) {
	var initialized string
	_ = r.st.DB.QueryRowContext(ctx, `SELECT v FROM setting WHERE k='auto_local_initialized'`).Scan(&initialized)
	if initialized == "1" {
		return nil, nil
	}
	var count int
	if err := r.st.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM host`).Scan(&count); err != nil {
		return nil, err
	}
	if count > 0 {
		_, _ = r.st.DB.ExecContext(ctx, `INSERT INTO setting(k, v) VALUES ('auto_local_initialized', '1') ON CONFLICT(k) DO UPDATE SET v='1'`)
		return nil, nil
	}
	sock := transport.DefaultLocalSocket()
	if _, err := os.Stat(sock); err != nil {
		return nil, nil
	}
	name := "本机 Docker"
	kind := "docker"
	switch hostauth.DetectInstallMode(hostauth.OSFS()) {
	case hostauth.ModeFnOS:
		name = "本机飞牛 NAS"
		kind = "fnos"
	case hostauth.ModeOpenWrt:
		name = "本机 OpenWrt"
		if b, err := os.ReadFile("/etc/openwrt_release"); err == nil && strings.Contains(strings.ToLower(string(b)), "istoreos") {
			name = "本机 iStoreOS"
		}
		kind = "openwrt"
	}
	h, err := r.Create(ctx, CreateOrUpdateInput{
		Name:      name,
		Kind:      kind,
		Transport: "local",
	}, i18n.ZhCN)
	if err != nil {
		return nil, err
	}
	_, _ = r.st.DB.ExecContext(ctx, `INSERT INTO setting(k, v) VALUES ('auto_local_initialized', '1') ON CONFLICT(k) DO UPDATE SET v='1'`)
	return h, nil
}

// LocalStatus 返回是否已存在本机设备、是否可一键添加本机设备以及本机 Docker Socket 状态（ok | socket_missing | permission_denied）。
func (r *Registry) LocalStatus(ctx context.Context) (hasLocalHost bool, canAddLocal bool, localStatus string) {
	var count int
	_ = r.st.DB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM host
		WHERE transport = 'local'
		   OR LOWER(COALESCE(address,'')) LIKE '%127.0.0.1%'
		   OR LOWER(COALESCE(address,'')) LIKE '%localhost%'
		   OR LOWER(COALESCE(dsm_url,'')) LIKE '%127.0.0.1%'
		   OR LOWER(COALESCE(dsm_url,'')) LIKE '%localhost%'
	`).Scan(&count)
	hasLocalHost = count > 0

	sock := transport.DefaultLocalSocket()
	if _, err := os.Stat(sock); err != nil {
		localStatus = "socket_missing"
		return hasLocalHost, false, localStatus
	}
	pCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	lTr := transport.NewLocal(sock)
	defer lTr.Close()
	if _, err := docker.New("probe", lTr).Probe(pCtx); err != nil {
		localStatus = "permission_denied"
	} else {
		localStatus = "ok"
	}
	canAddLocal = !hasLocalHost && localStatus == "ok"
	return
}

func (r *Registry) querySingleHostRuntime(ctx context.Context, h Host, wantStats bool) ([]platform.AppRuntimeTree, error) {
	runOnce := func(ad platform.Adapter) ([]platform.AppRuntimeTree, error) {
		if optAd, ok := ad.(platform.RuntimeStatOptionsAdapter); ok {
			return optAd.ListRuntimeAppsWithOptions(ctx, wantStats)
		}
		if rtAd, ok := ad.(platform.RuntimeStatAdapter); ok {
			return rtAd.ListRuntimeApps(ctx)
		}
		return nil, nil
	}

	ad, wasWarm, release, err := r.acquireRuntimeAdapter(ctx, h.ID)
	if err != nil {
		return nil, err
	}
	apps, err := runOnce(ad)
	if err != nil {
		release(true)
		// 若复用的空闲连接已断开，自动重连重试一次
		if wasWarm && ctx.Err() == nil {
			ad2, _, release2, err2 := r.acquireRuntimeAdapter(ctx, h.ID)
			if err2 != nil {
				return nil, err2
			}
			apps, err = runOnce(ad2)
			release2(err != nil)
		}
		if err != nil {
			return nil, err
		}
	} else {
		release(false)
	}

	for i := range apps {
		apps[i].HostID = h.ID
		apps[i].HostName = h.Name
	}
	r.saveHostRuntimeSnapshot(ctx, h.ID, wantStats, apps)
	return apps, nil
}

func (r *Registry) saveHostRuntimeSnapshot(ctx context.Context, hostID string, wantStats bool, apps []platform.AppRuntimeTree) {
	if r.st == nil || r.st.DB == nil {
		return
	}
	now := time.Now().Unix()
	toSave := apps

	// 若本次是仅拉取列表（wantStats=false），且库中有 60 秒内的近期资源统计，保留原有的 CPU/内存采样数据避免冲掉快照中的数值
	if !wantStats && len(apps) > 0 {
		var prevJSON string
		var prevAt int64
		if err := r.st.DB.QueryRowContext(ctx, `SELECT trees_json, updated_at FROM host_runtime_snapshot WHERE host_id=?`, hostID).Scan(&prevJSON, &prevAt); err == nil && now-prevAt <= 60 {
			var prevTrees []platform.AppRuntimeTree
			if json.Unmarshal([]byte(prevJSON), &prevTrees) == nil && len(prevTrees) > 0 {
				toSave = mergeRuntimeStats(apps, prevTrees)
			}
		}
	}

	if toSave == nil {
		toSave = []platform.AppRuntimeTree{}
	}
	b, err := json.Marshal(toSave)
	if err != nil {
		return
	}
	wCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	_, _ = r.st.DB.ExecContext(wCtx, `
		INSERT INTO host_runtime_snapshot(host_id, trees_json, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(host_id) DO UPDATE SET
			trees_json=excluded.trees_json,
			updated_at=excluded.updated_at`,
		hostID, string(b), now,
	)
}

func mergeRuntimeStats(fresh []platform.AppRuntimeTree, prev []platform.AppRuntimeTree) []platform.AppRuntimeTree {
	prevByKey := make(map[string]platform.AppRuntimeTree, len(prev))
	for _, p := range prev {
		if p.StatsReady {
			prevByKey[p.Key] = p
		}
	}
	if len(prevByKey) == 0 {
		return fresh
	}
	out := make([]platform.AppRuntimeTree, len(fresh))
	for i, f := range fresh {
		cpApp := f
		pApp, ok := prevByKey[f.Key]
		if !ok {
			out[i] = cpApp
			continue
		}
		cByID := make(map[string]platform.ContainerRuntimeStat, len(pApp.Containers))
		for _, pc := range pApp.Containers {
			k := pc.ID
			if k == "" {
				k = pc.Name
			}
			cByID[k] = pc
		}
		cpContainers := make([]platform.ContainerRuntimeStat, len(f.Containers))
		var cpuSum float64
		var memSum int64
		for j, fc := range f.Containers {
			k := fc.ID
			if k == "" {
				k = fc.Name
			}
			if pc, exists := cByID[k]; exists && fc.State == "running" {
				fc.CPUPercent = pc.CPUPercent
				fc.MemUsage = pc.MemUsage
				fc.MemLimit = pc.MemLimit
				cpuSum += pc.CPUPercent
				memSum += pc.MemUsage
			}
			cpContainers[j] = fc
		}
		cpApp.Containers = cpContainers
		cpApp.CPUPercent = math.Round(cpuSum*100) / 100
		cpApp.MemUsage = memSum
		cpApp.StatsReady = true
		out[i] = cpApp
	}
	return out
}

// ListCachedRuntimeContainers 纯本地读取 SQLite 已收录的容器运行态快照（零远程网络 IO，毫秒级返回）。
// 若某台主机尚未生成运行态快照，自动回退读取已发现的 app / service 库存表兜底展示。
func (r *Registry) ListCachedRuntimeContainers(ctx context.Context, hostIDFilter string) ([]platform.AppRuntimeTree, error) {
	hosts, err := r.List(ctx)
	if err != nil {
		return nil, err
	}
	filter := strings.TrimSpace(hostIDFilter)
	var targets []Host
	for _, h := range hosts {
		if filter != "" && h.ID != filter {
			continue
		}
		targets = append(targets, h)
	}
	if len(targets) == 0 {
		return []platform.AppRuntimeTree{}, nil
	}

	type snapRow struct {
		raw       string
		updatedAt int64
	}
	snaps := make(map[string]snapRow, len(targets))
	sRows, err := r.st.DB.QueryContext(ctx, `SELECT host_id, trees_json, updated_at FROM host_runtime_snapshot`)
	if err == nil {
		for sRows.Next() {
			var hid, raw string
			var uat int64
			if sRows.Scan(&hid, &raw, &uat) == nil {
				snaps[hid] = snapRow{raw: raw, updatedAt: uat}
			}
		}
		sRows.Close()
	}

	now := time.Now().Unix()
	out := make([]platform.AppRuntimeTree, 0)
	for _, h := range targets {
		if snap, ok := snaps[h.ID]; ok {
			var trees []platform.AppRuntimeTree
			if json.Unmarshal([]byte(snap.raw), &trees) == nil {
				age := now - snap.updatedAt
				for i := range trees {
					trees[i].HostID = h.ID
					trees[i].HostName = h.Name
					if age > 45 {
						trees[i].StatsReady = false
						trees[i].CPUPercent = 0
						trees[i].MemUsage = 0
					}
					if age > 60 {
						for j := range trees[i].Containers {
							if trees[i].Containers[j].StartedAt > 0 {
								trees[i].Containers[j].Uptime = ""
							}
						}
					}
				}
				out = append(out, trees...)
				continue
			}
		}
		if fallback := r.synthesizeRuntimeFromInventory(ctx, h); len(fallback) > 0 {
			out = append(out, fallback...)
		}
	}
	return out, nil
}

func (r *Registry) synthesizeRuntimeFromInventory(ctx context.Context, h Host) []platform.AppRuntimeTree {
	rows, err := r.st.DB.QueryContext(ctx, `
		SELECT a.key, a.name, a.source, s.key, s.name, COALESCE(s.image_ref,''), COALESCE(s.state,'unknown')
		FROM app a
		JOIN service s ON s.app_key = a.key
		WHERE a.host_id = ? AND a.gone_at IS NULL AND s.gone_at IS NULL
		ORDER BY a.source ASC, a.name ASC, s.name ASC`, h.ID)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var order []string
	byApp := make(map[string]*platform.AppRuntimeTree)
	for rows.Next() {
		var appKey, appName, srcStr, svcKey, svcName, imgRef, state string
		if rows.Scan(&appKey, &appName, &srcStr, &svcKey, &svcName, &imgRef, &state) != nil {
			continue
		}
		tree, exists := byApp[appKey]
		if !exists {
			tree = &platform.AppRuntimeTree{
				Key:        appKey,
				HostID:     h.ID,
				HostName:   h.Name,
				Name:       appName,
				Source:     platform.SourceKind(srcStr),
				StatsReady: false,
			}
			byApp[appKey] = tree
			order = append(order, appKey)
		}
		normState := strings.ToLower(strings.TrimSpace(state))
		if normState == "" {
			normState = "unknown"
		}
		cName := svcName
		svcLabel := svcName
		if srcStr == string(platform.SourceStandalone) || svcName == "main" {
			cName = appName
			svcLabel = ""
		}
		tree.TotalCount++
		if normState == "running" {
			tree.RunningCount++
		}
		tree.Containers = append(tree.Containers, platform.ContainerRuntimeStat{
			ID:      svcKey,
			Name:    cName,
			Service: svcLabel,
			Image:   imgRef,
			State:   normState,
		})
	}
	out := make([]platform.AppRuntimeTree, 0, len(order))
	for _, k := range order {
		out = append(out, *byApp[k])
	}
	return out
}

// ListRuntimeContainers 并发采集全部（或指定）主机上容器的树状实时运行状态。
// withStats 默认 true；传入 false 时跳过 CPU/内存采样以实现列表毫秒级首屏返回。
func (r *Registry) ListRuntimeContainers(ctx context.Context, hostIDFilter string, withStats ...bool) ([]platform.AppRuntimeTree, error) {
	wantStats := true
	if len(withStats) > 0 {
		wantStats = withStats[0]
	}
	hosts, err := r.List(ctx)
	if err != nil {
		return nil, err
	}
	filter := strings.TrimSpace(hostIDFilter)
	var targets []Host
	for _, h := range hosts {
		if filter != "" && h.ID != filter {
			continue
		}
		targets = append(targets, h)
	}
	if len(targets) == 0 {
		return []platform.AppRuntimeTree{}, nil
	}

	perHostTimeout := 12 * time.Second
	if !wantStats {
		perHostTimeout = 8 * time.Second
	}

	perHost := make([][]platform.AppRuntimeTree, len(targets))
	var wg sync.WaitGroup
	for i, h := range targets {
		wg.Add(1)
		go func(idx int, item Host) {
			defer wg.Done()
			hCtx, cancel := context.WithTimeout(ctx, perHostTimeout)
			defer cancel()
			if apps, err := r.querySingleHostRuntime(hCtx, item, wantStats); err == nil && len(apps) > 0 {
				perHost[idx] = apps
			}
		}(i, h)
	}
	wg.Wait()

	out := make([]platform.AppRuntimeTree, 0)
	for _, apps := range perHost {
		out = append(out, apps...)
	}
	return out, nil
}

func (r *Registry) querySingleHostMetrics(ctx context.Context, h Host) (*platform.HostRuntimeStat, error) {
	runOnce := func(ad platform.Adapter) (*platform.HostRuntimeStat, error) {
		hmAd, ok := ad.(platform.HostMetricsAdapter)
		if !ok {
			return nil, errors.New("host metrics unsupported")
		}
		return hmAd.CollectHostMetrics(ctx)
	}

	ad, wasWarm, release, err := r.acquireRuntimeAdapter(ctx, h.ID)
	if err != nil {
		return nil, err
	}
	st, err := runOnce(ad)
	if err != nil {
		release(true)
		if wasWarm && ctx.Err() == nil {
			ad2, _, release2, err2 := r.acquireRuntimeAdapter(ctx, h.ID)
			if err2 != nil {
				return nil, err2
			}
			st, err = runOnce(ad2)
			release2(err != nil)
		}
		if err != nil {
			return nil, err
		}
	} else {
		release(false)
	}
	if st == nil {
		return nil, errors.New("empty host metrics")
	}
	st.HostID = h.ID
	st.Online = true
	if st.SampledAt <= 0 {
		st.SampledAt = time.Now().Unix()
	}

	now := time.Now().Unix()
	if h.Status != "online" || now-h.LastSeenAt >= 30 {
		wCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		_, _ = r.st.DB.ExecContext(wCtx, `UPDATE host SET status='online', last_seen_at=? WHERE id=?`, now, h.ID)
		cancel()
	}
	r.saveHostMetricsSnapshot(ctx, h.ID, st)
	return st, nil
}

func (r *Registry) saveHostMetricsSnapshot(ctx context.Context, hostID string, st *platform.HostRuntimeStat) {
	if r.st == nil || r.st.DB == nil || st == nil {
		return
	}
	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	now := time.Now().Unix()
	wCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	_, _ = r.st.DB.ExecContext(wCtx, `
		INSERT INTO host_metrics_snapshot(host_id, metrics_json, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(host_id) DO UPDATE SET
			metrics_json=excluded.metrics_json,
			updated_at=excluded.updated_at`,
		hostID, string(b), now,
	)
}

// ListCachedHostMetrics 纯本地读取 SQLite 已收录的设备资源快照（零网络等待，毫秒级返回）。
func (r *Registry) ListCachedHostMetrics(ctx context.Context, hostIDFilter string) ([]platform.HostRuntimeStat, error) {
	hosts, err := r.List(ctx)
	if err != nil {
		return nil, err
	}
	filter := strings.TrimSpace(hostIDFilter)
	var targets []Host
	for _, h := range hosts {
		if filter != "" && h.ID != filter {
			continue
		}
		targets = append(targets, h)
	}
	if len(targets) == 0 {
		return []platform.HostRuntimeStat{}, nil
	}

	type snapRow struct {
		raw       string
		updatedAt int64
	}
	snaps := make(map[string]snapRow, len(targets))
	sRows, err := r.st.DB.QueryContext(ctx, `SELECT host_id, metrics_json, updated_at FROM host_metrics_snapshot`)
	if err == nil {
		for sRows.Next() {
			var hid, raw string
			var uat int64
			if sRows.Scan(&hid, &raw, &uat) == nil {
				snaps[hid] = snapRow{raw: raw, updatedAt: uat}
			}
		}
		sRows.Close()
	}

	now := time.Now().Unix()
	out := make([]platform.HostRuntimeStat, 0, len(targets))
	for _, h := range targets {
		if h.Status == "offline" {
			out = append(out, platform.HostRuntimeStat{HostID: h.ID, Online: false})
			continue
		}
		if snap, ok := snaps[h.ID]; ok && now-snap.updatedAt <= 60 {
			var st platform.HostRuntimeStat
			if json.Unmarshal([]byte(snap.raw), &st) == nil && st.Online {
				st.HostID = h.ID
				out = append(out, st)
				continue
			}
		}
		out = append(out, platform.HostRuntimeStat{HostID: h.ID, Online: false})
	}
	return out, nil
}

// ListHostMetrics 并发采集全部（或指定）设备的实时 CPU、内存与存储空间剩余情况。
func (r *Registry) ListHostMetrics(ctx context.Context, hostIDFilter string) ([]platform.HostRuntimeStat, error) {
	hosts, err := r.List(ctx)
	if err != nil {
		return nil, err
	}
	filter := strings.TrimSpace(hostIDFilter)
	var targets []Host
	for _, h := range hosts {
		if filter != "" && h.ID != filter {
			continue
		}
		targets = append(targets, h)
	}
	if len(targets) == 0 {
		return []platform.HostRuntimeStat{}, nil
	}

	out := make([]platform.HostRuntimeStat, len(targets))
	var wg sync.WaitGroup
	for i, h := range targets {
		wg.Add(1)
		go func(idx int, item Host) {
			defer wg.Done()
			hCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			st, qErr := r.querySingleHostMetrics(hCtx, item)
			if qErr != nil {
				if ctx.Err() == nil {
					wCtx, wCancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
					r.markOffline(wCtx, item.ID)
					wCancel()
				}
				out[idx] = platform.HostRuntimeStat{
					HostID: item.ID,
					Online: false,
				}
				return
			}
			out[idx] = *st
		}(i, h)
	}
	wg.Wait()
	return out, nil
}

const hostColumns = `id, name, kind, transport, COALESCE(address,''), COALESCE(port,0),
	COALESCE(ssh_user,''), COALESCE(ssh_auth,''), COALESCE(ssh_host_key,''),
	COALESCE(ssh_prev_host_key,''), COALESCE(ssh_host_key_changed_at,0),
	COALESCE(transport_mode,''), COALESCE(dsm_url,''), COALESCE(dsm_user,''),
	COALESCE(info_json,''), COALESCE(status,'unknown'), COALESCE(last_seen_at,0),
	sort, created_at`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanHost(s rowScanner) (Host, error) {
	var h Host
	var infoStr string
	if err := s.Scan(
		&h.ID, &h.Name, &h.Kind, &h.Transport, &h.Address, &h.Port,
		&h.SSHUser, &h.SSHAuth, &h.SSHHostKey, &h.SSHPrevHostKey, &h.SSHHostKeyChangedAt,
		&h.TransportMode, &h.DSMURL, &h.DSMUser, &infoStr, &h.Status, &h.LastSeenAt,
		&h.Sort, &h.CreatedAt,
	); err != nil {
		return h, err
	}
	if infoStr != "" {
		var info platform.HostInfo
		if json.Unmarshal([]byte(infoStr), &info) == nil {
			h.Info = &info
		}
	}
	return h, nil
}

// List 返回所有主机。
func (r *Registry) List(ctx context.Context) ([]Host, error) {
	rows, err := r.st.DB.QueryContext(ctx, `SELECT `+hostColumns+` FROM host ORDER BY sort ASC, created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Host
	for rows.Next() {
		h, err := scanHost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// Get 获取单台主机。
func (r *Registry) Get(ctx context.Context, id string) (*Host, error) {
	h, err := scanHost(r.st.DB.QueryRowContext(ctx, `SELECT `+hostColumns+` FROM host WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, i18n.NewHostError("host.not_found", nil, id)
	}
	if err != nil {
		return nil, err
	}
	return &h, nil
}

// normalizeInput 补齐默认值并执行 Create / Update 共用的必填校验。
// hasSavedDSMSecret 表示编辑时数据库中已有 DSM 密码（允许留空沿用）。
func normalizeInput(in *CreateOrUpdateInput, hasSavedDSMSecret bool) error {
	if in.Name == "" {
		return i18n.NewHostError("host.name_required", nil)
	}
	if in.Kind == "" {
		in.Kind = "docker"
	}
	if in.Transport == "" {
		if in.Kind == "synology" && in.DSMURL != "" {
			in.Transport = "dsm_api"
		} else {
			in.Transport = "local"
		}
	}
	switch in.Transport {
	case "local":
	case "ssh":
		if in.Address == "" || in.SSHUser == "" {
			return i18n.NewHostError("host.ssh_required", nil)
		}
		if in.Port <= 0 {
			in.Port = 22
		}
		if in.SSHAuth == "" {
			in.SSHAuth = "password"
		}
	case "dsm_api":
		in.Kind = "synology"
		if in.DSMURL == "" || in.DSMUser == "" || (in.DSMSecret == "" && !hasSavedDSMSecret) {
			return i18n.NewHostError("host.dsm_required", nil)
		}
	default:
		return i18n.NewHostError("host.transport_invalid", nil, in.Transport)
	}
	return nil
}

// hostRefs 是主机记录上的凭据引用。
type hostRefs struct {
	ssh, dsm, did string
}

func loadRefs(ctx context.Context, q dbtx, id string) (hostRefs, error) {
	var refs hostRefs
	err := q.QueryRowContext(ctx, `
		SELECT COALESCE(ssh_secret_ref,''), COALESCE(dsm_secret_ref,''), COALESCE(dsm_device_token_ref,'')
		FROM host WHERE id=?`, id).Scan(&refs.ssh, &refs.dsm, &refs.did)
	if errors.Is(err, sql.ErrNoRows) {
		return refs, i18n.NewHostError("host.not_found", nil, id)
	}
	return refs, err
}

// fillSavedCredentials 在输入未重填密码 / 私钥 / 设备令牌时，加载已保存的凭据用于实时连接（Update 与编辑时测试连接共用）。
// 返回已保存的设备令牌明文（用于判断令牌是否变化）。
func (r *Registry) fillSavedCredentials(ctx context.Context, cur *Host, refs hostRefs, in *CreateOrUpdateInput) (savedDID string, err error) {
	if in.SSHSecret == "" && refs.ssh != "" && in.Transport == "ssh" {
		raw, err := r.loadSecret(ctx, r.st.DB, refs.ssh)
		if err != nil {
			return "", i18n.NewHostError("host.secret_load", err)
		}
		var p sshSecretPayload
		if json.Unmarshal([]byte(raw), &p) == nil {
			in.SSHSecret = p.Secret
			if in.SSHPassphrase == "" {
				in.SSHPassphrase = p.Passphrase
			}
		} else {
			in.SSHSecret = raw
		}
	}
	if in.DSMSecret == "" && refs.dsm != "" {
		raw, err := r.loadSecret(ctx, r.st.DB, refs.dsm)
		if err != nil {
			return "", i18n.NewHostError("host.secret_load", err)
		}
		in.DSMSecret = raw
	}
	// 设备令牌与 DSM 地址 + 账号绑定，二者未改动时才复用
	if refs.did != "" && in.DSMURL == cur.DSMURL && in.DSMUser == cur.DSMUser {
		if tok, err := r.loadSecret(ctx, r.st.DB, refs.did); err == nil {
			savedDID = tok
			if in.DSMDeviceToken == "" {
				in.DSMDeviceToken = tok
			}
		}
	}
	// 地址与端口未变时沿用已记录的指纹，用于检测“指纹已变化”
	port := in.Port
	if port <= 0 {
		port = 22
	}
	curPort := cur.Port
	if curPort <= 0 {
		curPort = 22
	}
	if in.Transport == "ssh" && in.Address == cur.Address && port == curPort {
		in.SSHHostKey = cur.SSHHostKey
	}
	return savedDID, nil
}

// applyDeviceTokenNonce 若带有测试连接暂存的设备令牌，则优先使用该令牌并不再提交 OTP。
func (r *Registry) applyDeviceTokenNonce(in *CreateOrUpdateInput) {
	if in.DeviceTokenNonce == "" || in.Transport != "dsm_api" {
		return
	}
	if tok := r.takeDeviceToken(in.DeviceTokenNonce, in.DSMURL, in.DSMUser); tok != "" {
		in.DSMDeviceToken = tok
		in.DSMOTPCode = ""
	}
}

// Create 新增主机并强制执行一次实时连接与能力校验，验证通过后在单个事务中入库。
func (r *Registry) Create(ctx context.Context, in CreateOrUpdateInput, lang i18n.Lang) (*Host, error) {
	if err := normalizeInput(&in, false); err != nil {
		return nil, err
	}
	if in.Transport == "local" {
		var localCnt int
		_ = r.st.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM host WHERE transport = 'local'`).Scan(&localCnt)
		if localCnt > 0 {
			return nil, i18n.NewHostError("host.err.local_exists", nil)
		}
	}
	in.SSHHostKey = "" // 首次连接自动记录指纹（TOFU）
	in.DSMDeviceToken = ""
	r.applyDeviceTokenNonce(&in)

	testRes := r.testConnection(ctx, in, lang)
	if !testRes.OK {
		return nil, buildSaveVerifyError(testRes, lang)
	}
	refineDetectedKind(&in, testRes.Info)

	id := newID("h_")
	now := time.Now().Unix()
	infoJSON := marshalInfo(testRes.Info)
	hostKey := ""
	if in.Transport == "ssh" {
		hostKey = testRes.PresentedHostKey
	}

	err := r.withTx(ctx, func(tx *sql.Tx) error {
		var sshRef, dsmRef, didRef string
		var err error
		if in.Transport == "ssh" && in.SSHSecret != "" {
			blob, _ := json.Marshal(sshSecretPayload{Secret: in.SSHSecret, Passphrase: in.SSHPassphrase})
			if sshRef, err = r.saveSecret(ctx, tx, string(blob)); err != nil {
				return err
			}
		}
		if in.DSMSecret != "" {
			if dsmRef, err = r.saveSecret(ctx, tx, in.DSMSecret); err != nil {
				return err
			}
		}
		if testRes.DeviceToken != "" {
			if didRef, err = r.saveSecret(ctx, tx, testRes.DeviceToken); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO host(
				id, name, kind, transport, address, port, ssh_user, ssh_auth,
				ssh_secret_ref, ssh_host_key, transport_mode, dsm_url, dsm_user,
				dsm_secret_ref, dsm_device_token_ref, info_json, status, last_seen_at, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'online', ?, ?)`,
			id, in.Name, in.Kind, in.Transport, in.Address, in.Port, in.SSHUser, in.SSHAuth,
			sshRef, hostKey, testRes.TransportMode, in.DSMURL, in.DSMUser,
			dsmRef, didRef, infoJSON, now, now,
		)
		return err
	})
	if err != nil {
		return nil, i18n.NewHostError("host.save_failed", err)
	}
	r.evictRuntimeAdapter(id)
	r.RefreshRuntimeAsync(id)
	return r.Get(ctx, id)
}

// Update 更新主机信息；若密码/私钥留空则沿用已保存凭证，并强制重新执行实时连接校验，校验通过后在单个事务中写库。
func (r *Registry) Update(ctx context.Context, id string, in CreateOrUpdateInput, lang i18n.Lang) (*Host, error) {
	cur, err := r.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	refs, err := loadRefs(ctx, r.st.DB, id)
	if err != nil {
		return nil, err
	}

	if in.Name == "" {
		in.Name = cur.Name
	}
	if in.Kind == "" {
		in.Kind = cur.Kind
	}
	if in.Transport == "" {
		in.Transport = cur.Transport
	}
	if in.SSHAuth == "" {
		in.SSHAuth = cur.SSHAuth
	}
	if err := normalizeInput(&in, refs.dsm != ""); err != nil {
		return nil, err
	}
	in.SSHHostKey = ""
	in.DSMDeviceToken = ""

	verifyIn := in
	savedDID, err := r.fillSavedCredentials(ctx, cur, refs, &verifyIn)
	if err != nil {
		return nil, err
	}
	r.applyDeviceTokenNonce(&verifyIn)

	testRes := r.testConnection(ctx, verifyIn, lang)
	if !testRes.OK {
		return nil, buildSaveVerifyError(testRes, lang)
	}

	infoJSON := marshalInfo(testRes.Info)
	now := time.Now().Unix()
	hostKey, prevKey, changedAt := "", "", int64(0)
	if in.Transport == "ssh" {
		hostKey = testRes.PresentedHostKey
		if testRes.HostKeyChanged {
			prevKey, changedAt = testRes.PreviousHostKey, now
		}
	}

	var obsolete []string
	err = r.withTx(ctx, func(tx *sql.Tx) error {
		sshRef, dsmRef, didRef := refs.ssh, refs.dsm, refs.did
		if in.Transport == "ssh" && in.SSHSecret != "" {
			blob, _ := json.Marshal(sshSecretPayload{Secret: in.SSHSecret, Passphrase: in.SSHPassphrase})
			ref, err := r.saveSecret(ctx, tx, string(blob))
			if err != nil {
				return err
			}
			if sshRef != "" {
				obsolete = append(obsolete, sshRef)
			}
			sshRef = ref
		}
		if in.DSMSecret != "" {
			ref, err := r.saveSecret(ctx, tx, in.DSMSecret)
			if err != nil {
				return err
			}
			if dsmRef != "" {
				obsolete = append(obsolete, dsmRef)
			}
			dsmRef = ref
		}
		// 令牌与已保存值相同时不重复写入
		if testRes.DeviceToken != "" && testRes.DeviceToken != savedDID {
			ref, err := r.saveSecret(ctx, tx, testRes.DeviceToken)
			if err != nil {
				return err
			}
			if didRef != "" {
				obsolete = append(obsolete, didRef)
			}
			didRef = ref
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE host SET name=?, kind=?, transport=?, address=?, port=?, ssh_user=?, ssh_auth=?,
			                ssh_secret_ref=?, ssh_host_key=?, ssh_prev_host_key=?, ssh_host_key_changed_at=?,
			                transport_mode=?, dsm_url=?, dsm_user=?, dsm_secret_ref=?, dsm_device_token_ref=?,
			                info_json=?, status='online', last_seen_at=?
			WHERE id=?`,
			in.Name, in.Kind, in.Transport, in.Address, in.Port, in.SSHUser, in.SSHAuth,
			sshRef, hostKey, nullIfEmpty(prevKey), nullIfZero(changedAt),
			testRes.TransportMode, in.DSMURL, in.DSMUser, dsmRef, didRef,
			infoJSON, now, id,
		); err != nil {
			return err
		}
		return deleteSecrets(ctx, tx, obsolete...)
	})
	if err != nil {
		return nil, i18n.NewHostError("host.save_failed", err)
	}
	r.evictRuntimeAdapter(id)
	r.RefreshRuntimeAsync(id)
	return r.Get(ctx, id)
}

func buildSaveVerifyError(res *TestResult, lang i18n.Lang) error {
	msg := i18n.HostText(lang, "host.verify_failed")
	for i := len(res.Steps) - 1; i >= 0; i-- {
		if res.Steps[i].Status == "error" && res.Steps[i].Message != "" {
			msg = res.Steps[i].Message
			break
		}
	}
	code := "connection_failed"
	if res.NeedOTP {
		code = "need_otp"
	}
	return &SaveVerifyError{
		Code:       code,
		Message:    msg,
		TestResult: res,
	}
}

// Delete 删除主机（级联删除其下的 app / service）及其加密凭据记录。
func (r *Registry) Delete(ctx context.Context, id string) error {
	r.evictRuntimeAdapter(id)
	return r.withTx(ctx, func(tx *sql.Tx) error {
		refs, err := loadRefs(ctx, tx, id)
		if err != nil {
			var he *i18n.HostError
			if errors.As(err, &he) {
				return nil // 已不存在，视为删除成功
			}
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM host WHERE id=?`, id); err != nil {
			return err
		}
		return deleteSecrets(ctx, tx, refs.ssh, refs.dsm, refs.did)
	})
}

// TestConnection 执行分步连接诊断（按 lang 返回中/英诊断步骤），不会保存主机。
// 若 in.HostID 非空，则复用该主机已保存的 SSH 密码/私钥、DSM 密码与设备令牌。
// DSM 登录拿到新的设备令牌时暂存于服务端，并通过 DeviceTokenNonce 返回给前端，保存时带回复用。
func (r *Registry) TestConnection(ctx context.Context, in CreateOrUpdateInput, lang i18n.Lang) (*TestResult, error) {
	in.SSHHostKey = ""
	in.DSMDeviceToken = ""
	if in.HostID != "" {
		cur, err := r.Get(ctx, in.HostID)
		if err != nil {
			return nil, err
		}
		refs, err := loadRefs(ctx, r.st.DB, in.HostID)
		if err != nil {
			return nil, err
		}
		if in.Transport == "" {
			in.Transport = cur.Transport
		}
		if in.SSHAuth == "" {
			in.SSHAuth = cur.SSHAuth
		}
		if _, err := r.fillSavedCredentials(ctx, cur, refs, &in); err != nil {
			return nil, err
		}
	}
	res := r.testConnection(ctx, in, lang)
	if res.OK && res.DeviceToken != "" && res.DeviceToken != in.DSMDeviceToken {
		res.DeviceTokenNonce = r.stashDeviceToken(res.DeviceToken, in.DSMURL, in.DSMUser)
	}
	return res, nil
}

// testConnection 是实际的分步诊断实现（Create / Update 保存前强制校验也使用它）。
func (r *Registry) testConnection(ctx context.Context, in CreateOrUpdateInput, lang i18n.Lang) *TestResult {
	tr := func(zh, en string) string {
		if lang == i18n.EnUS {
			return en
		}
		return zh
	}
	res := &TestResult{}
	if in.Transport == "dsm_api" {
		ad := synology.New("test", nil, synology.DSMConfig{
			URL:           in.DSMURL,
			Username:      in.DSMUser,
			Secret:        in.DSMSecret,
			OTPCode:       in.DSMOTPCode,
			DeviceToken:   in.DSMDeviceToken,
			SkipTLSVerify: true,
		})
		defer ad.Close()
		info, err := ad.Probe(ctx)
		if err != nil {
			var authErr *synology.DSMAuthError
			if errors.As(err, &authErr) && authErr.NeedOTP {
				res.NeedOTP = true
			}
			res.Steps = append(res.Steps, StepResult{
				Step:    tr("群晖 DSM 网页接口登录与权限验证", "Synology DSM WebAPI login & permission check"),
				Status:  "error",
				Message: i18n.LocalizeErr(lang, err),
			})
			return res
		}
		arch := info.Arch
		if arch == "" {
			arch = tr("架构未知", "arch unknown")
		}
		res.Steps = append(res.Steps,
			StepResult{
				Step:    tr("群晖 DSM 网页接口登录与权限验证", "Synology DSM WebAPI login & permission check"),
				Status:  "ok",
				Message: fmt.Sprintf("%s (%s, %s)", in.DSMURL, info.PlatformVersion, arch),
			},
			StepResult{
				Step:    tr("群晖 Container Manager 原生项目与容器通道", "Synology Container Manager native project & container channel"),
				Status:  "ok",
				Message: tr("已启用免 SSH 网页接口直连（自动识别 Project、独立容器与套件中心容器）", "Agentless & SSH-free DSM WebAPI active (detects Projects, standalone & Package Center containers)"),
			},
		)
		res.OK = true
		res.TransportMode = "dsm_api"
		res.DeviceToken = ad.DeviceToken()
		res.Info = info
		return res
	}
	if in.Transport == "local" || in.Transport == "" {
		sock := transport.DefaultLocalSocket()
		if _, err := os.Stat(sock); err != nil {
			res.Steps = append(res.Steps, StepResult{
				Step:    tr("检查本机 Docker Socket", "Check local Docker Socket"),
				Status:  "error",
				Message: fmt.Sprintf(tr("未找到 %s：%v", "%s not found: %v"), sock, err),
			})
			return res
		}
		res.Steps = append(res.Steps, StepResult{
			Step:    tr("检查本机 Docker Socket", "Check local Docker Socket"),
			Status:  "ok",
			Message: sock,
		})
		lTr := transport.NewLocal(sock)
		defer lTr.Close()
		ad := docker.New("test", lTr)
		info, err := ad.Probe(ctx)
		if err != nil {
			res.Steps = append(res.Steps, StepResult{
				Step:    tr("读取 Docker Engine 信息", "Query Docker Engine info"),
				Status:  "error",
				Message: i18n.LocalizeErr(lang, err),
			})
			return res
		}
		res.Steps = append(res.Steps, StepResult{
			Step:    tr("读取 Docker Engine 信息", "Query Docker Engine info"),
			Status:  "ok",
			Message: fmt.Sprintf("Docker %s (%s/%s)", info.DockerVersion, info.OS, info.Arch),
		})
		res.OK = true
		res.TransportMode = string(transport.ModeLocal)
		res.Info = info
		return res
	}

	// SSH 连接测试：首次自动记录指纹；指纹变化时自动改用新指纹并标记“指纹已变化”
	port := in.Port
	if port <= 0 {
		port = 22
	}
	sshTr, prevKey, err := dialSSHRefreshingHostKey(ctx, transport.SSHConfig{
		Address:        in.Address,
		Port:           port,
		User:           in.SSHUser,
		AuthType:       in.SSHAuth,
		Secret:         in.SSHSecret,
		Passphrase:     in.SSHPassphrase,
		TrustedHostKey: in.SSHHostKey,
		AllowTOFU:      in.SSHHostKey == "",
	}, transport.DialSSH)
	if err != nil {
		res.Steps = append(res.Steps, StepResult{
			Step:    tr("SSH 连接与 Docker 通道探测", "SSH connection & Docker channel probe"),
			Status:  "error",
			Message: i18n.LocalizeErr(lang, err),
		})
		return res
	}
	defer sshTr.Close()

	res.PresentedHostKey = sshTr.PresentedHostKey()
	res.TransportMode = string(sshTr.Mode())
	fpMsg := i18n.HostText(lang, "host.fp", sshTr.PresentedHostKey())
	if prevKey != "" {
		res.HostKeyChanged = true
		res.PreviousHostKey = prevKey
		fpMsg = i18n.HostText(lang, "host.fp_changed", prevKey, sshTr.PresentedHostKey())
	}
	res.Steps = append(res.Steps,
		StepResult{
			Step:    tr("SSH 握手与认证", "SSH handshake & authentication"),
			Status:  "ok",
			Message: fpMsg,
		},
		StepResult{
			Step:    tr("Docker 通道探测", "Docker channel probe"),
			Status:  "ok",
			Message: describeMode(sshTr.Mode(), lang),
		},
	)

	var ad platform.Adapter
	if in.Kind == "synology" || isSynologyTarget(ctx, sshTr) {
		ad = synology.New("test", sshTr, synology.DSMConfig{
			URL:           in.DSMURL,
			Username:      in.DSMUser,
			Secret:        in.DSMSecret,
			SkipTLSVerify: true,
		})
	} else {
		ad = docker.New("test", sshTr)
	}
	info, err := ad.Probe(ctx)
	if err != nil {
		res.Steps = append(res.Steps, StepResult{
			Step:    tr("读取 Docker Engine 信息", "Query Docker Engine info"),
			Status:  "error",
			Message: i18n.LocalizeErr(lang, err),
		})
		return res
	}
	res.Steps = append(res.Steps, StepResult{
		Step:    tr("读取 Docker Engine 信息", "Query Docker Engine info"),
		Status:  "ok",
		Message: fmt.Sprintf("Docker %s · %s (%s/%s)", info.DockerVersion, info.PlatformVersion, info.OS, info.Arch),
	})
	if ad.Kind() == "synology" {
		if info.Capabilities.NativeProjects {
			res.Steps = append(res.Steps, StepResult{
				Step:    tr("群晖 Container Manager 原生项目发现", "Synology Container Manager native project discovery"),
				Status:  "ok",
				Message: tr("已启用（可自动识别群晖 Project 与套件中心容器）", "Enabled (detects DSM Projects & Package Center containers)"),
			})
		} else {
			res.Steps = append(res.Steps, StepResult{
				Step:    tr("群晖 Container Manager 原生项目发现", "Synology Container Manager native project discovery"),
				Status:  "skipped",
				Message: tr("仅使用标准 Docker/Compose 发现", "Using standard Docker/Compose discovery"),
			})
		}
	} else if in.Kind == "fnos" || isFnOSTarget(ctx, sshTr) {
		res.Steps = append(res.Steps, StepResult{
			Step:    tr("飞牛 fnOS 应用中心边界识别", "fnOS App Center boundary protection"),
			Status:  "ok",
			Message: tr("已启用（原位更新 Compose 项目，自动将 /var/apps 套件容器保护为只读）", "Enabled (in-place Compose updates; protects /var/apps packages as read-only)"),
		})
	} else if in.Kind == "openwrt" || isOpenWrtTarget(ctx, sshTr) {
		res.Steps = append(res.Steps, StepResult{
			Step:    tr("OpenWrt / iStoreOS 容器环境识别", "OpenWrt / iStoreOS container environment check"),
			Status:  "ok",
			Message: tr("已启用（支持原位更新独立容器与 Compose 项目）", "Enabled (in-place updates for standalone containers & Compose projects)"),
		})
	}
	res.OK = true
	res.Info = info
	return res
}

// dialSSHRefreshingHostKey 建立 SSH 连接；若远端指纹与已记录指纹不一致（家庭场景多为重装系统 / IP 变更），
// 自动改用远端出示的新指纹重连。返回值 prevKey 非空表示指纹已变化（为旧指纹）。
// 只依赖 transport 现有 API：用新指纹作为 TrustedHostKey 重新拨号。
func dialSSHRefreshingHostKey[T any](ctx context.Context, cfg transport.SSHConfig, dial func(context.Context, transport.SSHConfig) (T, error)) (T, string, error) {
	t, err := dial(ctx, cfg)
	var mm *transport.HostKeyMismatchError
	if err != nil && errors.As(err, &mm) && mm.Presented != "" {
		prev := mm.Expected
		if prev == "" {
			prev = cfg.TrustedHostKey
		}
		cfg.TrustedHostKey = mm.Presented
		cfg.AllowTOFU = false
		t, err = dial(ctx, cfg)
		if err != nil {
			var zero T
			return zero, "", err
		}
		return t, prev, nil
	}
	return t, "", err
}

// OpenAdapter 为指定主机建立传输并返回平台适配器（调用方负责 Close）。
func (r *Registry) OpenAdapter(ctx context.Context, hostID string) (platform.Adapter, *Host, error) {
	h, err := r.Get(ctx, hostID)
	if err != nil {
		return nil, nil, err
	}
	refs, err := loadRefs(ctx, r.st.DB, hostID)
	if err != nil {
		return nil, nil, err
	}

	var dsmSecret, dsmDeviceToken string
	if refs.dsm != "" {
		dsmSecret, _ = r.loadSecret(ctx, r.st.DB, refs.dsm)
	}
	if refs.did != "" {
		dsmDeviceToken, _ = r.loadSecret(ctx, r.st.DB, refs.did)
	}
	dsmCfg := synology.DSMConfig{
		URL:           h.DSMURL,
		Username:      h.DSMUser,
		Secret:        dsmSecret,
		DeviceToken:   dsmDeviceToken,
		SkipTLSVerify: true,
	}

	if h.Transport == "dsm_api" {
		h.TransportMode = "dsm_api"
		ad := synology.New(h.ID, nil, dsmCfg)
		// 主动登录一次：登录失败时立即标记离线
		if err := ad.Login(ctx); err != nil {
			_ = ad.Close()
			r.markOffline(ctx, h.ID)
			h.Status = "offline"
			return nil, h, err
		}
		return ad, h, nil
	}
	if h.Transport == "local" {
		tr := transport.NewLocal("")
		if h.Kind == "synology" || isSynologyTarget(ctx, tr) {
			return synology.New(h.ID, tr, dsmCfg), h, nil
		}
		return docker.New(h.ID, tr), h, nil
	}

	var payload sshSecretPayload
	if refs.ssh != "" {
		raw, err := r.loadSecret(ctx, r.st.DB, refs.ssh)
		if err != nil {
			return nil, h, i18n.NewHostError("host.secret_load", err)
		}
		if json.Unmarshal([]byte(raw), &payload) != nil {
			payload.Secret = raw
		}
	}
	cfg := transport.SSHConfig{
		Address:        h.Address,
		Port:           h.Port,
		User:           h.SSHUser,
		AuthType:       h.SSHAuth,
		Secret:         payload.Secret,
		Passphrase:     payload.Passphrase,
		TrustedHostKey: h.SSHHostKey,
		AllowTOFU:      h.SSHHostKey == "",
		PreferredMode:  transport.Mode(h.TransportMode),
	}
	sshTr, prevKey, err := dialSSHRefreshingHostKey(ctx, cfg, transport.DialSSH)
	if err != nil && h.TransportMode != "" {
		// 若首选模式失败，清空 PreferredMode 重新探测一次
		cfg.PreferredMode = ""
		sshTr, prevKey, err = dialSSHRefreshingHostKey(ctx, cfg, transport.DialSSH)
	}
	if err != nil {
		r.markOffline(ctx, h.ID)
		h.Status = "offline"
		return nil, h, err
	}
	prevMode := h.TransportMode
	h.TransportMode = string(sshTr.Mode())
	if h.TransportMode != "" && h.TransportMode != prevMode {
		_, _ = r.st.DB.ExecContext(ctx, `UPDATE host SET transport_mode=? WHERE id=?`, h.TransportMode, h.ID)
	}
	if presented := sshTr.PresentedHostKey(); presented != "" {
		if prevKey != "" {
			r.recordHostKeyChange(ctx, h.ID, prevKey, presented)
			h.SSHPrevHostKey = prevKey
			h.SSHHostKeyChangedAt = time.Now().Unix()
			h.SSHHostKey = presented
		} else if h.SSHHostKey == "" {
			_, _ = r.st.DB.ExecContext(ctx, `UPDATE host SET ssh_host_key=? WHERE id=?`, presented, h.ID)
			h.SSHHostKey = presented
		}
	}
	if h.Kind == "synology" || isSynologyTarget(ctx, sshTr) {
		if h.Kind != "synology" {
			h.Kind = "synology"
			_, _ = r.st.DB.ExecContext(ctx, `UPDATE host SET kind='synology' WHERE id=?`, h.ID)
		}
		return synology.New(h.ID, sshTr, dsmCfg), h, nil
	}
	if h.Kind != "fnos" && isFnOSTarget(ctx, sshTr) {
		h.Kind = "fnos"
		_, _ = r.st.DB.ExecContext(ctx, `UPDATE host SET kind='fnos' WHERE id=?`, h.ID)
	} else if h.Kind != "openwrt" && isOpenWrtTarget(ctx, sshTr) {
		h.Kind = "openwrt"
		_, _ = r.st.DB.ExecContext(ctx, `UPDATE host SET kind='openwrt' WHERE id=?`, h.ID)
	}
	return docker.New(h.ID, sshTr), h, nil
}

// recordHostKeyChange 自动更新为新指纹并标记“指纹已变化”。
func (r *Registry) recordHostKeyChange(ctx context.Context, id, prevKey, newKey string) {
	_, _ = r.st.DB.ExecContext(ctx, `
		UPDATE host SET ssh_host_key=?, ssh_prev_host_key=?, ssh_host_key_changed_at=? WHERE id=?`,
		newKey, prevKey, time.Now().Unix(), id)
}

func isSynologyTarget(ctx context.Context, tr transport.Transport) bool {
	b, err := tr.ReadFile(ctx, "/etc.defaults/VERSION")
	return err == nil && len(b) > 0
}

func isFnOSTarget(ctx context.Context, tr transport.Transport) bool {
	if b, err := tr.ReadFile(ctx, "/proc/sys/kernel/osrelease"); err == nil && strings.Contains(strings.ToLower(string(b)), "-trim") {
		return true
	}
	for _, p := range []string{"/usr/trim/etc/version", "/etc/trim_version"} {
		if b, err := tr.ReadFile(ctx, p); err == nil && len(strings.TrimSpace(string(b))) > 0 {
			return true
		}
	}
	return false
}

func isOpenWrtTarget(ctx context.Context, tr transport.Transport) bool {
	b, err := tr.ReadFile(ctx, "/etc/openwrt_release")
	return err == nil && len(strings.TrimSpace(string(b))) > 0
}

func refineDetectedKind(in *CreateOrUpdateInput, info *platform.HostInfo) {
	if in == nil || info == nil || in.Kind == "synology" || in.Transport == "dsm_api" {
		return
	}
	pv := strings.ToLower(strings.TrimSpace(info.PlatformVersion))
	switch {
	case strings.Contains(pv, "fnos"):
		in.Kind = "fnos"
	case strings.Contains(pv, "openwrt"), strings.Contains(pv, "istoreos"), strings.Contains(pv, "immortalwrt"), strings.Contains(pv, "kwrt"), strings.Contains(pv, "lede"):
		in.Kind = "openwrt"
	}
}

func (r *Registry) markOffline(ctx context.Context, id string) {
	_, _ = r.st.DB.ExecContext(ctx, `UPDATE host SET status='offline' WHERE id=?`, id)
}

// stashDeviceToken 暂存设备令牌（一次性、10 分钟有效），返回随机 nonce。
func (r *Registry) stashDeviceToken(token, url, user string) string {
	nonce := newID("dt_")
	now := time.Now()
	r.tokMu.Lock()
	defer r.tokMu.Unlock()
	for k, v := range r.tokens {
		if now.After(v.expires) {
			delete(r.tokens, k)
		}
	}
	r.tokens[nonce] = stashedToken{token: token, url: url, user: user, expires: now.Add(deviceTokenTTL)}
	return nonce
}

// takeDeviceToken 取出并删除暂存令牌；过期或 DSM 地址/账号不一致时返回空串。
func (r *Registry) takeDeviceToken(nonce, url, user string) string {
	r.tokMu.Lock()
	defer r.tokMu.Unlock()
	v, ok := r.tokens[nonce]
	if !ok {
		return ""
	}
	delete(r.tokens, nonce)
	if time.Now().After(v.expires) || v.url != url || v.user != user {
		return ""
	}
	return v.token
}

func (r *Registry) withTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := r.st.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func deleteSecrets(ctx context.Context, q dbtx, ids ...string) error {
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, err := q.ExecContext(ctx, `DELETE FROM secret WHERE id=?`, id); err != nil {
			return err
		}
	}
	return nil
}

func marshalInfo(info *platform.HostInfo) string {
	if info == nil {
		return ""
	}
	b, err := json.Marshal(info)
	if err != nil {
		return ""
	}
	return string(b)
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullIfZero(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

type sshSecretPayload struct {
	Secret     string `json:"secret"`
	Passphrase string `json:"passphrase,omitempty"`
}

func (r *Registry) saveSecret(ctx context.Context, q dbtx, plain string) (string, error) {
	nonce, sealed, err := r.box.Seal([]byte(plain))
	if err != nil {
		return "", err
	}
	id := newID("sec_")
	_, err = q.ExecContext(ctx, `INSERT INTO secret(id, nonce, cipher) VALUES (?, ?, ?)`, id, nonce, sealed)
	return id, err
}

func (r *Registry) loadSecret(ctx context.Context, q dbtx, id string) (string, error) {
	var nonce, cipher []byte
	if err := q.QueryRowContext(ctx, `SELECT nonce, cipher FROM secret WHERE id=?`, id).Scan(&nonce, &cipher); err != nil {
		return "", err
	}
	plain, err := r.box.Open(nonce, cipher)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func newID(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func describeMode(m transport.Mode, lang i18n.Lang) string {
	if lang == i18n.EnUS {
		switch m {
		case transport.ModeStreamLocal:
			return "Mode: SSH StreamLocal direct (/var/run/docker.sock)"
		case transport.ModeDialStdio:
			return "Mode: SSH docker system dial-stdio"
		case transport.ModeSudoStdio:
			return "Mode: SSH sudo docker system dial-stdio"
		case transport.ModeSudoPassStdio:
			return "Mode: SSH sudo (with admin password) docker system dial-stdio"
		default:
			return string(m)
		}
	}
	switch m {
	case transport.ModeStreamLocal:
		return "方式：SSH StreamLocal 直连 /var/run/docker.sock"
	case transport.ModeDialStdio:
		return "方式：SSH docker system dial-stdio"
	case transport.ModeSudoStdio:
		return "方式：SSH sudo docker system dial-stdio"
	case transport.ModeSudoPassStdio:
		return "方式：SSH sudo（管理员密码）docker system dial-stdio"
	default:
		return string(m)
	}
}
