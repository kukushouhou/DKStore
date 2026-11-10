// Package synology 实现群晖 DSM / Container Manager（及旧版 Docker 套件）的平台适配器（实施文档 §3.1 / §4.2）。
package synology

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/kukushouhou/DKStore/internal/i18n"
	"github.com/kukushouhou/DKStore/internal/platform"
	"github.com/kukushouhou/DKStore/internal/platform/docker"
	"github.com/kukushouhou/DKStore/internal/transport"
)

// DSMConfig 是群晖 DSM WebAPI 连接配置（支持纯 DSM WebAPI 免 SSH 模式，或作为 SSH 模式的补充）。
type DSMConfig struct {
	URL           string
	Username      string
	Secret        string
	OTPCode       string
	DeviceToken   string
	SkipTLSVerify bool
}

// dsmProject 对应 SYNO.Docker.Project method=list 返回的单个项目项。
type dsmProject struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Path         string   `json:"path"`
	Status       string   `json:"status"`
	IsPackage    bool     `json:"is_package"`
	ContainerIDs []string `json:"containerIds"`
}

type synoWebAPIExecutor interface {
	ExecSynoWebAPIProjectList(ctx context.Context) ([]byte, error)
}

// Adapter 实现群晖 DSM 平台适配器（兼容免 SSH 的纯 DSM WebAPI 模式与 SSH/Local 模式）。
type Adapter struct {
	hostID string
	tr     transport.Transport
	dock   *docker.Adapter
	dsm    DSMConfig
	dsmCli *dsmClient
}

// New 创建群晖 DSM 平台适配器。当 tr == nil 时工作在纯 DSM WebAPI 免 SSH 模式。
func New(hostID string, tr transport.Transport, dsm DSMConfig) *Adapter {
	var dock *docker.Adapter
	if tr != nil {
		dock = docker.New(hostID, tr)
	}
	var dsmCli *dsmClient
	if strings.TrimSpace(dsm.URL) != "" {
		dsmCli = newDSMClient(dsm)
	}
	return &Adapter{
		hostID: hostID,
		tr:     tr,
		dock:   dock,
		dsm:    dsm,
		dsmCli: dsmCli,
	}
}

func (a *Adapter) Kind() string { return "synology" }

// DeviceToken 返回 DSM WebAPI 2FA 登录成功后颁发的免二次验证设备令牌（did）。
func (a *Adapter) DeviceToken() string {
	if a.dsmCli == nil {
		return ""
	}
	return a.dsmCli.DeviceToken()
}

// Login 主动登录 DSM WebAPI（纯 dsm_api 模式下用于尽早发现登录失败）。
func (a *Adapter) Login(ctx context.Context) error {
	if a.dsmCli == nil {
		return i18n.NewHostError("dsm.not_configured", nil)
	}
	_, err := a.dsmCli.login(ctx)
	return err
}

// Close 注销 DSM 会话并关闭底层传输。
func (a *Adapter) Close() error {
	if a.dsmCli != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = a.dsmCli.Logout(ctx)
		cancel()
	}
	if a.dock != nil {
		return a.dock.Close()
	}
	return nil
}

// HasLocalImage 检查群晖本地 Docker 是否仍保留指定镜像。
func (a *Adapter) HasLocalImage(ctx context.Context, imageIDOrRef string) bool {
	if a.dock != nil {
		return a.dock.HasLocalImage(ctx, imageIDOrRef)
	}
	if a.dsmCli == nil {
		return false
	}
	imgs, err := a.dsmCli.ListImages(ctx)
	if err != nil {
		return false
	}
	target := strings.TrimSpace(imageIDOrRef)
	for _, img := range imgs {
		if img.ID != "" && sameImageID(img.ID, target) {
			return true
		}
		for _, t := range img.Tags {
			if img.Repository+":"+t == target || strings.TrimPrefix(img.Repository, "library/")+":"+t == target {
				return true
			}
		}
	}
	return false
}

// bi 生成中英双语文案（UpdateError 不携带语言，由前端/上层原样展示）。
func bi(zh, en string) string { return zh + " / " + en }

func biErr(err error) string {
	return bi(i18n.LocalizeErr(i18n.ZhCN, err), i18n.LocalizeErr(i18n.EnUS, err))
}

// 校验阶段轮询参数（测试中可调小）。
var (
	verifyTimeout  = 90 * time.Second
	verifyInterval = 3 * time.Second
)

