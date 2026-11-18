package docker

import (
	"context"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/kukushouhou/DKStore/internal/i18n"
	"github.com/kukushouhou/DKStore/internal/platform"
	"github.com/kukushouhou/DKStore/internal/transport"
	"gopkg.in/yaml.v3"
)

func normalizeComposePosixPath(raw string) string {
	s := strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	if s == "" {
		return ""
	}
	if !strings.HasPrefix(s, "/") {
		s = "/" + s
	}
	clean := path.Clean(s)
	if clean == "." {
		return "/"
	}
	return clean
}

func posixDirOf(filePath string) string {
	clean := normalizeComposePosixPath(filePath)
	if clean == "" || clean == "/" {
		return "/"
	}
	d := path.Dir(clean)
	if d == "" || d == "." {
		return "/"
	}
	return d
}

// resolveExistingOrDefaultComposeFile 返回项目的主配置文件路径；当 ConfigFiles 为空但 WorkingDir 存在其他常见命名文件时优先返回实际存在的文件。
func (a *Adapter) resolveExistingOrDefaultComposeFile(ctx context.Context, project *platform.ProjectInfo) string {
	if project == nil {
		return ""
	}
	if len(project.ConfigFiles) > 0 && strings.TrimSpace(project.ConfigFiles[0]) != "" {
		return resolvePath(project.WorkingDir, strings.TrimSpace(project.ConfigFiles[0]))
	}
	if strings.TrimSpace(project.WorkingDir) == "" {
		return ""
	}
	candidates := []string{"compose.yaml", "docker-compose.yml", "docker-compose.yaml", "compose.yml"}
	if fsTr, ok := a.tr.(transport.HostFSTransport); ok {
		for _, name := range candidates {
			cand := resolvePath(project.WorkingDir, name)
			if st, err := fsTr.ProbeFileAccess(ctx, cand); err == nil && st.Exists {
				return cand
			}
		}
	} else {
		for _, name := range candidates {
			cand := resolvePath(project.WorkingDir, name)
			if _, err := a.tr.ReadFile(ctx, cand); err == nil {
				return cand
			}
		}
	}
	return resolvePath(project.WorkingDir, "compose.yaml")
}

// InspectProjectComposeEdit 预检项目配置文件的存在性与读写权限；若原文件缺失或不可读，自动从当前容器运行参数反推生成等效 compose.yaml。
func (a *Adapter) InspectProjectComposeEdit(ctx context.Context, appName string, project *platform.ProjectInfo) (*platform.ProjectComposeInspection, error) {
	projName := strings.TrimSpace(appName)
	if project != nil && strings.TrimSpace(project.Name) != "" {
		projName = strings.TrimSpace(project.Name)
	}
	composePath := a.resolveExistingOrDefaultComposeFile(ctx, project)

	var access transport.FileAccessState
	if composePath != "" {
		if fsTr, ok := a.tr.(transport.HostFSTransport); ok {
			st, err := fsTr.ProbeFileAccess(ctx, composePath)
			if err == nil {
				access = st
			}
		} else {
			if _, err := a.tr.ReadFile(ctx, composePath); err == nil {
				access = transport.FileAccessState{Exists: true, Readable: true, Writable: true}
			}
		}
	}

	extraSvcs := a.readExtraComposeServiceNames(ctx, project)
	liveSvcs := a.listLiveComposeServiceNames(ctx, projName)

	// 1. 原文件存在且可读
	if composePath != "" && access.Exists && access.Readable {
		b, err := a.tr.ReadFile(ctx, composePath)
		if err == nil {
			primarySvcs, _ := ParseComposeServiceNames(string(b))
			var suggested []platform.SuggestedComposeDir
			if !access.Writable {
				suggested = a.collectSuggestedComposeDirs(ctx, projName)
			}
			return &platform.ProjectComposeInspection{
				ComposePath:       composePath,
				ProjectName:       projName,
				Content:           string(b),
				PathMissing:       false,
				PathReadOnly:      !access.Writable,
				OrigWritable:      access.Writable,
				SuggestedDirs:     suggested,
				EffectiveServices: unionOrderedStrings(primarySvcs, extraSvcs, liveSvcs),
				ExtraFileServices: extraSvcs,
			}, nil
		}
	}

	// 2. 原文件缺失或无权读取：从运行中的项目容器反推生成等效 compose.yaml
	reconstructed, suggested, reconSvcs, err := a.reconstructProjectCompose(ctx, projName)
	if err != nil {
		return nil, err
	}
	pathMissing := composePath == "" || !access.Exists
	origWritable := composePath != "" && access.Writable
	pathReadOnly := !pathMissing && !origWritable

	return &platform.ProjectComposeInspection{
		ComposePath:       composePath,
		ProjectName:       projName,
		Content:           reconstructed,
		PathMissing:       pathMissing,
		PathReadOnly:      pathReadOnly,
		OrigWritable:      origWritable,
		SuggestedDirs:     suggested,
		EffectiveServices: unionOrderedStrings(reconSvcs, extraSvcs, liveSvcs),
		ExtraFileServices: extraSvcs,
	}, nil
}

