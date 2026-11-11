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
	"strconv"
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
	emit(platform.PullProgress{
		Phase:      "precheck",
		StatusText: "Prechecking DSM Container Manager state",
		Percent:    5,
		LogChunk:   "Prechecking DSM Container Manager state...\r\n",
	})

	repo, tag := splitDSMRepoTag(req.TargetImageRef)
	authUser, authPass := decodeRegistryAuth(req.RegistryAuthB64)
	isProject := req.Project != nil && req.Project.DSMProjectID != ""

	if isProject {
		oldRepo, oldTag := splitDSMRepoTag(req.OldImageRef)
		sameRepoTag := req.OldImageRef != "" && normalizeRepo(oldRepo) == normalizeRepo(repo) && oldTag == tag
		if sameRepoTag {
			// 同标签 digest_update：先尝试带正确 registry 地址调用 PullImage 拉取最新层；
			// 若群晖后台尚未将该第三方仓库登记到 SYNO.Docker.Registry（返回 1202/1051/1052 等 DSMError），
			// 不中断任务，继续交由 updateProjectService 的原生 build_stream 尝试拉取与重建校验（附录 E.1）。
			if err := a.dsmCli.PullImage(ctx, repo, tag, authUser, authPass, onProgress); err != nil {
				if ctx.Err() != nil {
					return nil, &platform.UpdateError{
						Code:    "pull_failed",
						Message: fmt.Sprintf("%s:%s — %s", repo, tag, biErr(err)),
					}
				}
				var de *DSMError
				if !errors.As(err, &de) {
					return nil, &platform.UpdateError{
						Code:    "pull_failed",
						Message: fmt.Sprintf("%s:%s — %s", repo, tag, biErr(err)),
					}
				}
				emit(platform.PullProgress{
					Phase:      "pulling",
					StatusText: "Fallback to native Docker Compose build_stream",
					Percent:    15,
					LogChunk:   fmt.Sprintf("DSM Image API returned code %d, delegating pull to native Compose build_stream...\r\n", de.Code),
				})
			}
		}
		return a.updateProjectService(ctx, req, repo, tag, emit)
	}

	containers, err := a.dsmCli.ListContainers(ctx)
	if err != nil {
		return nil, &platform.UpdateError{Code: "recreate_failed", Message: biErr(err)}
	}
	standalone := findStandaloneContainer(containers, req)
	oldRef := req.OldImageRef
	if oldRef == "" && standalone != nil {
		oldRef = containerImageRef(*standalone)
	}
	oldRepo, oldTag := splitDSMRepoTag(oldRef)
	sameRepoTag := oldRef != "" && normalizeRepo(oldRepo) == normalizeRepo(repo) && oldTag == tag

	isCLICompose := req.Source == platform.SourceCompose ||
		(standalone != nil && strings.TrimSpace(standalone.Labels["com.docker.compose.project"]) != "")
	if isCLICompose && !sameRepoTag {
		return nil, dsmCLIComposeUnsupportedErr()
	}
	if standalone == nil {
		return nil, &platform.UpdateError{
			Code:    "recreate_failed",
			Message: bi("未在群晖容器列表中找到目标容器 "+req.AppName, "Target container not found in DSM container list: "+req.AppName),
		}
	}
	if !sameRepoTag {
		return a.recreateStandalone(ctx, req, repo, tag, authUser, authPass, standalone, onProgress, emit, nil, nil)
	}

	if err := a.dsmCli.PullImage(ctx, repo, tag, authUser, authPass, onProgress); err != nil {
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
	return a.upgradeStandalone(ctx, req, repo, expectedID, standalone, emit)
}