// UpdateService 在群晖主机上执行单服务更新（保持 DSM Project 原生路径、YAML 与 Container Manager 状态同步，立项文档 §9.2 / A13 / A14）。
func (a *Adapter) UpdateService(
	ctx context.Context,
	req platform.UpdateRequest,
	onProgress func(platform.PullProgress),
) (*platform.UpdateResult, error) {
	if a.dock != nil {
		return a.dock.UpdateService(ctx, req, onProgress)
	}
	if a.dsmCli == nil {
		return nil, &platform.UpdateError{Code: "recreate_failed", Message: biErr(i18n.NewHostError("dsm.not_configured", nil))}
	}
	if req.Source == platform.SourceVendorManaged {
		return nil, &platform.UpdateError{
			Code:    "vendor_managed_readonly",
			Message: bi("群晖套件中心托管的容器受只读保护，请前往套件中心升级", "This container is managed by Synology Package Center, please upgrade it there"),
		}
	}

	emit := func(p platform.PullProgress) {
		if onProgress != nil {
			onProgress(p)
		}
	}
	emit(platform.PullProgress{Phase: "precheck", StatusText: "Prechecking DSM Container Manager state", Percent: 5})

	repo, tag := splitDSMRepoTag(req.TargetImageRef)
	isProject := req.Project != nil && req.Project.DSMProjectID != ""

	// 独立容器：DSM WebAPI 的 upgrade_start 只能把容器升级到同一 repo:tag 的新摘要，
	// 无法切换标签（版本更新/回退），必须在拉取前拒绝，避免“报成功但实际没切换”。
	var standalone *dsmContainerListItem
	if !isProject {
		containers, err := a.dsmCli.ListContainers(ctx)
		if err != nil {
			return nil, &platform.UpdateError{Code: "recreate_failed", Message: biErr(err)}
		}
		standalone = findStandaloneContainer(containers, req)
		oldRef := req.OldImageRef
		if oldRef == "" && standalone != nil {
			oldRef = containerImageRef(*standalone)
		}
		oldRepo, oldTag := splitDSMRepoTag(oldRef)
		if oldRef == "" || normalizeRepo(oldRepo) != normalizeRepo(repo) || oldTag != tag {
			return nil, &platform.UpdateError{
				Code: "dsm_tag_change_unsupported",
				Message: bi(
					fmt.Sprintf("该容器需通过 Compose 项目或 SSH 模式更新（群晖网页接口仅支持同标签新摘要升级，无法从 %s 切换到 %s）", oldRef, req.TargetImageRef),
					fmt.Sprintf("This container must be updated via a Compose project or SSH mode (DSM WebAPI only supports same-tag new-digest upgrades and cannot switch from %s to %s)", oldRef, req.TargetImageRef),
				),
			}
		}
		if standalone == nil {
			return nil, &platform.UpdateError{
				Code:    "recreate_failed",
				Message: bi("未在群晖容器列表中找到目标容器 "+req.AppName, "Target container not found in DSM container list: "+req.AppName),
			}
		}
	}

	if err := a.dsmCli.PullImage(ctx, repo, tag, onProgress); err != nil {
		return nil, &platform.UpdateError{
			Code:    "pull_failed",
			Message: fmt.Sprintf("%s:%s — %s", repo, tag, biErr(err)),
		}
	}
	// 拉取后通过镜像列表取得 repo:tag 对应的镜像 ID，作为校验阶段的期望值
	expectedID := ""
	if imgs, err := a.dsmCli.ListImages(ctx); err == nil {
		expectedID = findImageID(imgs, repo, tag)
	}

	if isProject {
		return a.updateProjectService(ctx, req, expectedID, emit)
	}
	return a.upgradeStandalone(ctx, req, repo, expectedID, standalone, emit)
}

func (a *Adapter) updateProjectService(ctx context.Context, req platform.UpdateRequest, expectedID string, emit func(platform.PullProgress)) (*platform.UpdateResult, error) {
	projectID := req.Project.DSMProjectID
	detail, err := a.dsmCli.GetProject(ctx, projectID)
	if err != nil {
		return nil, &platform.UpdateError{
			Code:    "recreate_failed",
			Message: fmt.Sprintf("%s — %s", req.Project.Name, biErr(err)),
		}
	}
	beforeYAML := detail.Content
	oldImageID := ""
	if c := findProjectServiceContainer(detail, req.ServiceName); c != nil {
		oldImageID = c.Image
	}
	if oldImageID == "" {
		oldImageID = req.ExpectedImageID
	}
	afterBytes, ok := docker.ReplaceServiceImageInYAML([]byte(beforeYAML), req.ServiceName, req.TargetImageRef)
	if !ok {
		return nil, &platform.UpdateError{
			Code: "recreate_failed",
			Message: bi(
				fmt.Sprintf("未能在项目 %s 的 compose.yaml 中定位服务 %s 的 image 字段", req.Project.Name, req.ServiceName),
				fmt.Sprintf("Cannot locate the image field of service %s in compose.yaml of project %s", req.ServiceName, req.Project.Name),
			),
		}
	}
	emit(platform.PullProgress{Phase: "recreating", StatusText: "Updating DSM Project YAML & rebuilding service", Percent: 85})
	if err := a.dsmCli.UpdateAndBuildProject(ctx, projectID, string(afterBytes)); err != nil {
		var pbe *ProjectBuildError
		restoreNote := ""
		if errors.As(err, &pbe) && pbe.YAMLWritten {
			restoreNote = a.restoreProjectYAML(ctx, projectID, beforeYAML)
		}
		return nil, &platform.UpdateError{
			Code:    "recreate_failed",
			Message: biErr(err) + restoreNote,
		}
	}

	emit(platform.PullProgress{Phase: "verifying", StatusText: "Verifying container image via DSM WebAPI", Percent: 95})
	var (
		found      *dsmContainerDetailed
		lastReason string
	)
	deadline := time.Now().Add(verifyTimeout)
	for {
		afterDetail, err := a.dsmCli.GetProject(ctx, projectID)
		if err != nil {
			lastReason = biErr(err)
		} else if c := findProjectServiceContainer(afterDetail, req.ServiceName); c == nil {
			lastReason = bi("未读到服务 "+req.ServiceName+" 的容器", "No container found for service "+req.ServiceName)
		} else if !imageSwitched(c.Image, expectedID, oldImageID) {
			lastReason = bi(
				fmt.Sprintf("容器镜像未切换为新拉取的镜像（当前 %s，期望 %s）", shortID(c.Image), shortID(expectedID)),
				fmt.Sprintf("container image did not switch to the newly pulled image (current %s, expected %s)", shortID(c.Image), shortID(expectedID)),
			)
		} else if c.State.Health.Status == "unhealthy" {
			found = nil
			lastReason = bi("容器健康检查未通过（unhealthy）", "container health check failed (unhealthy)")
			break
		} else if c.State.Health.Status == "starting" {
			cc := *c
			found = &cc
		} else {
			cc := *c
			found = &cc
			break
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(verifyInterval):
		}
	}
	if found == nil {
		restoreNote := a.restoreProjectYAML(ctx, projectID, beforeYAML)
		return nil, &platform.UpdateError{Code: "verify_failed", Message: lastReason + restoreNote}
	}

	state := found.State.Status
	if state == "" {
		if found.State.Running {
			state = "running"
		} else {
			state = "unknown"
		}
	}
	health := found.State.Health.Status
	if health == "" {
		health = "none"
	}
	sum := sha256.Sum256(afterBytes)
	composePath := strings.TrimRight(detail.Path, "/") + "/compose.yaml"
	emit(platform.PullProgress{Phase: "done", StatusText: "Update completed", Percent: 100})
	return &platform.UpdateResult{
		NewImageID:     found.Image,
		NewContainerID: found.ID,
		ContainerState: state,
		HealthStatus:   health,
		ComposePath:    composePath,
		ComposeSHA256:  hex.EncodeToString(sum[:]),
		Logs:           []string{"Updated via Synology DSM WebAPI SYNO.Docker.Project"},
	}, nil
}

