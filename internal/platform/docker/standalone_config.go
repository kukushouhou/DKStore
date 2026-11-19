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

type standaloneOverrides struct {
	env           *[]platform.EnvVar
	ports         *[]platform.PortBinding
	volumes       *[]platform.VolumeBinding
	devices       *[]platform.DeviceBinding
	networkMode   *string
	restartPolicy *string
	user          *string
	privileged    *bool
	capAdd        *[]string
	hostname      *string
	extraHosts    *[]string
}

// GetStandaloneConfig 读取非 YAML 独立容器当前的镜像、网络、权限、环境变量、端口映射、挂载目录与硬件设备（附录 E.4）。
func (a *Adapter) GetStandaloneConfig(ctx context.Context, appName, serviceName string) (*platform.StandaloneConfig, error) {
	cid, rawInspect, _, err := a.findTargetContainer(ctx, platform.UpdateRequest{
		AppName:     appName,
		ServiceName: serviceName,
		Source:      platform.SourceStandalone,
	})
	if err != nil {
		return nil, err
	}

	rawName, _ := rawInspect["Name"].(string)
	cName := strings.TrimPrefix(strings.TrimSpace(rawName), "/")
	if cName == "" {
		cName = appName
	}

	cfgMap, _ := rawInspect["Config"].(map[string]any)
	imageRef, _ := cfgMap["Image"].(string)
	if labels, ok := cfgMap["Labels"].(map[string]any); ok {
		if proj, _ := labels["com.docker.compose.project"].(string); strings.TrimSpace(proj) != "" {
			return &platform.StandaloneConfig{
				Editable:      false,
				ContainerName: cName,
				ImageRef:      imageRef,
				Env:           []platform.EnvVar{},
				Ports:         []platform.PortBinding{},
				Volumes:       []platform.VolumeBinding{},
				Devices:       []platform.DeviceBinding{},
			}, nil
		}
	}

	// 读取旧镜像默认 Env 与 User，仅返回用户自定义的部分（避免镜像内置 PATH/默认用户污染表单）
	imgEnvSet := make(map[string]bool)
	imgDefaultUser := ""
	if oldImage, _ := rawInspect["Image"].(string); oldImage != "" {
		var img struct {
			Config map[string]any `json:"Config"`
		}
		if a.getJSON(ctx, "/images/"+url.PathEscape(oldImage)+"/json", &img) == nil && img.Config != nil {
			for _, e := range toStringSlice(img.Config["Env"]) {
				imgEnvSet[e] = true
			}
			imgDefaultUser, _ = img.Config["User"].(string)
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

	customUser, _ := cfgMap["User"].(string)
	customUser = strings.TrimSpace(customUser)
	if customUser == strings.TrimSpace(imgDefaultUser) {
		customUser = ""
	}

	customHostname, _ := cfgMap["Hostname"].(string)
	customHostname = strings.TrimSpace(customHostname)
	domainName, _ := cfgMap["Domainname"].(string)
	if customHostname != "" {
		if (cid != "" && strings.HasPrefix(strings.TrimPrefix(cid, "sha256:"), customHostname)) ||
			(strings.TrimSpace(domainName) == "" && isAutoDockerHexHostname(customHostname)) {
			customHostname = ""
		}
	}

	hostCfg, _ := rawInspect["HostConfig"].(map[string]any)
	netMode := extractDockerNetworkMode(hostCfg, rawInspect["NetworkSettings"])
	restartPolicy := extractDockerRestartPolicy(hostCfg)
	privileged := false
	if hostCfg != nil {
		privileged, _ = hostCfg["Privileged"].(bool)
	}
	var capAdd, extraHosts []string
	if hostCfg != nil {
		capAdd = filterNonDefaultCapAdd(toStringSlice(hostCfg["CapAdd"]))
		extraHosts = toStringSlice(hostCfg["ExtraHosts"])
	}
	ports := extractDockerPorts(hostCfg)
	volumes := extractDockerVolumes(rawInspect["Mounts"], hostCfg)
	devices := extractDockerDevices(hostCfg)

	return &platform.StandaloneConfig{
		Editable:      true,
		ContainerName: cName,
		ImageRef:      imageRef,
		NetworkMode:   netMode,
		RestartPolicy: restartPolicy,
		User:          customUser,
		Privileged:    privileged,
		CapAdd:        capAdd,
		Hostname:      customHostname,
		ExtraHosts:    extraHosts,
		Env:           envList,
		Ports:         ports,
		Volumes:       volumes,
		Devices:       devices,
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
		Percent:    0,
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
	wantRunning := wasRunning
	if req.StartAfter != nil {
		wantRunning = *req.StartAfter
	}
	addLog("precheck ok: container=%s image=%s wasRunning=%v wantRunning=%v", shortID(targetContainerID), shortID(curImageID), wasRunning, wantRunning)

	emit(platform.PullProgress{
		Phase:      "pulling",
		StatusText: "Pulling image " + req.TargetImageRef,
		Percent:    0,
		LogChunk:   fmt.Sprintf("Pulling image %s ...\r\n", req.TargetImageRef),
	})
	newImageID, newRepoDigest, err := a.pullImageWithMirrorPairs(ctx, execTr, req.TargetImageRef, req.RegistryAuthB64, req.LayerSizes, req.MirrorPairs, emit)
	if err != nil {
		return nil, &platform.UpdateError{Code: "pull_failed", Message: i18n.DetailOf(err)}
	}
	addLog("image pulled: id=%s digest=%s", shortID(newImageID), newRepoDigest)

	emit(platform.PullProgress{
		Phase:      "recreating",
		StatusText: "Recreating standalone container with updated config",
		Percent:    90,
		LogChunk:   "Applying updated environment & port bindings and recreating container...\r\n",
	})
	envCopy := append([]platform.EnvVar(nil), req.Env...)
	portsCopy := append([]platform.PortBinding(nil), req.Ports...)
	ov := &standaloneOverrides{
		env:           &envCopy,
		ports:         &portsCopy,
		volumes:       req.Volumes,
		devices:       req.Devices,
		networkMode:   req.NetworkMode,
		restartPolicy: req.RestartPolicy,
		user:          req.User,
		privileged:    req.Privileged,
		capAdd:        req.CapAdd,
		hostname:      req.Hostname,
		extraHosts:    req.ExtraHosts,
	}
	newCID, err := a.recreateContainerViaEngineWithFullOverrides(ctx, execTr, targetContainerID, rawInspect, req.TargetImageRef, wasRunning, wantRunning, ov)
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
	res, err := a.verifyUpdatedService(ctx, upReq, newImageID, newRepoDigest, wantRunning)
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
	applyStandaloneOverrides(payload, nil, &standaloneOverrides{
		env:   overrideEnv,
		ports: overridePorts,
	})
}

func applyStandaloneOverrides(payload map[string]any, origMounts any, ov *standaloneOverrides) {
	if ov == nil {
		return
	}
	ensureHC := func() map[string]any {
		hc, _ := payload["HostConfig"].(map[string]any)
		if hc == nil {
			hc = make(map[string]any)
			payload["HostConfig"] = hc
		}
		return hc
	}

	if ov.env != nil {
		envStrs := make([]string, 0, len(*ov.env))
		for _, e := range *ov.env {
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

	if ov.ports != nil {
		hc := ensureHC()
		portBindings := make(map[string]any)
		exposed := make(map[string]any)
		for _, p := range *ov.ports {
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

	if ov.volumes != nil {
		hc := ensureHC()
		binds := make([]string, 0, len(*ov.volumes))
		for _, v := range *ov.volumes {
			hp := strings.TrimSpace(v.HostPath)
			cp := strings.TrimSpace(v.ContainerPath)
			if cp == "" {
				continue
			}
			spec := cp
			if hp != "" {
				spec = hp + ":" + cp
				if strings.EqualFold(strings.TrimSpace(v.Mode), "ro") {
					spec += ":ro"
				}
			}
			binds = append(binds, spec)
		}
		delete(hc, "Mounts")
		hc["Binds"] = toAnySlice(binds)
		attachExistingVolumes(hc, origMounts)
	}

	if ov.devices != nil {
		hc := ensureHC()
		devs := make([]any, 0, len(*ov.devices))
		for _, d := range *ov.devices {
			hp := strings.TrimSpace(d.HostPath)
			cp := strings.TrimSpace(d.ContainerPath)
			if hp == "" && cp == "" {
				continue
			}
			if cp == "" {
				cp = hp
			}
			if hp == "" {
				hp = cp
			}
			perm := strings.TrimSpace(d.Permissions)
			if perm == "" {
				perm = "rwm"
			}
			devs = append(devs, map[string]any{
				"PathOnHost":        hp,
				"PathInContainer":   cp,
				"CgroupPermissions": perm,
			})
		}
		hc["Devices"] = devs
	}

	if ov.networkMode != nil {
		hc := ensureHC()
		nm := strings.TrimSpace(*ov.networkMode)
		if nm == "" || nm == "default" {
			nm = "bridge"
		}
		hc["NetworkMode"] = nm
	}

	if ov.restartPolicy != nil {
		hc := ensureHC()
		rp := strings.TrimSpace(*ov.restartPolicy)
		if rp == "" {
			rp = "no"
		}
		hc["RestartPolicy"] = map[string]any{"Name": rp}
	}

	if ov.user != nil {
		u := strings.TrimSpace(*ov.user)
		if u == "" {
			delete(payload, "User")
		} else {
			payload["User"] = u
		}
	}

	if ov.privileged != nil {
		hc := ensureHC()
		hc["Privileged"] = *ov.privileged
	}

	if ov.capAdd != nil {
		hc := ensureHC()
		var caps []string
		for _, c := range *ov.capAdd {
			if s := strings.TrimSpace(c); s != "" {
				caps = append(caps, s)
			}
		}
		if len(caps) == 0 {
			delete(hc, "CapAdd")
		} else {
			hc["CapAdd"] = toAnySlice(caps)
		}
	}

	if ov.hostname != nil {
		hn := strings.TrimSpace(*ov.hostname)
		if hn == "" {
			delete(payload, "Hostname")
		} else {
			payload["Hostname"] = hn
		}
	}

	if ov.extraHosts != nil {
		hc := ensureHC()
		var hosts []string
		for _, h := range *ov.extraHosts {
			if s := strings.TrimSpace(h); s != "" {
				hosts = append(hosts, s)
			}
		}
		if len(hosts) == 0 {
			delete(hc, "ExtraHosts")
		} else {
			hc["ExtraHosts"] = toAnySlice(hosts)
		}
	}
}

func extractDockerNetworkMode(hostCfg map[string]any, netSettings any) string {
	nm := ""
	if hostCfg != nil {
		nm, _ = hostCfg["NetworkMode"].(string)
		nm = strings.TrimSpace(nm)
	}
	if nm == "" || nm == "default" {
		if nsMap, ok := netSettings.(map[string]any); ok {
			if nets, ok := nsMap["Networks"].(map[string]any); ok && len(nets) == 1 {
				for k := range nets {
					if k != "" && k != "bridge" {
						return k
					}
				}
			}
		}
		return "bridge"
	}
	return nm
}

func extractDockerRestartPolicy(hostCfg map[string]any) string {
	if hostCfg == nil {
		return ""
	}
	rp, _ := hostCfg["RestartPolicy"].(map[string]any)
	if rp == nil {
		return ""
	}
	name, _ := rp["Name"].(string)
	name = strings.TrimSpace(name)
	if name == "no" {
		return ""
	}
	return name
}

func extractDockerDevices(hostCfg map[string]any) []platform.DeviceBinding {
	out := []platform.DeviceBinding{}
	if hostCfg == nil {
		return out
	}
	list, _ := hostCfg["Devices"].([]any)
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		hp, _ := m["PathOnHost"].(string)
		cp, _ := m["PathInContainer"].(string)
		perm, _ := m["CgroupPermissions"].(string)
		hp = strings.TrimSpace(hp)
		cp = strings.TrimSpace(cp)
		perm = strings.TrimSpace(perm)
		if hp == "" && cp == "" {
			continue
		}
		if cp == "" {
			cp = hp
		}
		out = append(out, platform.DeviceBinding{
			HostPath:      hp,
			ContainerPath: cp,
			Permissions:   perm,
		})
	}
	return out
}

func isAnonymousHexVolumeName(name string) bool {
	if len(name) != 64 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
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
	declaredTargets := make(map[string]bool)
	if hostCfg != nil {
		for _, b := range toStringSlice(hostCfg["Binds"]) {
			parts := strings.Split(b, ":")
			if len(parts) >= 2 {
				declaredTargets[strings.TrimSpace(parts[1])] = true
			}
		}
	}
	if list, ok := mounts.([]any); ok && len(list) > 0 {
		for _, m := range list {
			mm, ok := m.(map[string]any)
			if !ok {
				continue
			}
			typ, _ := mm["Type"].(string)
			volName, _ := mm["Name"].(string)
			src, _ := mm["Source"].(string)
			dst, _ := mm["Destination"].(string)
			dst = strings.TrimSpace(dst)
			if strings.EqualFold(typ, "volume") {
				if isAnonymousHexVolumeName(volName) && !declaredTargets[dst] {
					// 镜像自身声明的匿名卷不在 docker run 中展示（重建时由 attachExistingVolumes 自动保留）
					continue
				}
				if volName != "" && !isAnonymousHexVolumeName(volName) {
					src = volName
				}
			} else if src == "" {
				src = volName
			}
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

func isAutoDockerHexHostname(h string) bool {
	if len(h) != 12 {
		return false
	}
	for i := 0; i < 12; i++ {
		c := h[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

var defaultDockerCaps = map[string]bool{
	"AUDIT_WRITE":      true,
	"CHOWN":            true,
	"DAC_OVERRIDE":     true,
	"FOWNER":           true,
	"FSETID":           true,
	"KILL":             true,
	"MKNOD":            true,
	"NET_BIND_SERVICE": true,
	"NET_RAW":          true,
	"SETFCAP":          true,
	"SETGID":           true,
	"SETPCAP":          true,
	"SETUID":           true,
	"SYS_CHROOT":       true,
}

func filterNonDefaultCapAdd(caps []string) []string {
	var out []string
	for _, c := range caps {
		clean := strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(c)), "CAP_")
		if clean == "" || defaultDockerCaps[clean] {
			continue
		}
		out = append(out, clean)
	}
	return out
}