func (a *Adapter) updateProjectService(
	ctx context.Context,
	req platform.UpdateRequest,
	repo, tag string,
	emit func(platform.PullProgress),
) (*platform.UpdateResult, error) {
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

	parser := platform.NewComposeStreamParser()
	emit(platform.PullProgress{
		Phase:      "pulling",
		StatusText: "Updating DSM Project YAML & streaming Docker Compose build",
		Percent:    10,
		LogChunk:   fmt.Sprintf("==> Updating DSM Project %s (service %s -> %s)\r\n", req.Project.Name, req.ServiceName, req.TargetImageRef),
	})
	if err := a.dsmCli.UpdateAndBuildProjectStream(ctx, projectID, string(afterBytes), func(chunk string) {
		emit(parser.Feed(chunk))
	}); err != nil {
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
	parser.Flush()

	expectedID := ""
	if imgs, err := a.dsmCli.ListImages(ctx); err == nil {
		expectedID = findImageID(imgs, repo, tag)
	}

	emit(platform.PullProgress{
		Phase:      "verifying",
		StatusText: "Verifying container image via DSM WebAPI",
		Percent:    95,
		LogChunk:   "Verifying container state & switched image via DSM WebAPI...\r\n",
	})
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

func dsmCLIComposeUnsupportedErr() *platform.UpdateError {
	return &platform.UpdateError{
		Code: "dsm_cli_compose_unsupported",
		Message: bi(
			"该容器是在终端里用命令安装的，管理后台连接无法修改其命令行配置文件，请改用 SSH 连接更新",
			"This container was installed via terminal commands; the DSM admin connection cannot modify its command-line config file, please switch to an SSH connection to update it",
		),
	}
}

// recreateStandalone 跨版本号升级、回退或修改独立单容器配置：
// 先读取原 profile 再拉取新镜像（拉取失败不影响运行中的旧容器），随后停止、删除并用新镜像/新配置重建，失败时自动用备份 profile 恢复原容器。
func (a *Adapter) recreateStandalone(
	ctx context.Context,
	req platform.UpdateRequest,
	repo, tag, authUser, authPass string,
	before *dsmContainerListItem,
	onProgress func(platform.PullProgress),
	emit func(platform.PullProgress),
	overrideEnv *[]platform.EnvVar,
	overridePorts *[]platform.PortBinding,
) (*platform.UpdateResult, error) {
	cName := strings.TrimSpace(req.ServiceName)
	if cName == "" || cName == "main" {
		if before != nil {
			cName = strings.TrimPrefix(before.Name, "/")
		}
		if cName == "" {
			cName = strings.TrimSpace(req.AppName)
		}
	}

	origProfile, wasRunning, err := a.dsmCli.GetContainerProfile(ctx, cName)
	if err != nil {
		return nil, &platform.UpdateError{Code: "recreate_failed", Message: biErr(err)}
	}
	if strings.TrimSpace(profileLabelValue(origProfile, "com.docker.compose.project")) != "" {
		return nil, dsmCLIComposeUnsupportedErr()
	}
	backupProfile := cloneProfile(origProfile)

	oldImageID := ""
	beforeID := ""
	if before != nil {
		oldImageID = before.ImageID
		beforeID = before.ID
	}
	if oldImageID == "" {
		oldImageID = req.ExpectedImageID
	}

	if err := a.dsmCli.PullImage(ctx, repo, tag, authUser, authPass, onProgress); err != nil {
		return nil, &platform.UpdateError{
			Code:    "pull_failed",
			Message: fmt.Sprintf("%s:%s — %s", repo, tag, biErr(err)),
		}
	}
	expectedID := ""
	if imgs, err := a.dsmCli.ListImages(ctx); err == nil {
		expectedID = findImageID(imgs, repo, tag)
	}

	newProfile := cloneProfile(origProfile)
	newProfile["image"] = req.TargetImageRef
	isConfigOverride := overrideEnv != nil || overridePorts != nil
	if overrideEnv != nil {
		applyDSMEnvToProfile(newProfile, *overrideEnv)
	}
	if overridePorts != nil {
		applyDSMPortsToProfile(newProfile, *overridePorts)
	}

	emit(platform.PullProgress{
		Phase:      "recreating",
		StatusText: "Recreating standalone container via DSM WebAPI",
		Percent:    85,
		LogChunk:   fmt.Sprintf("Recreating standalone container %s via DSM WebAPI...\r\n", cName),
	})
	if wasRunning {
		if err := a.dsmCli.StopContainer(ctx, cName); err != nil {
			return nil, &platform.UpdateError{Code: "recreate_failed", Message: biErr(err)}
		}
	}
	if err := a.dsmCli.DeleteContainer(ctx, cName); err != nil {
		return nil, &platform.UpdateError{Code: "recreate_failed", Message: biErr(err)}
	}

	restoreBackup := func(cleanCurrent bool) string {
		restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dsmDefaultTimeout)
		defer cancel()
		if cleanCurrent {
			_ = a.dsmCli.StopContainer(restoreCtx, cName)
			_ = a.dsmCli.DeleteContainer(restoreCtx, cName)
		}
		if rErr := a.dsmCli.CreateContainer(restoreCtx, backupProfile, wasRunning); rErr != nil {
			return " | " + bi(
				"恢复原容器失败，请在 Container Manager 中手动检查："+i18n.LocalizeErr(i18n.ZhCN, rErr),
				"failed to restore original container, please check in Container Manager: "+i18n.LocalizeErr(i18n.EnUS, rErr),
			)
		}
		return " | " + bi("已自动恢复原容器", "original container restored")
	}

	if err := a.dsmCli.CreateContainer(ctx, newProfile, wasRunning); err != nil {
		return nil, &platform.UpdateError{
			Code:    "recreate_failed",
			Message: biErr(err) + restoreBackup(false),
		}
	}

	emit(platform.PullProgress{
		Phase:      "verifying",
		StatusText: "Verifying container image via DSM WebAPI",
		Percent:    95,
		LogChunk:   "Verifying container state via DSM WebAPI...\r\n",
	})
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
			lastReason = bi("重建后未找到容器 "+cName, "Container not found after recreate: "+cName)
		} else if !imageSwitched(c.ImageID, expectedID, oldImageID) && !(isConfigOverride && expectedID == "" && c.ID != "" && c.ID != beforeID) {
			lastReason = bi(
				fmt.Sprintf("容器镜像 ID 未切换为新镜像（当前 %s，原 %s，期望 %s）", shortID(c.ImageID), shortID(oldImageID), shortID(expectedID)),
				fmt.Sprintf("container image ID did not switch to the new image (current %s, old %s, expected %s)", shortID(c.ImageID), shortID(oldImageID), shortID(expectedID)),
			)
		} else if !standaloneStateMatches(*c, wasRunning) {
			lastReason = bi("重建后容器运行状态未达到预期", "container running state does not match expected state after recreate")
		} else if strings.EqualFold(c.State.Health.Status, "unhealthy") {
			found = nil
			lastReason = bi("容器健康检查未通过（unhealthy）", "container health check failed (unhealthy)")
			break
		} else if strings.EqualFold(c.State.Health.Status, "starting") {
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
		restoreNote := ""
		if isConfigOverride {
			restoreNote = restoreBackup(true)
		}
		return nil, &platform.UpdateError{Code: "verify_failed", Message: lastReason + restoreNote}
	}

	state := found.State.Status
	if state == "" {
		state = found.Status
	}
	if state == "" {
		if wasRunning || found.State.Running {
			state = "running"
		} else {
			state = "stopped"
		}
	}
	health := found.State.Health.Status
	if health == "" {
		health = "none"
	}
	emit(platform.PullProgress{Phase: "done", StatusText: "Update completed", Percent: 100})
	return &platform.UpdateResult{
		NewImageID:     found.ImageID,
		NewContainerID: found.ID,
		ContainerState: state,
		HealthStatus:   health,
		Logs:           []string{"Recreated via Synology DSM WebAPI SYNO.Docker.Container"},
	}, nil
}