// restoreProjectYAML 写回原 YAML 并尽力按原配置重建，返回附加到错误消息中的说明。
func (a *Adapter) restoreProjectYAML(ctx context.Context, projectID, beforeYAML string) string {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dsmLongCallTimeout)
	defer cancel()
	if err := a.dsmCli.call(rctx, "SYNO.Docker.Project", "1", "update", map[string]string{
		"id":      jsonParam(projectID),
		"content": jsonParam(beforeYAML),
	}, nil); err != nil {
		return " | " + bi("写回原 YAML 失败，请在 Container Manager 中手动检查："+i18n.LocalizeErr(i18n.ZhCN, err),
			"failed to restore original YAML, please check in Container Manager: "+i18n.LocalizeErr(i18n.EnUS, err))
	}
	if err := a.dsmCli.buildProject(rctx, projectID); err != nil {
		return " | " + bi("已写回原 YAML，但按原配置重建失败，请在 Container Manager 中手动构建",
			"original YAML restored, but rebuilding with it failed; please build it manually in Container Manager")
	}
	return " | " + bi("已写回原 YAML", "original YAML restored")
}

func (a *Adapter) upgradeStandalone(ctx context.Context, req platform.UpdateRequest, repo, expectedID string, before *dsmContainerListItem, emit func(platform.PullProgress)) (*platform.UpdateResult, error) {
	oldImageID := before.ImageID
	if oldImageID == "" {
		oldImageID = req.ExpectedImageID
	}
	emit(platform.PullProgress{Phase: "recreating", StatusText: "Upgrading container via DSM WebAPI", Percent: 85})
	if err := a.dsmCli.UpgradeByRepository(ctx, repo); err != nil {
		return nil, &platform.UpdateError{Code: "recreate_failed", Message: biErr(err)}
	}

	emit(platform.PullProgress{Phase: "verifying", StatusText: "Verifying container image via DSM WebAPI", Percent: 95})
	var (
		found      *dsmContainerListItem
		lastReason string
	)
	deadline := time.Now().Add(verifyTimeout)
	for {
		containers, err := a.dsmCli.ListContainers(ctx)
		if err != nil {
			lastReason = biErr(err)
		} else if c := findStandaloneContainer(containers, req); c == nil {
			lastReason = bi("升级后未找到容器 "+req.AppName, "Container not found after upgrade: "+req.AppName)
		} else if c.ImageID == "" || (oldImageID != "" && sameImageID(c.ImageID, oldImageID)) || (expectedID != "" && !sameImageID(c.ImageID, expectedID)) || (oldImageID == "" && expectedID == "") {
			lastReason = bi(
				fmt.Sprintf("容器镜像 ID 未改变或与新拉取的镜像不一致（当前 %s，原 %s，期望 %s）", shortID(c.ImageID), shortID(oldImageID), shortID(expectedID)),
				fmt.Sprintf("container image ID did not change or does not match the pulled image (current %s, old %s, expected %s)", shortID(c.ImageID), shortID(oldImageID), shortID(expectedID)),
			)
		} else {
			cc := *c
			found = &cc
			break
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(verifyInterval):
		}
	}
	if found == nil {
		return nil, &platform.UpdateError{Code: "verify_failed", Message: lastReason}
	}
	state := found.State.Status
	if state == "" {
		state = found.Status
	}
	if state == "" {
		state = "running"
	}
	emit(platform.PullProgress{Phase: "done", StatusText: "Update completed", Percent: 100})
	return &platform.UpdateResult{
		NewImageID:     found.ImageID,
		NewContainerID: found.ID,
		ContainerState: state,
		HealthStatus:   "none",
		Logs:           []string{"Upgraded via Synology DSM WebAPI SYNO.Docker.Image"},
	}, nil
}

// findProjectServiceContainer 在 Project 详情中查找目标服务的容器（仅一个容器时直接取用）。
func findProjectServiceContainer(detail *dsmProjectDetail, service string) *dsmContainerDetailed {
	if detail == nil {
		return nil
	}
	for i := range detail.Containers {
		if detail.Containers[i].Config.Labels["com.docker.compose.service"] == service {
			return &detail.Containers[i]
		}
	}
	if len(detail.Containers) == 1 {
		return &detail.Containers[0]
	}
	return nil
}

