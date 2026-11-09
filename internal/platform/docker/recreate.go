package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/kukushouhou/DKStore/internal/i18n"
	"github.com/kukushouhou/DKStore/internal/platform"
	"github.com/kukushouhou/DKStore/internal/transport"
)

// restoreTimeout 是失败回滚（恢复旧容器、清理新容器）使用的独立超时，不受已取消的任务 ctx 影响（审计 L8）。
const restoreTimeout = 60 * time.Second

func (a *Adapter) findTargetContainer(ctx context.Context, req platform.UpdateRequest) (containerID string, rawInspect map[string]any, wasRunning bool, err error) {
	var list []containerSummary
	if err := a.getJSON(ctx, "/containers/json?all=1", &list); err != nil {
		return "", nil, false, err
	}
	var matchedID string
	for _, cs := range list {
		if req.Source == platform.SourceCompose || req.Source == platform.SourceDSMProject {
			projName := req.AppName
			if req.Project != nil && req.Project.Name != "" {
				projName = req.Project.Name
			}
			if cs.Labels["com.docker.compose.project"] == projName &&
				cs.Labels["com.docker.compose.service"] == req.ServiceName {
				matchedID = cs.ID
				break
			}
		} else {
			for _, n := range cs.Names {
				clean := strings.TrimPrefix(n, "/")
				if clean == req.ServiceName || clean == req.AppName {
					matchedID = cs.ID
					break
				}
			}
			if matchedID != "" {
				break
			}
		}
	}
	if matchedID == "" {
		return "", nil, false, i18n.NewEngineError(i18n.MsgDockerContainerNotFound, req.AppName, req.ServiceName)
	}

	raw := make(map[string]any)
	if err := a.getJSON(ctx, "/containers/"+url.PathEscape(matchedID)+"/json", &raw); err != nil {
		return "", nil, false, err
	}
	running := false
	if st, ok := raw["State"].(map[string]any); ok {
		if r, ok := st["Running"].(bool); ok && r {
			running = true
		} else if status, _ := st["Status"].(string); status == "running" {
			running = true
		}
	}
	return matchedID, raw, running, nil
}

// buildCreatePayload 生成新容器的创建参数（审计 H6）：
//   - 只继承用户自定义的 Config：与旧镜像默认 Config 相同的 Env 项、Cmd、Entrypoint、WorkingDir、User、Labels、
//     ExposedPorts、Healthcheck、Volumes、StopSignal 不再传递，让新镜像的默认值生效；
//   - 旧容器 Mounts 中 Type=volume 且未在 HostConfig 中声明的卷（匿名卷或镜像 VOLUME 卷）以同名卷挂载到新容器。
//
// imageCfg 为 nil（读取旧镜像失败）时退化为完整复制旧容器 Config。
func buildCreatePayload(oldID string, rawInspect map[string]any, imageCfg map[string]any, targetImageRef string) map[string]any {
	cfgMap, _ := rawInspect["Config"].(map[string]any)
	payload := make(map[string]any, len(cfgMap)+2)
	for k, v := range cfgMap {
		payload[k] = v
	}
	payload["Image"] = targetImageRef
	if hn, _ := payload["Hostname"].(string); hn != "" && strings.HasPrefix(oldID, hn) {
		delete(payload, "Hostname")
	}

	if imageCfg != nil {
		diffConfigAgainstImage(payload, imageCfg)
	}

	if hostCfg, ok := rawInspect["HostConfig"].(map[string]any); ok && hostCfg != nil {
		hc := make(map[string]any, len(hostCfg)+1)
		for k, v := range hostCfg {
			hc[k] = v
		}
		attachExistingVolumes(hc, rawInspect["Mounts"])
		payload["HostConfig"] = hc
	} else if extra := volumeBindsFromMounts(nil, nil, rawInspect["Mounts"]); len(extra) > 0 {
		payload["HostConfig"] = map[string]any{"Binds": toAnySlice(extra)}
	}
	return payload
}

