package docker

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/kukushouhou/DKStore/internal/i18n"
	"github.com/kukushouhou/DKStore/internal/platform"
	"github.com/kukushouhou/DKStore/internal/transport"
)

// GetStandaloneConfig 读取非 YAML 独立容器当前的镜像、用户自定义环境变量、端口映射与挂载目录（附录 E.4）。
func (a *Adapter) GetStandaloneConfig(ctx context.Context, appName, serviceName string) (*platform.StandaloneConfig, error) {
	_, rawInspect, _, err := a.findTargetContainer(ctx, platform.UpdateRequest{
		AppName:     appName,
		ServiceName: serviceName,
		Source:      platform.SourceStandalone,
	})
	if err != nil {
		return nil, err
	}

	cfgMap, _ := rawInspect["Config"].(map[string]any)
	imageRef, _ := cfgMap["Image"].(string)
	if labels, ok := cfgMap["Labels"].(map[string]any); ok {
		if proj, _ := labels["com.docker.compose.project"].(string); strings.TrimSpace(proj) != "" {
			return &platform.StandaloneConfig{
				Editable: false,
				ImageRef: imageRef,
				Env:      []platform.EnvVar{},
				Ports:    []platform.PortBinding{},
				Volumes:  []platform.VolumeBinding{},
			}, nil
		}
	}

	// 读取旧镜像默认 Env，仅返回用户自定义的环境变量（避免镜像内置 PATH/VERSION 污染表单）
	imgEnvSet := make(map[string]bool)
	if oldImage, _ := rawInspect["Image"].(string); oldImage != "" {
		var img struct {
			Config map[string]any `json:"Config"`
		}
		if a.getJSON(ctx, "/images/"+url.PathEscape(oldImage)+"/json", &img) == nil && img.Config != nil {
			for _, e := range toStringSlice(img.Config["Env"]) {
				imgEnvSet[e] = true
			}
		}
	}

	envList := []platform.EnvVar{}
	for _, rawEnv := range toStringSlice(cfgMap["Env"]) {
		if imgEnvSet[rawEnv] {
			continue
		}
		k, v, ok := strings.Cut(rawEnv, "=")
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if !ok {
			v = ""
		}
		envList = append(envList, platform.EnvVar{Key: k, Value: v})
	}

	hostCfg, _ := rawInspect["HostConfig"].(map[string]any)
	ports := extractDockerPorts(hostCfg)
	volumes := extractDockerVolumes(rawInspect["Mounts"], hostCfg)

	return &platform.StandaloneConfig{
		Editable: true,
		ImageRef: imageRef,
		Env:      envList,
		Ports:    ports,
		Volumes:  volumes,
	}, nil
}

// UpdateStandaloneConfig 拉取目标镜像并更新独立容器的环境变量与端口映射，失败时自动恢复原备份容器（附录 E.4）。
func (a *Adapter) UpdateStandaloneConfig(
	ctx context.Context,
	req platform.StandaloneUpdateRequest,
	onProgress func(platform.PullProgress),
) (*platform.UpdateResult, error) {
	execTr, ok := a.tr.(transport.ExecutableTransport)
	if !ok {
		return nil, &platform.UpdateError{
			Code:    "capability_unsupported",
			Message: i18n.EngineDetail(i18n.MsgDockerNoExec),
		}
	}
	emit := func(p platform.PullProgress) {
		if onProgress != nil {
			onProgress(p)
		}
	}

	var logs []string
	addLog := func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...)))
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

	emit(platform.PullProgress{
		Phase:      "precheck",
		StatusText: "Checking standalone container & configuration",
		Percent:    5,
		LogChunk:   "Checking standalone container & configuration...\r\n",
	})
	targetContainerID, rawInspect, wasRunning, err := a.findTargetContainer(ctx, upReq)
	if err != nil {
		return nil, &platform.UpdateError{Code: "precheck_failed", Message: i18n.DetailOf(err)}
	}
	curImageID, _ := rawInspect["Image"].(string)
	if req.ExpectedImageID != "" && curImageID != "" &&
		!strings.HasPrefix(curImageID, req.ExpectedImageID) && !strings.HasPrefix(req.ExpectedImageID, curImageID) {
		return nil, &platform.UpdateError{
			Code:    "external_conflict",
			Message: i18n.EngineDetail(i18n.MsgDockerImageChanged, shortID(req.ExpectedImageID), shortID(curImageID)),
		}
	}
	addLog("precheck ok: container=%s image=%s wasRunning=%v", shortID(targetContainerID), shortID(curImageID), wasRunning)

	emit(platform.PullProgress{
		Phase:      "pulling",
		StatusText: "Pulling image " + req.TargetImageRef,
		Percent:    10,
		LogChunk:   fmt.Sprintf("Pulling image %s ...\r\n", req.TargetImageRef),
	})
	newImageID, newRepoDigest, err := a.pullImageStream(ctx, execTr, req.TargetImageRef, req.RegistryAuthB64, emit)
	if err != nil {
		return nil, &platform.UpdateError{Code: "pull_failed", Message: i18n.DetailOf(err)}
	}
	addLog("image pulled: id=%s digest=%s", shortID(newImageID), newRepoDigest)

	emit(platform.PullProgress{
		Phase:      "recreating",
		StatusText: "Recreating standalone container with updated config",
		Percent:    85,
		LogChunk:   "Applying updated environment & port bindings and recreating container...\r\n",
	})
	envCopy := append([]platform.EnvVar(nil), req.Env...)
	portsCopy := append([]platform.PortBinding(nil), req.Ports...)
	newCID, err := a.recreateContainerViaEngineWithOverrides(ctx, execTr, targetContainerID, rawInspect, req.TargetImageRef, wasRunning, &envCopy, &portsCopy)
	if err != nil {
		return nil, &platform.UpdateError{Code: "recreate_failed", Message: i18n.DetailOf(err)}
	}
	addLog("recreated standalone container via Engine API: %s", shortID(newCID))

	emit(platform.PullProgress{
		Phase:      "verifying",
		StatusText: "Verifying container state & switched image",
		Percent:    95,
		LogChunk:   "Verifying container state & switched image...\r\n",
	})
	res, err := a.verifyUpdatedService(ctx, upReq, newImageID, newRepoDigest, wasRunning)
	if err != nil {
		return nil, &platform.UpdateError{Code: "verify_failed", Message: i18n.DetailOf(err)}
	}
	res.Logs = logs
	emit(platform.PullProgress{
		Phase:      "done",
		StatusText: "Update completed",
		Percent:    100,
	})
	return res, nil
}