// findStandaloneContainer 按容器名（AppName / ServiceName）查找独立容器。
func findStandaloneContainer(list []dsmContainerListItem, req platform.UpdateRequest) *dsmContainerListItem {
	for _, want := range []string{req.AppName, req.ServiceName} {
		if want == "" {
			continue
		}
		for i := range list {
			if strings.TrimPrefix(list[i].Name, "/") == want {
				return &list[i]
			}
		}
	}
	return nil
}

func containerImageRef(c dsmContainerListItem) string {
	if c.Image != "" {
		return c.Image
	}
	return c.ImageAlt
}

// normalizeRepo 去掉 Docker Hub 默认前缀，便于比较仓库名。
func normalizeRepo(repo string) string {
	r := strings.ToLower(strings.TrimSpace(repo))
	for _, p := range []string{"docker.io/", "index.docker.io/", "registry-1.docker.io/"} {
		r = strings.TrimPrefix(r, p)
	}
	return strings.TrimPrefix(r, "library/")
}

// findImageID 在 DSM 镜像列表中查找 repo:tag 对应的镜像 ID。
func findImageID(imgs []dsmImageListItem, repo, tag string) string {
	want := normalizeRepo(repo)
	for _, img := range imgs {
		if normalizeRepo(img.Repository) != want {
			continue
		}
		for _, t := range img.Tags {
			if t == tag {
				return img.ID
			}
		}
	}
	return ""
}

func sameImageID(a, b string) bool {
	a = strings.TrimPrefix(strings.TrimSpace(a), "sha256:")
	b = strings.TrimPrefix(strings.TrimSpace(b), "sha256:")
	return a != "" && a == b
}

// imageSwitched 判断容器镜像是否已切换为新拉取的镜像：优先与期望镜像 ID 比对，取不到期望值时要求与旧镜像不同。
func imageSwitched(current, expected, old string) bool {
	if current == "" {
		return false
	}
	if expected != "" {
		return sameImageID(current, expected)
	}
	return old != "" && !sameImageID(current, old)
}

func shortID(id string) string {
	s := strings.TrimPrefix(id, "sha256:")
	if s == "" {
		return "-"
	}
	if len(s) > 12 {
		s = s[:12]
	}
	return s
}

// Probe 探测群晖 DSM 版本、Docker 引擎信息及原生 Project 发现能力。
func (a *Adapter) Probe(ctx context.Context) (*platform.HostInfo, error) {
	if a.dock == nil {
		if a.dsmCli == nil {
			return nil, i18n.NewHostError("dsm.not_configured", nil)
		}
		if _, err := a.dsmCli.currentSID(ctx); err != nil {
			return nil, err
		}
		ver := "DSM (WebAPI)"
		// Arch 取不到时保持为空（未知），不再默认 amd64；下游需按“未知架构”跳过平台级比对。
		arch := ""
		if sysInfo, err := a.dsmCli.GetDSMInfo(ctx); err == nil && sysInfo != nil {
			arch = sysInfo.Arch
			if sysInfo.VersionString != "" {
				ver = sysInfo.VersionString
				if sysInfo.Model != "" {
					ver = fmt.Sprintf("%s (%s)", sysInfo.VersionString, sysInfo.Model)
				}
			}
		}
		if _, err := a.dsmCli.ListProjects(ctx); err != nil {
			return nil, i18n.NewHostError("dsm.no_container_mgr", err)
		}
		return &platform.HostInfo{
			OS:              "linux",
			Arch:            arch,
			DockerVersion:   "Container Manager (DSM WebAPI)",
			PlatformVersion: ver,
			Capabilities: platform.Capabilities{
				Discover:            true,
				CheckUpdate:         true,
				NativeProjects:      true,
				PullImage:           true,
				UpdateSingleService: true,
				UpdateWholeProject:  true,
				NativeManagerSync:   true,
				Rollback:            true,
			},
		}, nil
	}

	info, err := a.dock.Probe(ctx)
	if err != nil {
		return nil, err
	}
	if ver := a.readDSMVersion(ctx); ver != "" {
		info.PlatformVersion = ver
	}
	if projs, err := a.listDSMProjects(ctx); err == nil && projs != nil {
		info.Capabilities.NativeProjects = true
		info.Capabilities.NativeManagerSync = true
	}
	return info, nil
}