// ListComposeHostDirs 列出宿主机指定路径下的子文件夹列表。
func (a *Adapter) ListComposeHostDirs(ctx context.Context, dirPath string) (*platform.ComposeHostDirList, error) {
	fsTr, ok := a.tr.(transport.HostFSTransport)
	if !ok {
		return nil, i18n.NewEngineError(i18n.MsgComposeNotEditable)
	}
	res, err := fsTr.ListDirectories(ctx, dirPath)
	if err != nil {
		return nil, err
	}
	return &platform.ComposeHostDirList{
		Path:     res.Path,
		Dirs:     res.Dirs,
		Writable: res.Writable,
		HomeDir:  res.HomeDir,
	}, nil
}

// CreateComposeHostDir 在宿主机指定父目录下安全创建新文件夹并返回完整路径。
func (a *Adapter) CreateComposeHostDir(ctx context.Context, parentDir, name string) (string, error) {
	fsTr, ok := a.tr.(transport.HostFSTransport)
	if !ok {
		return "", i18n.NewEngineError(i18n.MsgComposeNotEditable)
	}
	trimmedName := strings.TrimSpace(name)
	if !transport.IsSafeDirSegment(trimmedName) {
		return "", i18n.NewEngineError(i18n.MsgTransportInvalidPath)
	}
	base := normalizeComposePosixPath(parentDir)
	if base == "" {
		base = "/"
	}
	full := path.Join(base, trimmedName)
	if err := fsTr.CreateDirectory(ctx, full); err != nil {
		return "", err
	}
	return full, nil
}