// diffConfigAgainstImage 从 payload 中移除与旧镜像默认 Config 相同的部分。
func diffConfigAgainstImage(payload, imageCfg map[string]any) {
	// Env：去掉与旧镜像默认完全相同的 KEY=VALUE 项
	if env := toStringSlice(payload["Env"]); env != nil {
		imgEnv := make(map[string]bool)
		for _, e := range toStringSlice(imageCfg["Env"]) {
			imgEnv[e] = true
		}
		var keep []string
		for _, e := range env {
			if !imgEnv[e] {
				keep = append(keep, e)
			}
		}
		if len(keep) == 0 {
			delete(payload, "Env")
		} else {
			payload["Env"] = toAnySlice(keep)
		}
	}

	// Entrypoint / Cmd：Docker 在 Entrypoint 被覆盖时不会再使用镜像的 Cmd，因此仅在 Entrypoint 也沿用镜像默认时才可省略 Cmd
	if sameJSONValue(payload["Entrypoint"], imageCfg["Entrypoint"]) {
		delete(payload, "Entrypoint")
		if sameJSONValue(payload["Cmd"], imageCfg["Cmd"]) {
			delete(payload, "Cmd")
		}
	}

	for _, k := range []string{"WorkingDir", "User", "Healthcheck", "StopSignal"} {
		if v, ok := payload[k]; ok && sameJSONValue(v, imageCfg[k]) {
			delete(payload, k)
		}
	}

	// Labels / ExposedPorts / Volumes：按键去掉与镜像默认相同的项
	for _, k := range []string{"Labels", "ExposedPorts", "Volumes"} {
		cur, ok := payload[k].(map[string]any)
		if !ok {
			if v, exists := payload[k]; exists && v == nil {
				delete(payload, k)
			}
			continue
		}
		img, _ := imageCfg[k].(map[string]any)
		keep := make(map[string]any, len(cur))
		for kk, vv := range cur {
			if iv, exists := img[kk]; exists && sameJSONValue(iv, vv) {
				continue
			}
			keep[kk] = vv
		}
		if len(keep) == 0 {
			delete(payload, k)
		} else {
			payload[k] = keep
		}
	}
}

// attachExistingVolumes 将旧容器上未在 HostConfig.Binds / HostConfig.Mounts 中声明的 volume 挂载（匿名卷）以同名卷追加到 Binds。
func attachExistingVolumes(hc map[string]any, mounts any) {
	binds := toStringSlice(hc["Binds"])
	extra := volumeBindsFromMounts(binds, hc["Mounts"], mounts)
	if len(extra) == 0 {
		return
	}
	hc["Binds"] = toAnySlice(append(append([]string(nil), binds...), extra...))
}

func volumeBindsFromMounts(binds []string, hcMounts any, mounts any) []string {
	declared := make(map[string]bool)
	for _, b := range binds {
		parts := strings.Split(b, ":")
		if len(parts) >= 2 {
			declared[parts[1]] = true
		} else if len(parts) == 1 {
			declared[parts[0]] = true
		}
	}
	if list, ok := hcMounts.([]any); ok {
		for _, m := range list {
			if mm, ok := m.(map[string]any); ok {
				if tgt, _ := mm["Target"].(string); tgt != "" {
					declared[tgt] = true
				}
			}
		}
	}
	list, _ := mounts.([]any)
	var extra []string
	for _, m := range list {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := mm["Type"].(string)
		name, _ := mm["Name"].(string)
		dest, _ := mm["Destination"].(string)
		if typ != "volume" || name == "" || dest == "" || declared[dest] {
			continue
		}
		spec := name + ":" + dest
		if rw, ok := mm["RW"].(bool); ok && !rw {
			spec += ":ro"
		}
		extra = append(extra, spec)
		declared[dest] = true
	}
	return extra
}

func toStringSlice(v any) []string {
	switch s := v.(type) {
	case []string:
		return s
	case []any:
		out := make([]string, 0, len(s))
		for _, x := range s {
			if str, ok := x.(string); ok {
				out = append(out, str)
			}
		}
		return out
	default:
		return nil
	}
}