// GetStandaloneConfig 读取群晖独立容器当前生效的镜像、环境变量、端口映射与只读挂载卷（附录 E.4）。
func (a *Adapter) GetStandaloneConfig(ctx context.Context, appName, serviceName string) (*platform.StandaloneConfig, error) {
	if a.dock != nil {
		return a.dock.GetStandaloneConfig(ctx, appName, serviceName)
	}
	if a.dsmCli == nil {
		return nil, i18n.NewHostError("dsm.not_configured", nil)
	}
	cName := strings.TrimSpace(serviceName)
	if cName == "" || cName == "main" {
		cName = strings.TrimSpace(appName)
	}
	if containers, err := a.dsmCli.ListContainers(ctx); err == nil {
		if c := findStandaloneContainer(containers, platform.UpdateRequest{AppName: appName, ServiceName: serviceName}); c != nil {
			if n := strings.TrimPrefix(c.Name, "/"); n != "" {
				cName = n
			}
		}
	}
	prof, _, err := a.dsmCli.GetContainerProfile(ctx, cName)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(profileLabelValue(prof, "com.docker.compose.project")) != "" {
		return &platform.StandaloneConfig{
			Editable: false,
			Reason:   dsmCLIComposeUnsupportedErr().Message,
			ImageRef: anyString(prof["image"]),
			Env:      []platform.EnvVar{},
			Ports:    []platform.PortBinding{},
			Volumes:  []platform.VolumeBinding{},
		}, nil
	}
	return &platform.StandaloneConfig{
		Editable: true,
		ImageRef: anyString(prof["image"]),
		Env:      extractDSMEnv(prof),
		Ports:    extractDSMPorts(prof),
		Volumes:  extractDSMVolumes(prof),
	}, nil
}