// CheckComposeSaveDir 核验用户在文件夹选择器中选定的保存目录是否可写，以及是否存在同名 compose.yaml 和容器占用冲突。
func (a *Adapter) CheckComposeSaveDir(ctx context.Context, appName string, project *platform.ProjectInfo, dirPath, subFolder string) (*platform.ComposeDirCheckResult, error) {
	fsTr, ok := a.tr.(transport.HostFSTransport)
	if !ok {
		return nil, i18n.NewEngineError(i18n.MsgComposeNotEditable)
	}
	baseDir := normalizeComposePosixPath(dirPath)
	if baseDir == "" {
		baseDir = "/"
	}
	sub := strings.TrimSpace(subFolder)
	targetDir := baseDir
	if sub != "" {
		if !transport.IsSafeDirSegment(sub) {
			return nil, i18n.NewEngineError(i18n.MsgTransportInvalidPath)
		}
		if path.Base(baseDir) != sub {
			targetDir = path.Join(baseDir, sub)
		}
	}
	targetFile := path.Join(targetDir, "compose.yaml")

	access, err := fsTr.ProbeFileAccess(ctx, targetFile)
	if err != nil || !access.Writable {
		return &platform.ComposeDirCheckResult{
			TargetFile: targetFile,
			Writable:   false,
			ExistKind:  "none",
		}, nil
	}

	curProj := strings.TrimSpace(appName)
	if project != nil && strings.TrimSpace(project.Name) != "" {
		curProj = strings.TrimSpace(project.Name)
	}

	// 读取已有 compose.yaml 内容（若存在且可读）用于识别归属
	var existingYAML string
	var fileDeclProj string
	fileContainerNames := make(map[string]bool)
	fileServiceNames := make(map[string]bool)
	fileImageRepos := make(map[string]bool)
	if access.Exists && access.Readable {
		if b, rErr := a.tr.ReadFile(ctx, targetFile); rErr == nil {
			existingYAML = string(b)
			var doc struct {
				Name     string                    `yaml:"name"`
				Services map[string]map[string]any `yaml:"services"`
			}
			if yaml.Unmarshal(b, &doc) == nil {
				fileDeclProj = strings.TrimSpace(doc.Name)
				for sName, sMap := range doc.Services {
					fileServiceNames[strings.TrimSpace(sName)] = true
					if cn, ok := sMap["container_name"].(string); ok && strings.TrimSpace(cn) != "" {
						fileContainerNames[strings.TrimPrefix(strings.TrimSpace(cn), "/")] = true
					}
					if img, ok := sMap["image"].(string); ok && strings.TrimSpace(img) != "" {
						repo, _ := splitPullImageAndTag(img)
						fileImageRepos[normalizeRepoName(repo)] = true
					}
				}
			}
		}
	}

	var list []containerSummary
	_ = a.getJSON(ctx, "/containers/json?all=1", &list)

	var runningOwners []string
	var stoppedOwners []string
	seenRunning := make(map[string]bool)
	seenStopped := make(map[string]bool)
	curProjServices := make(map[string]bool)
	curProjRepos := make(map[string]bool)
	sameByWorkDir := false

	for _, cs := range list {
		if cs.Labels["dkstore.self"] == "true" {
			continue
		}
		cProj := strings.TrimSpace(cs.Labels["com.docker.compose.project"])
		cSvc := strings.TrimSpace(cs.Labels["com.docker.compose.service"])
		cWorkRaw := strings.TrimSpace(cs.Labels["com.docker.compose.project.working_dir"])
		cWork := ""
		if cWorkRaw != "" {
			cWork = normalizeComposePosixPath(cWorkRaw)
		}
		var dispName string
		for _, n := range cs.Names {
			clean := strings.TrimPrefix(strings.TrimSpace(n), "/")
			if clean != "" {
				dispName = clean
				break
			}
		}
		if dispName == "" {
			if cProj != "" {
				dispName = cProj
			} else {
				dispName = shortID(cs.ID)
			}
		}

		if cProj != "" && cProj == curProj {
			if cSvc != "" {
				curProjServices[cSvc] = true
			}
			if cs.Image != "" {
				repo, _ := splitPullImageAndTag(cs.Image)
				curProjRepos[normalizeRepoName(repo)] = true
			}
			if cWork == targetDir {
				sameByWorkDir = true
			}
			continue
		}

		matchedOther := false
		if cWork != "" && cWork == targetDir {
			matchedOther = true
		}
		if !matchedOther {
			for _, cf := range splitFiles(cs.Labels["com.docker.compose.project.config_files"]) {
				full := resolvePath(cWorkRaw, cf)
				if posixDirOf(full) == targetDir {
					matchedOther = true
					break
				}
			}
		}
		if !matchedOther && access.Exists {
			if fileDeclProj != "" && cProj == fileDeclProj {
				matchedOther = true
			} else if dispName != "" && fileContainerNames[dispName] {
				matchedOther = true
			}
		}
		if !matchedOther {
			continue
		}

		ownerLabel := dispName
		if cProj != "" && cProj != dispName {
			ownerLabel = cProj + " / " + dispName
		}
		stLower := strings.ToLower(strings.TrimSpace(cs.State))
		if stLower == "running" || stLower == "restarting" {
			if !seenRunning[ownerLabel] {
				seenRunning[ownerLabel] = true
				runningOwners = append(runningOwners, ownerLabel)
			}
		} else {
			if !seenStopped[ownerLabel] {
				seenStopped[ownerLabel] = true
				stoppedOwners = append(stoppedOwners, ownerLabel)
			}
		}
	}

	if len(runningOwners) > 0 {
		return &platform.ComposeDirCheckResult{
			TargetFile: targetFile,
			Writable:   true,
			ExistKind:  "other_running",
			Owner:      strings.Join(runningOwners, ", "),
		}, nil
	}

	if !access.Exists {
		return &platform.ComposeDirCheckResult{
			TargetFile: targetFile,
			Writable:   true,
			ExistKind:  "none",
		}, nil
	}

	if len(stoppedOwners) > 0 {
		return &platform.ComposeDirCheckResult{
			TargetFile: targetFile,
			Writable:   true,
			ExistKind:  "other_stopped",
			Owner:      strings.Join(stoppedOwners, ", "),
		}, nil
	}

	isSame := sameByWorkDir || (fileDeclProj != "" && fileDeclProj == curProj)
	if !isSame && existingYAML != "" && len(fileImageRepos) > 0 {
		allMatch := true
		for r := range fileImageRepos {
			if !curProjRepos[r] {
				allMatch = false
				break
			}
		}
		if allMatch {
			isSame = true
		}
	}
	if isSame {
		return &platform.ComposeDirCheckResult{
			TargetFile: targetFile,
			Writable:   true,
			ExistKind:  "same",
		}, nil
	}

	return &platform.ComposeDirCheckResult{
		TargetFile: targetFile,
		Writable:   true,
		ExistKind:  "other_untracked",
	}, nil
}