// ListApps 列出群晖主机上的所有容器、Compose 项目，并叠加 Container Manager 原生项目与套件归属信息。
func (a *Adapter) ListApps(ctx context.Context) ([]platform.App, error) {
	if a.dock == nil {
		return a.listAppsViaDSMAPI(ctx)
	}

	apps, err := a.dock.ListApps(ctx)
	if err != nil {
		return nil, err
	}

	projs, _ := a.listDSMProjects(ctx)
	byPath := make(map[string]dsmProject, len(projs))
	byName := make(map[string]dsmProject, len(projs))
	for _, p := range projs {
		if p.Path != "" {
			byPath[strings.TrimRight(p.Path, "/")] = p
		}
		if p.Name != "" {
			byName[strings.ToLower(p.Name)] = p
		}
	}

	for i := range apps {
		app := &apps[i]
		var matched *dsmProject
		if app.Project != nil {
			cleanDir := strings.TrimRight(app.Project.WorkingDir, "/")
			if p, ok := byPath[cleanDir]; ok {
				matched = &p
			} else if p, ok := byName[strings.ToLower(app.Project.Name)]; ok {
				matched = &p
			}
		} else if p, ok := byName[strings.ToLower(app.Name)]; ok {
			matched = &p
		}

		if matched != nil {
			if matched.IsPackage {
				app.Source = platform.SourceVendorManaged
			} else {
				app.Source = platform.SourceDSMProject
			}
			if app.Project == nil {
				app.Project = &platform.ProjectInfo{
					Name:       matched.Name,
					WorkingDir: matched.Path,
				}
			}
			app.Project.DSMProjectID = matched.ID
			if app.Project.WorkingDir == "" {
				app.Project.WorkingDir = matched.Path
			}
			// 将 app.Key 升级为 dsm_project 前缀，与实施文档 §2.2 保持一致
			if app.Source == platform.SourceDSMProject {
				oldKey := app.Key
				newKey := fmt.Sprintf("%s/dsm_project/%s", a.hostID, app.Name)
				app.Key = newKey
				for j := range app.Services {
					app.Services[j].Key = strings.Replace(app.Services[j].Key, oldKey+"/", newKey+"/", 1)
				}
			}
		}

	// 兜底识别群晖套件中心（/var/packages/...）托管的容器
	if app.Project != nil && strings.HasPrefix(app.Project.WorkingDir, "/var/packages/") {
		app.Source = platform.SourceVendorManaged
	}
	}

	return apps, nil
}

func (a *Adapter) listAppsViaDSMAPI(ctx context.Context) ([]platform.App, error) {
	if a.dsmCli == nil {
		return nil, i18n.NewHostError("dsm.not_configured", nil)
	}
	projs, err := a.dsmCli.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	containers, err := a.dsmCli.ListContainers(ctx)
	if err != nil {
		return nil, err
	}
	images, _ := a.dsmCli.ListImages(ctx)
	digestByRepoTag := make(map[string]string, len(images))
	for _, img := range images {
		if img.Digest != "" {
			for _, t := range img.Tags {
				digestByRepoTag[img.Repository+":"+t] = img.Digest
			}
		}
	}

	claimedContainers := make(map[string]bool)
	var apps []platform.App

	for _, p := range projs {
		detail, err := a.dsmCli.GetProject(ctx, p.ID)
		if err != nil {
			continue
		}
		for _, cid := range detail.ContainerIDs {
			claimedContainers[cid] = true
		}

		src := platform.SourceDSMProject
		appKeyPrefix := "dsm_project"
		if detail.IsPackage || strings.HasPrefix(detail.Path, "/var/packages/") {
			src = platform.SourceVendorManaged
			appKeyPrefix = "compose"
		}
		appKey := fmt.Sprintf("%s/%s/%s", a.hostID, appKeyPrefix, detail.Name)
		cfgFile := strings.TrimRight(detail.Path, "/") + "/compose.yaml"

		svcMap := make(map[string]*platform.Service)
		var svcOrder []string
		for _, c := range detail.Containers {
			claimedContainers[c.ID] = true
			svcName := c.Config.Labels["com.docker.compose.service"]
			if svcName == "" {
				svcName = strings.TrimPrefix(c.Name, "/")
			}
			imgRef := c.Config.Image
			if imgRef == "" {
				imgRef = c.Image
			}
			svc, exists := svcMap[svcName]
			if !exists {
				var digests []string
				if d := digestByRepoTag[imgRef]; d != "" {
					digests = []string{imgRef + "@" + d}
				}
				svc = &platform.Service{
					Key:         fmt.Sprintf("%s/%s", appKey, svcName),
					Name:        svcName,
					State:       c.State.Status,
					ImageRef:    imgRef,
					ImageID:     c.Image,
					RepoDigests: digests,
					Labels:      c.Config.Labels,
				}
				svcMap[svcName] = svc
				svcOrder = append(svcOrder, svcName)
			}
			svc.ContainerIDs = append(svc.ContainerIDs, c.ID)
		}

		// 若某个停止的项目当前没有运行容器，但有内联 compose.yaml，从 YAML 中解析声明的服务与镜像
		if len(svcOrder) == 0 && detail.Content != "" {
			for svcName, imgRef := range parseServicesFromComposeYAML(detail.Content) {
				svcOrder = append(svcOrder, svcName)
				svcMap[svcName] = &platform.Service{
					Key:      fmt.Sprintf("%s/%s", appKey, svcName),
					Name:     svcName,
					State:    strings.ToLower(detail.Status),
					ImageRef: imgRef,
				}
			}
		}

		services := make([]platform.Service, 0, len(svcOrder))
		for _, name := range svcOrder {
			services = append(services, *svcMap[name])
		}
		apps = append(apps, platform.App{
			Key:    appKey,
			Name:   detail.Name,
			Source: src,
			Project: &platform.ProjectInfo{
				Name:           detail.Name,
				DSMProjectID:   detail.ID,
				WorkingDir:     detail.Path,
				ConfigFiles:    []string{cfgFile},
				SourceReadable: detail.Content != "",
			},
			Services: services,
		})
	}

	// 将不属于任何 DSM Project 的独立容器归纳为 standalone（或 vendor_managed）应用
	for _, c := range containers {
		if claimedContainers[c.ID] {
			continue
		}
		if projName := c.Labels["com.docker.compose.project"]; projName != "" {
			// 已在 Project 列表中处理过的同名项目跳过
			already := false
			for _, a := range apps {
				if strings.EqualFold(a.Name, projName) {
					already = true
					break
				}
			}
			if already {
				continue
			}
		}
		name := strings.TrimPrefix(c.Name, "/")
		if name == "" {
			name = c.ID
		}
		imgRef := c.Image
		if imgRef == "" {
			imgRef = c.ImageAlt
		}
		src := platform.SourceStandalone
		if c.IsPackage || strings.HasPrefix(c.Labels["com.docker.compose.project.working_dir"], "/var/packages/") {
			src = platform.SourceVendorManaged
		}
		appKey := fmt.Sprintf("%s/container/%s", a.hostID, name)
		var digests []string
		if d := digestByRepoTag[imgRef]; d != "" {
			digests = []string{imgRef + "@" + d}
		}
		state := c.State.Status
		if state == "" {
			state = c.Status
		}
		apps = append(apps, platform.App{
			Key:    appKey,
			Name:   name,
			Source: src,
			Services: []platform.Service{
				{
					Key:          appKey + "/main",
					Name:         name,
					ContainerIDs: []string{c.ID},
					State:        state,
					ImageRef:     imgRef,
					ImageID:      c.ImageID,
					RepoDigests:  digests,
					Labels:       c.Labels,
				},
			},
		})
	}

	return apps, nil
}