func applyPayloadOverrides(payload map[string]any, overrideEnv *[]platform.EnvVar, overridePorts *[]platform.PortBinding) {
	if overrideEnv != nil {
		envStrs := make([]string, 0, len(*overrideEnv))
		for _, e := range *overrideEnv {
			k := strings.TrimSpace(e.Key)
			if k == "" {
				continue
			}
			envStrs = append(envStrs, k+"="+e.Value)
		}
		if len(envStrs) == 0 {
			delete(payload, "Env")
		} else {
			payload["Env"] = toAnySlice(envStrs)
		}
	}

	if overridePorts != nil {
		hc, _ := payload["HostConfig"].(map[string]any)
		if hc == nil {
			hc = make(map[string]any)
			payload["HostConfig"] = hc
		}
		portBindings := make(map[string]any)
		exposed := make(map[string]any)
		for _, p := range *overridePorts {
			cPort := strings.TrimSpace(p.ContainerPort)
			if cPort == "" {
				continue
			}
			proto := strings.ToLower(strings.TrimSpace(p.Protocol))
			if proto == "" {
				proto = "tcp"
			}
			key := cPort + "/" + proto
			exposed[key] = map[string]any{}
			curList, _ := portBindings[key].([]map[string]string)
			curList = append(curList, map[string]string{
				"HostIp":   strings.TrimSpace(p.HostIP),
				"HostPort": strings.TrimSpace(p.HostPort),
			})
			portBindings[key] = curList
		}
		hc["PortBindings"] = portBindings
		if len(exposed) == 0 {
			delete(payload, "ExposedPorts")
		} else {
			payload["ExposedPorts"] = exposed
		}
	}
}

func extractDockerPorts(hostCfg map[string]any) []platform.PortBinding {
	out := []platform.PortBinding{}
	if hostCfg == nil {
		return out
	}
	pbMap, ok := hostCfg["PortBindings"].(map[string]any)
	if !ok || len(pbMap) == 0 {
		return out
	}
	keys := make([]string, 0, len(pbMap))
	for k := range pbMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		cPort, proto, hasProto := strings.Cut(k, "/")
		if !hasProto || proto == "" {
			proto = "tcp"
		}
		list, _ := pbMap[k].([]any)
		for _, item := range list {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			hostIP, _ := m["HostIp"].(string)
			hostPort, _ := m["HostPort"].(string)
			out = append(out, platform.PortBinding{
				HostIP:        strings.TrimSpace(hostIP),
				HostPort:      strings.TrimSpace(hostPort),
				ContainerPort: strings.TrimSpace(cPort),
				Protocol:      strings.ToLower(strings.TrimSpace(proto)),
			})
		}
	}
	return out
}

func extractDockerVolumes(mounts any, hostCfg map[string]any) []platform.VolumeBinding {
	out := []platform.VolumeBinding{}
	if list, ok := mounts.([]any); ok && len(list) > 0 {
		for _, m := range list {
			mm, ok := m.(map[string]any)
			if !ok {
				continue
			}
			src, _ := mm["Source"].(string)
			if src == "" {
				src, _ = mm["Name"].(string)
			}
			dst, _ := mm["Destination"].(string)
			mode := "rw"
			if rw, ok := mm["RW"].(bool); ok && !rw {
				mode = "ro"
			}
			if src == "" && dst == "" {
				continue
			}
			out = append(out, platform.VolumeBinding{
				HostPath:      src,
				ContainerPath: dst,
				Mode:          mode,
			})
		}
		return out
	}
	if hostCfg != nil {
		for _, b := range toStringSlice(hostCfg["Binds"]) {
			parts := strings.Split(b, ":")
			if len(parts) < 2 {
				continue
			}
			mode := "rw"
			if len(parts) >= 3 && strings.Contains(parts[2], "ro") {
				mode = "ro"
			}
			out = append(out, platform.VolumeBinding{
				HostPath:      parts[0],
				ContainerPath: parts[1],
				Mode:          mode,
			})
		}
	}
	return out
}