type reconstructedServiceSpec struct {
	ContainerName string   `yaml:"container_name,omitempty"`
	Image         string   `yaml:"image"`
	NetworkMode   string   `yaml:"network_mode,omitempty"`
	Networks      []string `yaml:"networks,omitempty"`
	Restart       string   `yaml:"restart,omitempty"`
	User          string   `yaml:"user,omitempty"`
	Hostname      string   `yaml:"hostname,omitempty"`
	Privileged    bool     `yaml:"privileged,omitempty"`
	CapAdd        []string `yaml:"cap_add,omitempty"`
	ExtraHosts    []string `yaml:"extra_hosts,omitempty"`
	WorkingDir    string   `yaml:"working_dir,omitempty"`
	Entrypoint    []string `yaml:"entrypoint,omitempty"`
	Command       []string `yaml:"command,omitempty"`
	Ports         []string `yaml:"ports,omitempty"`
	Environment   []string `yaml:"environment,omitempty"`
	Volumes       []string `yaml:"volumes,omitempty"`
	Devices       []string `yaml:"devices,omitempty"`
}

func (a *Adapter) collectSuggestedComposeDirs(ctx context.Context, projName string) []platform.SuggestedComposeDir {
	_, dirs, _, _ := a.reconstructProjectCompose(ctx, projName)
	return dirs
}

func isSystemSkipSuggestedParent(dir string) bool {
	clean := normalizeComposePosixPath(dir)
	switch clean {
	case "", "/", "/etc", "/var", "/var/lib", "/var/run", "/run", "/usr", "/tmp", "/proc", "/sys", "/dev":
		return true
	}
	return strings.HasPrefix(clean, "/var/lib/docker")
}