func computeDSMStat(st dsmContainerStat) (cpuPct float64, memUsage int64, memLimit int64) {
	if st.PreCPUStats.SystemCPUUsage > 0 && st.CPUStats.SystemCPUUsage > st.PreCPUStats.SystemCPUUsage && st.CPUStats.CPUUsage.TotalUsage > st.PreCPUStats.CPUUsage.TotalUsage {
		cpuDelta := st.CPUStats.CPUUsage.TotalUsage - st.PreCPUStats.CPUUsage.TotalUsage
		sysDelta := st.CPUStats.SystemCPUUsage - st.PreCPUStats.SystemCPUUsage
		onlineCPUs := st.CPUStats.OnlineCPUs
		if onlineCPUs <= 0 {
			onlineCPUs = len(st.CPUStats.CPUUsage.PercpuUsage)
		}
		if onlineCPUs <= 0 {
			onlineCPUs = 1
		}
		pct := (float64(cpuDelta) / float64(sysDelta)) * float64(onlineCPUs) * 100.0
		cpuPct = math.Round(pct*100) / 100
	} else if st.CPU > 0 {
		cpuPct = math.Round(st.CPU*100) / 100
	}
	if st.MemoryStats.Usage > 0 {
		inactive := st.MemoryStats.Stats["inactive_file"]
		if st.MemoryStats.Usage > inactive {
			memUsage = int64(st.MemoryStats.Usage - inactive)
		}
		memLimit = int64(st.MemoryStats.Limit)
	} else {
		memUsage = st.Memory
		memLimit = st.MemoryLimit
	}
	return
}

func parseDSMStartedAt(raw string) int64 {
	s := strings.TrimSpace(raw)
	if s == "" || strings.HasPrefix(s, "0001-") {
		return 0
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil && t.Year() > 1970 {
			return t.Unix()
		}
	}
	return 0
}

// ListRuntimeApps 采集群晖主机上全部容器与项目的树状实时运行状态与资源占用。
func (a *Adapter) ListRuntimeApps(ctx context.Context) ([]platform.AppRuntimeTree, error) {
	if a.dock == nil {
		return a.listRuntimeAppsViaDSMAPI(ctx)
	}

	trees, err := a.dock.ListRuntimeApps(ctx)
	if err != nil {
		return nil, err
	}

	projs, _ := a.listDSMProjects(ctx)
	byName := make(map[string]dsmProject, len(projs))
	for _, p := range projs {
		if p.Name != "" {
			byName[strings.ToLower(p.Name)] = p
		}
	}

	for i := range trees {
		t := &trees[i]
		if p, ok := byName[strings.ToLower(t.Name)]; ok {
			if p.IsPackage || strings.HasPrefix(p.Path, "/var/packages/") {
				t.Source = platform.SourceVendorManaged
			} else {
				t.Source = platform.SourceDSMProject
				t.Key = fmt.Sprintf("%s/dsm_project/%s", a.hostID, t.Name)
			}
		}
	}
	return trees, nil
}