// UpdateStandaloneConfig 拉取目标镜像并按更新后的环境变量/端口映射重建群晖独立容器，失败时自动恢复原备份 profile（附录 E.4）。
func (a *Adapter) UpdateStandaloneConfig(
	ctx context.Context,
	req platform.StandaloneUpdateRequest,
	onProgress func(platform.PullProgress),
) (*platform.UpdateResult, error) {
	if a.dock != nil {
		return a.dock.UpdateStandaloneConfig(ctx, req, onProgress)
	}
	if a.dsmCli == nil {
		return nil, &platform.UpdateError{Code: "recreate_failed", Message: biErr(i18n.NewHostError("dsm.not_configured", nil))}
	}
	emit := func(p platform.PullProgress) {
		if onProgress != nil {
			onProgress(p)
		}
	}
	emit(platform.PullProgress{
		Phase:      "precheck",
		StatusText: "Prechecking standalone container configuration",
		Percent:    5,
		LogChunk:   "Prechecking DSM standalone container configuration...\r\n",
	})

	containers, err := a.dsmCli.ListContainers(ctx)
	if err != nil {
		return nil, &platform.UpdateError{Code: "recreate_failed", Message: biErr(err)}
	}
	upReq := platform.UpdateRequest{
		AppName:         req.AppName,
		ServiceName:     req.ServiceName,
		Source:          platform.SourceStandalone,
		ExpectedImageID: req.ExpectedImageID,
		OldImageRef:     req.OldImageRef,
		TargetImageRef:  req.TargetImageRef,
		RegistryAuthB64: req.RegistryAuthB64,
	}
	standalone := findStandaloneContainer(containers, upReq)
	if standalone == nil {
		return nil, &platform.UpdateError{
			Code:    "recreate_failed",
			Message: bi("未在群晖容器列表中找到目标容器 "+req.AppName, "Target container not found in DSM container list: "+req.AppName),
		}
	}
	if strings.TrimSpace(standalone.Labels["com.docker.compose.project"]) != "" {
		return nil, dsmCLIComposeUnsupportedErr()
	}
	repo, tag := splitDSMRepoTag(req.TargetImageRef)
	authUser, authPass := decodeRegistryAuth(req.RegistryAuthB64)
	envCopy := append([]platform.EnvVar(nil), req.Env...)
	portsCopy := append([]platform.PortBinding(nil), req.Ports...)
	return a.recreateStandalone(ctx, upReq, repo, tag, authUser, authPass, standalone, onProgress, emit, &envCopy, &portsCopy)
}

func extractDSMEnv(prof map[string]any) []platform.EnvVar {
	out := []platform.EnvVar{}
	raw, ok := prof["env_variables"].([]any)
	if !ok {
		return out
	}
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		k := strings.TrimSpace(anyString(m["key"]))
		if k == "" {
			continue
		}
		out = append(out, platform.EnvVar{
			Key:   k,
			Value: anyString(m["value"]),
		})
	}
	return out
}

func applyDSMEnvToProfile(prof map[string]any, env []platform.EnvVar) {
	items := make([]map[string]string, 0, len(env))
	for _, e := range env {
		k := strings.TrimSpace(e.Key)
		if k == "" {
			continue
		}
		items = append(items, map[string]string{
			"key":   k,
			"value": e.Value,
		})
	}
	prof["env_variables"] = items
}

func extractDSMPorts(prof map[string]any) []platform.PortBinding {
	out := []platform.PortBinding{}
	raw, ok := prof["port_bindings"].([]any)
	if !ok {
		return out
	}
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		cPort := anyString(m["container_port"])
		hPort := anyString(m["host_port"])
		proto := strings.ToLower(anyString(m["type"]))
		if proto == "" {
			proto = "tcp"
		}
		if cPort == "" {
			continue
		}
		out = append(out, platform.PortBinding{
			HostIP:        anyString(m["host_ip"]),
			HostPort:      hPort,
			ContainerPort: cPort,
			Protocol:      proto,
		})
	}
	return out
}