func toAnySlice(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

// sameJSONValue 按 JSON 语义比较两个值（nil 与空数组/空对象/空字符串视为相同）。
func sameJSONValue(a, b any) bool {
	if isEmptyJSON(a) && isEmptyJSON(b) {
		return true
	}
	ab, err1 := json.Marshal(a)
	bb, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return reflect.DeepEqual(a, b)
	}
	var na, nb any
	_ = json.Unmarshal(ab, &na)
	_ = json.Unmarshal(bb, &nb)
	return reflect.DeepEqual(na, nb)
}

func isEmptyJSON(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case []any:
		return len(x) == 0
	case []string:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	default:
		return false
	}
}

func dockerHTTPErr(status int, body string) string {
	return i18n.EngineDetail(i18n.MsgDockerHTTPStatus, strconv.Itoa(status), body)
}

// recreateContainerViaEngine 通过 Docker Engine API 原位重建单个容器，保留原容器名称、端口、用户自定义环境变量、挂载卷（含匿名卷）、
// 网络、重启策略与 Labels，且若原容器处于停止状态则重建后保持停止（立项文档 §7.2 / 审计 H6）。
func (a *Adapter) recreateContainerViaEngine(
	ctx context.Context,
	execTr transport.ExecutableTransport,
	oldID string,
	rawInspect map[string]any,
	targetImageRef string,
	wasRunning bool,
) (newID string, err error) {
	rawName, _ := rawInspect["Name"].(string)
	origName := strings.TrimPrefix(rawName, "/")
	if origName == "" {
		return "", i18n.NewEngineError(i18n.MsgDockerNoContainerName)
	}
	if cfgMap, _ := rawInspect["Config"].(map[string]any); cfgMap == nil {
		return "", i18n.NewEngineError(i18n.MsgDockerNoContainerConfig)
	}

	// 读取旧镜像默认 Config，用于差分出用户自定义部分
	var imageCfg map[string]any
	if oldImage, _ := rawInspect["Image"].(string); oldImage != "" {
		var img struct {
			Config map[string]any `json:"Config"`
		}
		if err := a.getJSON(ctx, "/images/"+url.PathEscape(oldImage)+"/json", &img); err == nil {
			imageCfg = img.Config
			if imageCfg == nil {
				imageCfg = map[string]any{}
			}
		}
	}
	createPayload := buildCreatePayload(oldID, rawInspect, imageCfg, targetImageRef)

	// 提取原容器连接的全部网络（首个网络放入 NetworkingConfig.EndpointsConfig，其余在 create 后 connect）
	type extraNet struct {
		name string
		cfg  map[string]any
	}
	var extraNetworks []extraNet
	if netSettings, ok := rawInspect["NetworkSettings"].(map[string]any); ok {
		if nets, ok := netSettings["Networks"].(map[string]any); ok && len(nets) > 0 {
			firstPlaced := false
			for netName, netVal := range nets {
				epCfg, _ := netVal.(map[string]any)
				if epCfg == nil {
					continue
				}
				// 清理旧容器专属的运行时状态字段（EndpointID / Gateway / IPAddress / MacAddress），保留 Aliases 与 IPAMConfig
				cleanEP := make(map[string]any)
				if aliases, ok := epCfg["Aliases"]; ok {
					cleanEP["Aliases"] = aliases
				}
				if ipam, ok := epCfg["IPAMConfig"]; ok && ipam != nil {
					cleanEP["IPAMConfig"] = ipam
				}
				if !firstPlaced {
					createPayload["NetworkingConfig"] = map[string]any{
						"EndpointsConfig": map[string]any{
							netName: cleanEP,
						},
					}
					firstPlaced = true
				} else {
					extraNetworks = append(extraNetworks, extraNet{name: netName, cfg: cleanEP})
				}
			}
		}
	}

	bodyBytes, err := json.Marshal(createPayload)
	if err != nil {
		return "", err
	}

	// 失败回滚使用独立上下文，避免任务 ctx 已取消导致旧容器无法恢复（审计 L8）
	cleanupCtx := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.WithoutCancel(ctx), restoreTimeout)
	}
	startOld := func() {
		if !wasRunning {
			return
		}
		rctx, cancel := cleanupCtx()
		defer cancel()
		if r, e := execTr.RequestDocker(rctx, http.MethodPost,
			"/containers/"+url.PathEscape(oldID)+"/start", nil, nil); e == nil {
			_ = r.Body.Close()
		}
	}

	// 1. 若原容器正在运行，先优雅停止（204 已停止，304 原本已停止）
	if wasRunning {
		resp, err := execTr.RequestDocker(ctx, http.MethodPost, "/containers/"+url.PathEscape(oldID)+"/stop?t=10", nil, nil)
		if err != nil {
			startOld()
			return "", i18n.NewEngineError(i18n.MsgDockerStopFailed, err.Error())
		}
		if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotModified {
			msg := readBodyString(resp.Body)
			_ = resp.Body.Close()
			startOld()
			return "", i18n.NewEngineError(i18n.MsgDockerStopFailed, dockerHTTPErr(resp.StatusCode, msg))
		}
		_ = resp.Body.Close()
	}

	// 2. 将旧容器临时改名，以便新容器沿用原名；若新容器创建或启动失败可立即恢复旧容器
	backupName := origName + "-dkstore-bak-" + strconv.FormatInt(time.Now().Unix()%10000, 10)
	renResp, err := execTr.RequestDocker(ctx, http.MethodPost,
		"/containers/"+url.PathEscape(oldID)+"/rename?name="+url.QueryEscape(backupName), nil, nil)
	if err != nil {
		startOld()
		return "", i18n.NewEngineError(i18n.MsgDockerRenameFailed, err.Error())
	}
	if renResp.StatusCode >= 300 {
		msg := readBodyString(renResp.Body)
		_ = renResp.Body.Close()
		startOld()
		return "", i18n.NewEngineError(i18n.MsgDockerRenameFailed, dockerHTTPErr(renResp.StatusCode, msg))
	}
	_ = renResp.Body.Close()

	restoreOld := func() {
		rctx, cancel := cleanupCtx()
		defer cancel()
		if r, e := execTr.RequestDocker(rctx, http.MethodPost,
			"/containers/"+url.PathEscape(oldID)+"/rename?name="+url.QueryEscape(origName), nil, nil); e == nil {
			_ = r.Body.Close()
		}
		if wasRunning {
			if r, e := execTr.RequestDocker(rctx, http.MethodPost,
				"/containers/"+url.PathEscape(oldID)+"/start", nil, nil); e == nil {
				_ = r.Body.Close()
			}
		}
	}
	removeNew := func(id string) {
		if id == "" {
			return
		}
		rctx, cancel := cleanupCtx()
		defer cancel()
		// 仅删除新容器本身，不带 v=1，避免误删沿用的卷
		if r, e := execTr.RequestDocker(rctx, http.MethodDelete, "/containers/"+url.PathEscape(id)+"?force=1", nil, nil); e == nil {
			_ = r.Body.Close()
		}
	}

	// 3. 创建新容器
	crResp, err := execTr.RequestDocker(ctx, http.MethodPost,
		"/containers/create?name="+url.QueryEscape(origName),
		bytes.NewReader(bodyBytes),
		map[string]string{"Content-Type": "application/json"},
	)
	if err != nil {
		restoreOld()
		return "", i18n.NewEngineError(i18n.MsgDockerCreateFailed, err.Error())
	}
	if crResp.StatusCode < 200 || crResp.StatusCode >= 300 {
		msg := readBodyString(crResp.Body)
		_ = crResp.Body.Close()
		restoreOld()
		return "", i18n.NewEngineError(i18n.MsgDockerCreateFailed, dockerHTTPErr(crResp.StatusCode, msg))
	}
	var crRes struct {
		ID string `json:"Id"`
	}
	_ = json.NewDecoder(crResp.Body).Decode(&crRes)
	_ = crResp.Body.Close()
	newID = crRes.ID

	// 4. 连接附加网络
	for _, en := range extraNetworks {
		connBody, _ := json.Marshal(map[string]any{
			"Container":      newID,
			"EndpointConfig": en.cfg,
		})
		if r, e := execTr.RequestDocker(ctx, http.MethodPost,
			"/networks/"+url.PathEscape(en.name)+"/connect",
			bytes.NewReader(connBody),
			map[string]string{"Content-Type": "application/json"},
		); e == nil {
			_ = r.Body.Close()
		}
	}

	// 5. 若原容器原本处于运行状态，启动新容器；若原容器原本处于停止状态，保持停止（立项文档 §7.2）
	if wasRunning {
		stResp, err := execTr.RequestDocker(ctx, http.MethodPost, "/containers/"+url.PathEscape(newID)+"/start", nil, nil)
		if err != nil {
			removeNew(newID)
			restoreOld()
			return "", i18n.NewEngineError(i18n.MsgDockerStartFailed, err.Error())
		}
		if stResp.StatusCode >= 300 && stResp.StatusCode != http.StatusNotModified {
			msg := readBodyString(stResp.Body)
			_ = stResp.Body.Close()
			removeNew(newID)
			restoreOld()
			return "", i18n.NewEngineError(i18n.MsgDockerStartFailed, dockerHTTPErr(stResp.StatusCode, msg))
		}
		_ = stResp.Body.Close()
	}

	// 6. 新容器已就绪，移除旧备份容器（v=0：不删除任何卷，匿名卷已挂载到新容器）
	if delResp, err := execTr.RequestDocker(ctx, http.MethodDelete, "/containers/"+url.PathEscape(oldID)+"?v=0", nil, nil); err == nil {
		_ = delResp.Body.Close()
	}
	return newID, nil
}