func (a *Adapter) listRuntimeAppsViaDSMAPI(ctx context.Context) ([]platform.AppRuntimeTree, error) {
	if a.dsmCli == nil {
		return nil, i18n.NewHostError("dsm.not_configured", nil)
	}
	stats1, _ := a.dsmCli.ContainerStats(ctx)
	projs, err := a.dsmCli.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	containers, err := a.dsmCli.ListContainers(ctx)
	if err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
	case <-time.After(150 * time.Millisecond):
	}
	statsMap, _ := a.dsmCli.ContainerStats(ctx)
	if len(statsMap) == 0 {
		statsMap = stats1
	} else {
		for k, cur := range statsMap {
			if cur.PreCPUStats.SystemCPUUsage == 0 {
				if prev, ok := stats1[k]; ok {
					cur.PreCPUStats = prev.CPUStats
					statsMap[k] = cur
				}
			}
		}
	}

	byID := make(map[string]dsmContainerListItem, len(containers))
	byName := make(map[string]dsmContainerListItem, len(containers))
	for _, c := range containers {
		if c.ID != "" {
			byID[c.ID] = c
		}
		n := strings.TrimPrefix(c.Name, "/")
		if n != "" {
			byName[n] = c
		}
	}

	lookupStat := func(id, name string) (float64, int64, int64) {
		if st, ok := statsMap[name]; ok {
			return computeDSMStat(st)
		}
		if st, ok := statsMap[id]; ok {
			return computeDSMStat(st)
		}
		return 0, 0, 0
	}

	claimed := make(map[string]bool)
	var out []platform.AppRuntimeTree

	for _, p := range projs {
		detail, err := a.dsmCli.GetProject(ctx, p.ID)
		if err != nil {
			continue
		}
		for _, cid := range detail.ContainerIDs {
			claimed[cid] = true
		}

		src := platform.SourceDSMProject
		appKeyPrefix := "dsm_project"
		if detail.IsPackage || strings.HasPrefix(detail.Path, "/var/packages/") {
			src = platform.SourceVendorManaged
			appKeyPrefix = "compose"
		}
		appKey := fmt.Sprintf("%s/%s/%s", a.hostID, appKeyPrefix, detail.Name)
		tree := platform.AppRuntimeTree{
			Key:    appKey,
			HostID: a.hostID,
			Name:   detail.Name,
			Source: src,
		}

		for _, c := range detail.Containers {
			claimed[c.ID] = true
			cName := strings.TrimPrefix(c.Name, "/")
			if cName == "" {
				cName = shortID(c.ID)
			}
			claimed[cName] = true

			listItem, hasList := byID[c.ID]
			if !hasList {
				listItem, hasList = byName[cName]
			}

			state := strings.ToLower(strings.TrimSpace(c.State.Status))
			if state == "" && hasList {
				state = strings.ToLower(strings.TrimSpace(listItem.State.Status))
				if state == "" {
					state = strings.ToLower(strings.TrimSpace(listItem.Status))
				}
			}
			if state == "" {
				if c.State.Running || (hasList && listItem.State.Running) {
					state = "running"
				} else {
					state = "stopped"
				}
			}

			startedAt := parseDSMStartedAt(c.State.StartedAt)
			if startedAt == 0 && hasList {
				startedAt = parseDSMStartedAt(listItem.State.StartedAt)
			}
			uptime := ""
			if hasList {
				uptime = listItem.UpStatus
			}
			health := strings.ToLower(strings.TrimSpace(c.State.Health.Status))
			if health == "" && hasList {
				health = strings.ToLower(strings.TrimSpace(listItem.State.Health.Status))
			}

			imgRef := c.Config.Image
			if imgRef == "" && hasList {
				imgRef = containerImageRef(listItem)
			}
			if imgRef == "" {
				imgRef = c.Image
			}

			var cpu float64
			var memU, memL int64
			if state == "running" {
				cpu, memU, memL = lookupStat(c.ID, cName)
			}

			cStat := platform.ContainerRuntimeStat{
				ID:         shortID(c.ID),
				Name:       cName,
				Service:    strings.TrimSpace(c.Config.Labels["com.docker.compose.service"]),
				Image:      imgRef,
				State:      state,
				Health:     health,
				Uptime:     uptime,
				StartedAt:  startedAt,
				CPUPercent: cpu,
				MemUsage:   memU,
				MemLimit:   memL,
			}
			tree.TotalCount++
			if state == "running" {
				tree.RunningCount++
			}
			tree.CPUPercent = math.Round((tree.CPUPercent+cpu)*100) / 100
			tree.MemUsage += memU
			tree.Containers = append(tree.Containers, cStat)
		}

		sort.Slice(tree.Containers, func(i, j int) bool {
			return tree.Containers[i].Name < tree.Containers[j].Name
		})
		out = append(out, tree)
	}

	composeGroups := make(map[string]*platform.AppRuntimeTree)
	for _, c := range containers {
		cName := strings.TrimPrefix(c.Name, "/")
		if claimed[c.ID] || (cName != "" && claimed[cName]) {
			continue
		}
		if c.Labels["dkstore.self"] == "true" {
			continue
		}
		if cName == "" {
			cName = shortID(c.ID)
		}
		state := strings.ToLower(strings.TrimSpace(c.State.Status))
		if state == "" {
			state = strings.ToLower(strings.TrimSpace(c.Status))
		}
		if state == "" {
			if c.State.Running {
				state = "running"
			} else {
				state = "stopped"
			}
		}

		var cpu float64
		var memU, memL int64
		if state == "running" {
			cpu, memU, memL = lookupStat(c.ID, cName)
		}
		cStat := platform.ContainerRuntimeStat{
			ID:         shortID(c.ID),
			Name:       cName,
			Service:    strings.TrimSpace(c.Labels["com.docker.compose.service"]),
			Image:      containerImageRef(c),
			State:      state,
			Health:     strings.ToLower(strings.TrimSpace(c.State.Health.Status)),
			Uptime:     c.UpStatus,
			StartedAt:  parseDSMStartedAt(c.State.StartedAt),
			CPUPercent: cpu,
			MemUsage:   memU,
			MemLimit:   memL,
		}

		projName := strings.TrimSpace(c.Labels["com.docker.compose.project"])
		workDir := strings.TrimSpace(c.Labels["com.docker.compose.project.working_dir"])
		if projName != "" {
			already := false
			for _, existing := range out {
				if strings.EqualFold(existing.Name, projName) {
					already = true
					break
				}
			}
			if already {
				continue
			}
			appKey := fmt.Sprintf("%s/compose/%s", a.hostID, projName)
			grp, exists := composeGroups[appKey]
			if !exists {
				src := platform.SourceCompose
				if c.IsPackage || strings.HasPrefix(workDir, "/var/packages/") {
					src = platform.SourceVendorManaged
				}
				grp = &platform.AppRuntimeTree{
					Key:    appKey,
					HostID: a.hostID,
					Name:   projName,
					Source: src,
				}
				composeGroups[appKey] = grp
			}
			grp.TotalCount++
			if state == "running" {
				grp.RunningCount++
			}
			grp.CPUPercent = math.Round((grp.CPUPercent+cpu)*100) / 100
			grp.MemUsage += memU
			grp.Containers = append(grp.Containers, cStat)
			continue
		}

		src := platform.SourceStandalone
		if c.IsPackage || strings.HasPrefix(workDir, "/var/packages/") {
			src = platform.SourceVendorManaged
		}
		runCnt := 0
		if state == "running" {
			runCnt = 1
		}
		out = append(out, platform.AppRuntimeTree{
			Key:          fmt.Sprintf("%s/container/%s", a.hostID, cName),
			HostID:       a.hostID,
			Name:         cName,
			Source:       src,
			RunningCount: runCnt,
			TotalCount:   1,
			CPUPercent:   cpu,
			MemUsage:     memU,
			Containers:   []platform.ContainerRuntimeStat{cStat},
		})
	}

	for _, grp := range composeGroups {
		sort.Slice(grp.Containers, func(i, j int) bool {
			return grp.Containers[i].Name < grp.Containers[j].Name
		})
		out = append(out, *grp)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func parseServicesFromComposeYAML(raw string) map[string]string {
	out := make(map[string]string)
	lines := strings.Split(raw, "\n")
	inServices := false
	servicesIndent := -1
	curSvc := ""
	svcIndent := -1
	for _, line := range lines {
		trimmed := strings.TrimSpace(strings.TrimRight(line, "\r"))
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := 0
		for _, r := range line {
			if r == ' ' {
				indent++
			} else if r == '\t' {
				indent += 2
			} else {
				break
			}
		}
		if !inServices {
			if trimmed == "services:" || strings.HasPrefix(trimmed, "services:") {
				inServices = true
				servicesIndent = indent
			}
			continue
		}
		if indent <= servicesIndent {
			inServices = false
			curSvc = ""
			continue
		}
		if curSvc == "" || indent <= svcIndent {
			if strings.HasSuffix(trimmed, ":") && !strings.Contains(trimmed, " ") {
				curSvc = strings.TrimSuffix(trimmed, ":")
				svcIndent = indent
			}
			continue
		}
		if strings.HasPrefix(trimmed, "image:") {
			img := strings.TrimSpace(strings.TrimPrefix(trimmed, "image:"))
			if idx := strings.Index(img, "#"); idx >= 0 {
				img = strings.TrimSpace(img[:idx])
			}
			img = strings.Trim(img, `"'`)
			if img != "" {
				out[curSvc] = img
			}
		}
	}
	return out
}

func splitDSMRepoTag(ref string) (repo, tag string) {
	s := strings.TrimSpace(ref)
	if at := strings.Index(s, "@"); at >= 0 {
		s = s[:at]
	}
	lastSlash := strings.LastIndex(s, "/")
	colon := strings.LastIndex(s, ":")
	if colon > lastSlash {
		return s[:colon], s[colon+1:]
	}
	return s, "latest"
}

func (a *Adapter) readDSMVersion(ctx context.Context) string {
	if a.tr == nil {
		return ""
	}
	b, err := a.tr.ReadFile(ctx, "/etc.defaults/VERSION")
	if err != nil {
		return ""
	}
	kv := make(map[string]string)
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		kv[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	prod := kv["productversion"]
	if prod == "" {
		return ""
	}
	build := kv["buildnumber"]
	small := kv["smallfixnumber"]
	ver := "DSM " + prod
	if build != "" {
		ver += "-" + build
	}
	if small != "" && small != "0" {
		ver += " Update " + small
	}
	return ver
}

func (a *Adapter) listDSMProjects(ctx context.Context) ([]dsmProject, error) {
	// 1. 优先通过已认证的 SSH 传输层或本机执行只读 synowebapi（无需二次处理 DSM 2FA OTP）
	if a.tr != nil {
		if execTr, ok := a.tr.(synoWebAPIExecutor); ok {
			if raw, err := execTr.ExecSynoWebAPIProjectList(ctx); err == nil {
				if list, err := parseSynoWebAPIProjects(raw); err == nil {
					return list, nil
				}
			}
		} else if a.tr.Mode() == transport.ModeLocal {
			ctxCmd, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctxCmd, "/usr/syno/bin/synowebapi", "--exec", "api=SYNO.Docker.Project", "version=1", "method=list")
			if raw, err := cmd.Output(); err == nil {
				if list, err := parseSynoWebAPIProjects(raw); err == nil {
					return list, nil
				}
			}
		}
	}

	// 2. 若配置了 DSM HTTP WebAPI 客户端，通过 HTTP WebAPI 获取
	if a.dsmCli != nil && a.dsm.Username != "" && a.dsm.Secret != "" {
		return a.dsmCli.ListProjects(ctx)
	}
	return nil, i18n.NewHostError("dsm.not_configured", nil)
}

func parseSynoWebAPIProjects(raw []byte) ([]dsmProject, error) {
	s := strings.TrimSpace(string(raw))
	if idx := strings.Index(s, "\n{"); idx >= 0 {
		s = s[idx+1:]
	} else if idx := strings.Index(s, "{"); idx >= 0 {
		s = s[idx:]
	}
	var resp struct {
		Success bool                  `json:"success"`
		Data    map[string]dsmProject `json:"data"`
	}
	if err := json.Unmarshal([]byte(s), &resp); err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, i18n.NewHostError("dsm.synowebapi_failed", nil)
	}
	out := make([]dsmProject, 0, len(resp.Data))
	for k, p := range resp.Data {
		if p.ID == "" {
			p.ID = k
		}
		out = append(out, p)
	}
	return out, nil
}