func applyDSMPortsToProfile(prof map[string]any, ports []platform.PortBinding) {
	items := make([]map[string]any, 0, len(ports))
	for _, p := range ports {
		cPortStr := strings.TrimSpace(p.ContainerPort)
		if cPortStr == "" {
			continue
		}
		proto := strings.ToLower(strings.TrimSpace(p.Protocol))
		if proto == "" {
			proto = "tcp"
		}
		entry := map[string]any{
			"type": proto,
		}
		if cInt, err := strconv.Atoi(cPortStr); err == nil {
			entry["container_port"] = cInt
		} else {
			entry["container_port"] = cPortStr
		}
		hPortStr := strings.TrimSpace(p.HostPort)
		if hPortStr != "" {
			if hInt, err := strconv.Atoi(hPortStr); err == nil {
				entry["host_port"] = hInt
			} else {
				entry["host_port"] = hPortStr
			}
		} else {
			entry["host_port"] = ""
		}
		if hip := strings.TrimSpace(p.HostIP); hip != "" {
			entry["host_ip"] = hip
		}
		items = append(items, entry)
	}
	prof["port_bindings"] = items
}

func extractDSMVolumes(prof map[string]any) []platform.VolumeBinding {
	out := []platform.VolumeBinding{}
	raw, ok := prof["volume_bindings"].([]any)
	if !ok {
		return out
	}
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		hp := anyString(m["host_volume_file"])
		if hp == "" {
			hp = anyString(m["host_path"])
		}
		cp := anyString(m["mount_point"])
		if cp == "" {
			cp = anyString(m["container_path"])
		}
		mode := strings.ToLower(anyString(m["type"]))
		if mode != "ro" {
			mode = "rw"
		}
		if hp == "" && cp == "" {
			continue
		}
		out = append(out, platform.VolumeBinding{
			HostPath:      hp,
			ContainerPath: cp,
			Mode:          mode,
		})
	}
	return out
}

func standaloneStateMatches(c dsmContainerListItem, wantRunning bool) bool {
	st := strings.ToLower(strings.TrimSpace(c.State.Status))
	if st == "" {
		st = strings.ToLower(strings.TrimSpace(c.Status))
	}
	if wantRunning {
		if c.State.Running || st == "running" || strings.HasPrefix(st, "up") {
			return true
		}
		// 若列表项未填充任何状态字段（仅含 id/name/ImageID），视为不阻塞
		return st == ""
	}
	return !c.State.Running && st != "running"
}