// reconstructProjectCompose 从宿主机上当前归属 projName 的容器实例反推生成标准 compose.yaml 与快捷挂载父目录建议。
func (a *Adapter) reconstructProjectCompose(ctx context.Context, projName string) (string, []platform.SuggestedComposeDir, []string, error) {
	var list []containerSummary
	if err := a.getJSON(ctx, "/containers/json?all=1", &list); err != nil {
		return "", nil, nil, i18n.NewEngineError(i18n.MsgDockerListFailed, i18n.DetailOf(err))
	}

	type svcContainer struct {
		svcName string
		cs      containerSummary
	}
	picked := make(map[string]containerSummary)
	for _, cs := range list {
		if cs.Labels["dkstore.self"] == "true" {
			continue
		}
		if strings.TrimSpace(cs.Labels["com.docker.compose.project"]) != projName {
			continue
		}
		svc := strings.TrimSpace(cs.Labels["com.docker.compose.service"])
		if svc == "" {
			continue
		}
		prev, exists := picked[svc]
		if !exists || (prev.State != "running" && cs.State == "running") {
			picked[svc] = cs
		}
	}
	if len(picked) == 0 {
		return "", nil, nil, i18n.NewEngineError(i18n.MsgComposeNotEditable)
	}

	svcNames := make([]string, 0, len(picked))
	for s := range picked {
		svcNames = append(svcNames, s)
	}
	sort.Strings(svcNames)

	servicesNode := &yaml.Node{Kind: yaml.MappingNode}
	externalVols := make(map[string]bool)
	externalNets := make(map[string]bool)
	var suggested []platform.SuggestedComposeDir
	seenSuggested := make(map[string]bool)

	addSuggested := func(kind, rawDir string) {
		clean := normalizeComposePosixPath(rawDir)
		if isSystemSkipSuggestedParent(clean) || seenSuggested[clean] {
			return
		}
		seenSuggested[clean] = true
		suggested = append(suggested, platform.SuggestedComposeDir{Kind: kind, Path: clean})
	}

	for _, svcName := range svcNames {
		cs := picked[svcName]
		rawInspect := make(map[string]any)
		if err := a.getJSON(ctx, "/containers/"+url.PathEscape(cs.ID)+"/json", &rawInspect); err != nil {
			continue
		}
		cfgMap, _ := rawInspect["Config"].(map[string]any)
		hostCfg, _ := rawInspect["HostConfig"].(map[string]any)
		imageRef, _ := cfgMap["Image"].(string)
		if strings.TrimSpace(imageRef) == "" {
			imageRef = cs.Image
		}

		// 读取镜像默认 Config，仅输出用户自定义的差异项
		imgEnvSet := make(map[string]bool)
		var imgCfg map[string]any
		if imgID, _ := rawInspect["Image"].(string); imgID != "" {
			var img struct {
				Config map[string]any `json:"Config"`
			}
			if a.getJSON(ctx, "/images/"+url.PathEscape(imgID)+"/json", &img) == nil && img.Config != nil {
				imgCfg = img.Config
				for _, e := range toStringSlice(img.Config["Env"]) {
					imgEnvSet[e] = true
				}
			}
		}

		var spec reconstructedServiceSpec
		spec.Image = imageRef

		rawName, _ := rawInspect["Name"].(string)
		cName := strings.TrimPrefix(strings.TrimSpace(rawName), "/")
		defaultNameV2 := projName + "-" + svcName + "-1"
		defaultNameV1 := projName + "_" + svcName + "_1"
		if cName != "" && cName != defaultNameV2 && cName != defaultNameV1 {
			spec.ContainerName = cName
		}

		nm := extractDockerNetworkMode(hostCfg, rawInspect["NetworkSettings"])
		defaultProjNet := projName + "_default"
		switch {
		case nm == "" || nm == "default" || nm == defaultProjNet:
			// 使用 Compose 默认项目网络，无需额外声明
		case nm == "host" || nm == "none" || nm == "bridge" || strings.HasPrefix(nm, "container:"):
			spec.NetworkMode = nm
		default:
			spec.Networks = []string{nm}
			externalNets[nm] = true
		}

		spec.Restart = extractDockerRestartPolicy(hostCfg)

		customUser := strings.TrimSpace(anyToString(cfgMap["User"]))
		if imgCfg != nil && customUser == strings.TrimSpace(anyToString(imgCfg["User"])) {
			customUser = ""
		}
		spec.User = customUser

		customHostname := strings.TrimSpace(anyToString(cfgMap["Hostname"]))
		domainName := strings.TrimSpace(anyToString(cfgMap["Domainname"]))
		if customHostname != "" {
			if strings.HasPrefix(strings.TrimPrefix(cs.ID, "sha256:"), customHostname) ||
				(domainName == "" && isAutoDockerHexHostname(customHostname)) {
				customHostname = ""
			}
		}
		spec.Hostname = customHostname

		if hostCfg != nil {
			spec.Privileged, _ = hostCfg["Privileged"].(bool)
			spec.CapAdd = filterNonDefaultCapAdd(toStringSlice(hostCfg["CapAdd"]))
			spec.ExtraHosts = toStringSlice(hostCfg["ExtraHosts"])
		}

		if imgCfg != nil {
			curWD := strings.TrimSpace(anyToString(cfgMap["WorkingDir"]))
			if curWD != "" && curWD != strings.TrimSpace(anyToString(imgCfg["WorkingDir"])) {
				spec.WorkingDir = curWD
			}
			if !sameJSONValue(cfgMap["Entrypoint"], imgCfg["Entrypoint"]) {
				spec.Entrypoint = toStringSlice(cfgMap["Entrypoint"])
				spec.Command = toStringSlice(cfgMap["Cmd"])
			} else if !sameJSONValue(cfgMap["Cmd"], imgCfg["Cmd"]) {
				spec.Command = toStringSlice(cfgMap["Cmd"])
			}
		}

		if spec.NetworkMode != "host" {
			for _, p := range extractDockerPorts(hostCfg) {
				if p.ContainerPort == "" {
					continue
				}
				entry := p.ContainerPort
				if p.HostPort != "" {
					if p.HostIP != "" && p.HostIP != "0.0.0.0" && p.HostIP != "::" {
						entry = p.HostIP + ":" + p.HostPort + ":" + p.ContainerPort
					} else {
						entry = p.HostPort + ":" + p.ContainerPort
					}
				}
				proto := strings.ToLower(strings.TrimSpace(p.Protocol))
				if proto != "" && proto != "tcp" {
					entry += "/" + proto
				}
				spec.Ports = append(spec.Ports, entry)
			}
		}

		for _, rawEnv := range toStringSlice(cfgMap["Env"]) {
			if imgEnvSet[rawEnv] {
				continue
			}
			k, _, ok := strings.Cut(rawEnv, "=")
			if !ok || strings.TrimSpace(k) == "" {
				continue
			}
			spec.Environment = append(spec.Environment, rawEnv)
		}

		seenVolTarget := make(map[string]bool)
		if mountList, ok := rawInspect["Mounts"].([]any); ok {
			for _, mRaw := range mountList {
				m, ok := mRaw.(map[string]any)
				if !ok {
					continue
				}
				mType := strings.TrimSpace(anyToString(m["Type"]))
				dst := strings.TrimSpace(anyToString(m["Destination"]))
				if dst == "" || seenVolTarget[dst] {
					continue
				}
				rw := true
				if v, ok := m["RW"].(bool); ok {
					rw = v
				}
				switch mType {
				case "bind":
					src := strings.TrimSpace(anyToString(m["Source"]))
					if src == "" {
						continue
					}
					seenVolTarget[dst] = true
					line := src + ":" + dst
					if !rw {
						line += ":ro"
					}
					spec.Volumes = append(spec.Volumes, line)
					if src != "/etc/localtime" && src != "/etc/timezone" && !strings.HasSuffix(src, "docker.sock") {
						addSuggested("data_parent", posixDirOf(src))
					}
				case "volume":
					volName := strings.TrimSpace(anyToString(m["Name"]))
					if volName == "" || isAnonymousVolumeID(volName) {
						continue
					}
					seenVolTarget[dst] = true
					line := volName + ":" + dst
					if !rw {
						line += ":ro"
					}
					spec.Volumes = append(spec.Volumes, line)
					externalVols[volName] = true
				}
			}
		}
		for _, v := range extractDockerVolumes(rawInspect["Mounts"], hostCfg) {
			if v.ContainerPath == "" || seenVolTarget[v.ContainerPath] || v.HostPath == "" {
				continue
			}
			seenVolTarget[v.ContainerPath] = true
			line := v.HostPath + ":" + v.ContainerPath
			if strings.EqualFold(v.Mode, "ro") {
				line += ":ro"
			}
			spec.Volumes = append(spec.Volumes, line)
			if strings.HasPrefix(v.HostPath, "/") && v.HostPath != "/etc/localtime" && v.HostPath != "/etc/timezone" {
				addSuggested("data_parent", posixDirOf(v.HostPath))
			} else if !strings.HasPrefix(v.HostPath, "/") && !isAnonymousVolumeID(v.HostPath) {
				externalVols[v.HostPath] = true
			}
		}

		for _, d := range extractDockerDevices(hostCfg) {
			if d.HostPath == "" && d.ContainerPath == "" {
				continue
			}
			hp, cp := d.HostPath, d.ContainerPath
			if cp == "" {
				cp = hp
			}
			if hp == "" {
				hp = cp
			}
			entry := hp + ":" + cp
			if d.Permissions != "" && d.Permissions != "rwm" {
				entry += ":" + d.Permissions
			}
			spec.Devices = append(spec.Devices, entry)
		}

		var svcValNode yaml.Node
		if mErr := svcValNode.Encode(&spec); mErr == nil {
			servicesNode.Content = append(servicesNode.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Value: svcName},
				&svcValNode,
			)
		}
	}

	rootMap := &yaml.Node{Kind: yaml.MappingNode}
	rootMap.Content = append(rootMap.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "services"},
		servicesNode,
	)

	if len(externalVols) > 0 {
		volKeys := make([]string, 0, len(externalVols))
		for k := range externalVols {
			volKeys = append(volKeys, k)
		}
		sort.Strings(volKeys)
		volsNode := &yaml.Node{Kind: yaml.MappingNode}
		for _, k := range volKeys {
			extMap := &yaml.Node{
				Kind: yaml.MappingNode,
				Content: []*yaml.Node{
					{Kind: yaml.ScalarNode, Value: "external"},
					{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"},
				},
			}
			volsNode.Content = append(volsNode.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: k}, extMap)
		}
		rootMap.Content = append(rootMap.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "volumes"}, volsNode)
	}

	if len(externalNets) > 0 {
		netKeys := make([]string, 0, len(externalNets))
		for k := range externalNets {
			netKeys = append(netKeys, k)
		}
		sort.Strings(netKeys)
		netsNode := &yaml.Node{Kind: yaml.MappingNode}
		for _, k := range netKeys {
			extMap := &yaml.Node{
				Kind: yaml.MappingNode,
				Content: []*yaml.Node{
					{Kind: yaml.ScalarNode, Value: "external"},
					{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"},
				},
			}
			netsNode.Content = append(netsNode.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: k}, extMap)
		}
		rootMap.Content = append(rootMap.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "networks"}, netsNode)
	}

	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(rootMap); err != nil {
		return "", nil, nil, err
	}
	_ = enc.Close()

	// 追加 /opt 与用户主目录作为常用快捷目录（仅当宿主机上真实存在时加入）
	if fsTr, ok := a.tr.(transport.HostFSTransport); ok {
		if ls, err := fsTr.ListDirectories(ctx, "/"); err == nil && ls != nil {
			for _, d := range ls.Dirs {
				if d == "opt" {
					addSuggested("opt", "/opt")
				}
			}
			if ls.HomeDir != "" && !seenSuggested[normalizeComposePosixPath(ls.HomeDir)] {
				seenSuggested[normalizeComposePosixPath(ls.HomeDir)] = true
				suggested = append(suggested, platform.SuggestedComposeDir{
					Kind: "home",
					Path: normalizeComposePosixPath(ls.HomeDir),
				})
			}
		}
	}

	return strings.TrimRight(buf.String(), "\n") + "\n", suggested, svcNames, nil
}