func (a *Adapter) verifyUpdatedService(
	ctx context.Context,
	req platform.UpdateRequest,
	expectedNewImageID string,
	expectedNewRepoDigest string,
	wasRunning bool,
) (*platform.UpdateResult, error) {
	cid, raw, running, err := a.findTargetContainer(ctx, req)
	if err != nil {
		return nil, err
	}
	actualImageID, _ := raw["Image"].(string)
	if expectedNewImageID != "" && actualImageID != expectedNewImageID {
		return nil, i18n.NewEngineError(i18n.MsgDockerImageNotSwitched, shortID(expectedNewImageID), shortID(actualImageID))
	}
	if wasRunning && !running {
		return nil, i18n.NewEngineError(i18n.MsgDockerNotRunning)
	}

	stateStr := "stopped"
	healthStr := "none"
	if st, ok := raw["State"].(map[string]any); ok {
		if s, ok := st["Status"].(string); ok && s != "" {
			stateStr = s
		}
		if h, ok := st["Health"].(map[string]any); ok {
			if hs, ok := h["Status"].(string); ok && hs != "" {
				healthStr = hs
			}
		}
	}

	// 若容器配置了 Healthcheck 且处于 starting，最多等待 12 秒观察健康状态（立项文档 §7.2）；等待尊重 ctx 取消（审计 L8）
	if wasRunning && healthStr == "starting" {
		deadline := time.NewTimer(12 * time.Second)
		defer deadline.Stop()
		tick := time.NewTicker(1500 * time.Millisecond)
		defer tick.Stop()
	waitLoop:
		for {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-deadline.C:
				break waitLoop
			case <-tick.C:
			}
			_, latestRaw, _, err := a.findTargetContainer(ctx, req)
			if err != nil {
				break waitLoop
			}
			if st, ok := latestRaw["State"].(map[string]any); ok {
				if h, ok := st["Health"].(map[string]any); ok {
					if hs, ok := h["Status"].(string); ok {
						healthStr = hs
						if hs == "healthy" {
							break waitLoop
						}
						if hs == "unhealthy" {
							return nil, i18n.NewEngineError(i18n.MsgDockerUnhealthy)
						}
					}
				}
			}
		}
	}

	return &platform.UpdateResult{
		NewImageID:     actualImageID,
		NewRepoDigest:  expectedNewRepoDigest,
		NewContainerID: cid,
		ContainerState: stateStr,
		HealthStatus:   healthStr,
	}, nil
}