func profileLabelValue(profile map[string]any, key string) string {
	if profile == nil {
		return ""
	}
	switch labels := profile["labels"].(type) {
	case map[string]any:
		return anyString(labels[key])
	case map[string]string:
		return strings.TrimSpace(labels[key])
	case []any:
		for _, item := range labels {
			if m, ok := item.(map[string]any); ok && anyString(m["key"]) == key {
				return anyString(m["value"])
			}
		}
	}
	return ""
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

// findStandaloneContainer 按容器名（AppName / ServiceName）或命令行 Compose 标签查找非 DSM Project 容器。
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
	if req.AppName != "" && req.ServiceName != "" {
		for i := range list {
			if strings.EqualFold(list[i].Labels["com.docker.compose.project"], req.AppName) &&
				list[i].Labels["com.docker.compose.service"] == req.ServiceName {
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

func normalizeDSMDigest(d string) string {
	d = strings.TrimSpace(d)
	if d == "" || strings.Contains(d, ":") {
		return d
	}
	if len(d) == 64 {
		return "sha256:" + strings.ToLower(d)
	}
	return d
}

func normalizeDSMImageID(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	return strings.TrimPrefix(id, "sha256:")
}

// GetProjectCompose 读取群晖 DSM 项目（或 SSH/本机 Compose 项目）的当前 compose.yaml 内容。
func (a *Adapter) GetProjectCompose(ctx context.Context, project *platform.ProjectInfo) (string, error) {
	if a.dock != nil {
		if content, err := a.dock.GetProjectCompose(ctx, project); err == nil {
			return content, nil
		} else if a.dsmCli == nil || project == nil || project.DSMProjectID == "" {
			return "", err
		}
	}
	if a.dsmCli == nil {
		return "", i18n.NewHostError("dsm.not_configured", nil)
	}
	if project == nil || strings.TrimSpace(project.DSMProjectID) == "" {
		return "", i18n.NewEngineError(i18n.MsgComposeNotEditable)
	}
	detail, err := a.dsmCli.GetProject(ctx, project.DSMProjectID)
	if err != nil {
		return "", err
	}
	return detail.Content, nil
}

// UpdateProjectCompose 按新版完整 YAML 更新群晖项目：校验 services、备份原配置、写入新版 YAML 并流式执行 build_stream 拉取与重建；
// 失败自动写回原 YAML 并恢复（附录 D.4 / 附录 E.1）。
func (a *Adapter) UpdateProjectCompose(
	ctx context.Context,
	appName string,
	project *platform.ProjectInfo,
	newYAML string,
	onProgress func(platform.PullProgress),
) error {
	if a.dock != nil {
		return a.dock.UpdateProjectCompose(ctx, appName, project, newYAML, onProgress)
	}
	if a.dsmCli == nil {
		return &platform.UpdateError{Code: "recreate_failed", Message: biErr(i18n.NewHostError("dsm.not_configured", nil))}
	}
	if project == nil || strings.TrimSpace(project.DSMProjectID) == "" {
		return i18n.NewEngineError(i18n.MsgComposeNotEditable)
	}
	if _, err := docker.ValidateComposeYAML(newYAML); err != nil {
		return err
	}

	detail, err := a.dsmCli.GetProject(ctx, project.DSMProjectID)
	if err != nil {
		return &platform.UpdateError{Code: "recreate_failed", Message: biErr(err)}
	}
	beforeYAML := detail.Content

	emit := func(p platform.PullProgress) {
		if onProgress != nil {
			onProgress(p)
		}
	}
	emit(platform.PullProgress{
		Phase:      "precheck",
		StatusText: "Prechecking DSM project compose update",
		Percent:    5,
		LogChunk:   fmt.Sprintf("==> Preparing DSM Project %s compose configuration update...\r\n", project.Name),
	})

	parser := platform.NewComposeStreamParser()
	emit(platform.PullProgress{
		Phase:      "pulling",
		StatusText: "Updating DSM project compose.yaml & streaming Docker Compose build",
		Percent:    10,
		LogChunk:   "Writing updated compose.yaml and starting native Docker Compose build_stream...\r\n",
	})
	if err := a.dsmCli.UpdateAndBuildProjectStream(ctx, project.DSMProjectID, newYAML, func(chunk string) {
		emit(parser.Feed(chunk))
	}); err != nil {
		var pbe *ProjectBuildError
		restoreNote := ""
		if !errors.As(err, &pbe) || pbe.YAMLWritten {
			restoreNote = a.restoreProjectYAML(ctx, project.DSMProjectID, beforeYAML)
		}
		return &platform.UpdateError{
			Code:    "recreate_failed",
			Message: biErr(err) + restoreNote,
		}
	}
	parser.Flush()
	emit(platform.PullProgress{
		Phase:      "done",
		StatusText: "Compose project updated",
		Percent:    100,
		LogChunk:   "Compose project build_stream completed.\r\n",
	})
	return nil
}

// findImageID 在 DSM 镜像列表中查找 repo:tag 对应的镜像 ID（兼容带域名前缀与剥离域名前缀两种仓库记录格式）。
func findImageID(imgs []dsmImageListItem, repo, tag string) string {
	want := normalizeRepo(repo)
	_, stripped := splitDSMRegistryAndRepo(repo)
	wantStripped := normalizeRepo(stripped)
	for _, img := range imgs {
		normImg := normalizeRepo(img.Repository)
		if normImg != want && (wantStripped == "" || normImg != wantStripped) {
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
	digestByRepoTag := make(map[string]string, len(images)*2)
	digestByImageID := make(map[string]string, len(images))
	for _, img := range images {
		normDig := normalizeDSMDigest(img.Digest)
		if normDig == "" {
			continue
		}
		if idKey := normalizeDSMImageID(img.ID); idKey != "" {
			digestByImageID[idKey] = normDig
		}
		tags := img.Tags
		if len(tags) == 0 {
			tags = []string{"latest"}
		}
		normImgRepo := normalizeRepo(img.Repository)
		for _, t := range tags {
			t = strings.TrimSpace(t)
			if t == "" {
				t = "latest"
			}
			digestByRepoTag[strings.ToLower(strings.TrimSpace(img.Repository))+":"+t] = normDig
			if normImgRepo != "" {
				digestByRepoTag[normImgRepo+":"+t] = normDig
			}
		}
	}
	lookupDigests := func(imgRef, imageID string) []string {
		repo, tag := splitDSMRepoTag(imgRef)
		var d string
		for _, cand := range []string{
			strings.ToLower(strings.TrimSpace(imgRef)),
			strings.ToLower(strings.TrimSpace(repo)) + ":" + tag,
			normalizeRepo(repo) + ":" + tag,
		} {
			if cand != "" {
				if v := digestByRepoTag[cand]; v != "" {
					d = v
					break
				}
			}
		}
		if d == "" {
			if idKey := normalizeDSMImageID(imageID); idKey != "" {
				d = digestByImageID[idKey]
			}
		}
		if d == "" {
			return nil
		}
		baseRepo := repo
		if baseRepo == "" {
			baseRepo = imgRef
		}
		return []string{baseRepo + "@" + d}
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
				digests := lookupDigests(imgRef, c.Image)
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
					Key:         fmt.Sprintf("%s/%s", appKey, svcName),
					Name:        svcName,
					State:       strings.ToLower(detail.Status),
					ImageRef:    imgRef,
					RepoDigests: lookupDigests(imgRef, ""),
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

	// 将不属于任何 DSM Project 的容器归纳为 CLI Compose 项目（SourceCompose）、套件托管（vendor_managed）或普通独立容器（standalone）
	cliComposeApps := make(map[string]*platform.App)
	var cliComposeOrder []string
	for _, c := range containers {
		if claimedContainers[c.ID] {
			continue
		}
		name := strings.TrimPrefix(c.Name, "/")
		if name == "" {
			name = c.ID
		}
		imgRef := c.Image
		if imgRef == "" {
			imgRef = c.ImageAlt
		}
		digests := lookupDigests(imgRef, c.ImageID)
		state := c.State.Status
		if state == "" {
			state = c.Status
		}
		workDir := strings.TrimSpace(c.Labels["com.docker.compose.project.working_dir"])
		isPkg := c.IsPackage || strings.HasPrefix(workDir, "/var/packages/")

		if projName := strings.TrimSpace(c.Labels["com.docker.compose.project"]); projName != "" {
			// 已在 DSM Project 列表中处理过的同名项目跳过
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
			appKey := fmt.Sprintf("%s/compose/%s", a.hostID, projName)
			app, exists := cliComposeApps[appKey]
			if !exists {
				src := platform.SourceCompose
				if isPkg {
					src = platform.SourceVendorManaged
				}
				var cfgFiles []string
				for _, p := range strings.Split(c.Labels["com.docker.compose.project.config_files"], ",") {
					if p = strings.TrimSpace(p); p != "" {
						cfgFiles = append(cfgFiles, p)
					}
				}
				app = &platform.App{
					Key:    appKey,
					Name:   projName,
					Source: src,
					Project: &platform.ProjectInfo{
						Name:           projName,
						WorkingDir:     workDir,
						ConfigFiles:    cfgFiles,
						SourceReadable: false,
					},
				}
				cliComposeApps[appKey] = app
				cliComposeOrder = append(cliComposeOrder, appKey)
			}
			svcName := strings.TrimSpace(c.Labels["com.docker.compose.service"])
			if svcName == "" {
				svcName = name
			}
			svcKey := fmt.Sprintf("%s/%s", appKey, svcName)
			merged := false
			for i := range app.Services {
				if app.Services[i].Key == svcKey {
					app.Services[i].ContainerIDs = append(app.Services[i].ContainerIDs, c.ID)
					if state == "running" {
						app.Services[i].State = "running"
					}
					merged = true
					break
				}
			}
			if !merged {
				app.Services = append(app.Services, platform.Service{
					Key:          svcKey,
					Name:         svcName,
					ContainerIDs: []string{c.ID},
					State:        state,
					ImageRef:     imgRef,
					ImageID:      c.ImageID,
					RepoDigests:  digests,
					Labels:       c.Labels,
				})
			}
			continue
		}

		src := platform.SourceStandalone
		if isPkg {
			src = platform.SourceVendorManaged
		}
		appKey := fmt.Sprintf("%s/container/%s", a.hostID, name)
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
	for _, k := range cliComposeOrder {
		app := cliComposeApps[k]
		sort.Slice(app.Services, func(i, j int) bool {
			return app.Services[i].Name < app.Services[j].Name
		})
		apps = append(apps, *app)
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