func anyToString(v any) string {
	s, _ := v.(string)
	return s
}

func isAnonymousVolumeID(name string) bool {
	if len(name) != 64 {
		return false
	}
	for _, r := range name {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

// RelocateComposeRelativePaths 在将原 compose.yaml 另存到新目录时，把相对于原工作目录的挂载路径与 env_file 转为绝对路径，防止换目录后读不到原数据。
func RelocateComposeRelativePaths(rawYAML, oldWorkDir, newWorkDir string) string {
	oldDir := normalizeComposePosixPath(oldWorkDir)
	newDir := normalizeComposePosixPath(newWorkDir)
	if oldWorkDir == "" || newWorkDir == "" || oldDir == "" || oldDir == newDir {
		return rawYAML
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(rawYAML), &doc); err != nil || len(doc.Content) == 0 {
		return rawYAML
	}
	top := doc.Content[0]
	if top.Kind != yaml.MappingNode {
		return rawYAML
	}
	changed := false
	resolveRel := func(rel string) (string, bool) {
		trimmed := strings.TrimSpace(rel)
		if trimmed == "." || trimmed == ".." || strings.HasPrefix(trimmed, "./") || strings.HasPrefix(trimmed, "../") {
			return path.Clean(path.Join(oldDir, trimmed)), true
		}
		return rel, false
	}

	for i := 0; i+1 < len(top.Content); i += 2 {
		if top.Content[i].Value != "services" || top.Content[i+1].Kind != yaml.MappingNode {
			continue
		}
		svcsNode := top.Content[i+1]
		for j := 0; j+1 < len(svcsNode.Content); j += 2 {
			sMap := svcsNode.Content[j+1]
			if sMap.Kind != yaml.MappingNode {
				continue
			}
			for k := 0; k+1 < len(sMap.Content); k += 2 {
				field := sMap.Content[k].Value
				val := sMap.Content[k+1]
				if field == "volumes" && val.Kind == yaml.SequenceNode {
					for _, item := range val.Content {
						switch item.Kind {
						case yaml.ScalarNode:
							src, rest, hasColon := strings.Cut(item.Value, ":")
							if hasColon {
								if abs, ok := resolveRel(src); ok {
									item.Value = abs + ":" + rest
									changed = true
								}
							}
						case yaml.MappingNode:
							for m := 0; m+1 < len(item.Content); m += 2 {
								if item.Content[m].Value == "source" && item.Content[m+1].Kind == yaml.ScalarNode {
									if abs, ok := resolveRel(item.Content[m+1].Value); ok {
										item.Content[m+1].Value = abs
										changed = true
									}
								}
							}
						}
					}
				} else if field == "env_file" {
					if val.Kind == yaml.ScalarNode {
						if abs, ok := resolveRel(val.Value); ok {
							val.Value = abs
							changed = true
						}
					} else if val.Kind == yaml.SequenceNode {
						for _, item := range val.Content {
							if item.Kind == yaml.ScalarNode {
								if abs, ok := resolveRel(item.Value); ok {
									item.Value = abs
									changed = true
								}
							}
						}
					}
				}
			}
		}
	}
	if !changed {
		return rawYAML
	}
	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return rawYAML
	}
	_ = enc.Close()
	return strings.TrimRight(buf.String(), "\n") + "\n"
}
