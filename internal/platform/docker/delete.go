package docker

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/kukushouhou/DKStore/internal/i18n"
	"github.com/kukushouhou/DKStore/internal/platform"
	"github.com/kukushouhou/DKStore/internal/transport"
)

type inspectedDeletePlan struct {
	preview            *platform.DeleteAppPreview
	containerIDs       []string
	imageRefs          []string
	projectName        string
	workDir            string
	configFiles        []string
	envFiles           []string
	resolvedCfgPaths   []string
}

// PreviewDeleteApp 采集独立容器或整个 Compose 项目关联的容器、镜像、数据卷与挂载路径清单。
func (a *Adapter) PreviewDeleteApp(
	ctx context.Context,
	appName string,
	source platform.SourceKind,
	project *platform.ProjectInfo,
) (*platform.DeleteAppPreview, error) {
	plan, err := a.inspectDeletePlan(ctx, appName, source, project)
	if err != nil {
		return nil, err
	}
	return plan.preview, nil
}

// DeleteApp 停止并删除独立容器或整个 Compose 项目，并按用户确认清理本地镜像与关联文件/数据卷。
func (a *Adapter) DeleteApp(ctx context.Context, req platform.DeleteAppRequest) (*platform.DeleteAppResult, error) {
	if req.Source == platform.SourceVendorManaged {
		return nil, &platform.UpdateError{
			Code:    "vendor_managed_readonly",
			Message: i18n.EngineDetail(i18n.MsgDockerVendorReadonly),
		}
	}
	execTr, ok := a.tr.(transport.ExecutableTransport)
	if !ok {
		return nil, &platform.UpdateError{
			Code:    "capability_unsupported",
			Message: i18n.EngineDetail(i18n.MsgDeleteUnsupported),
		}
	}

	plan, err := a.inspectDeletePlan(ctx, req.AppName, req.Source, req.Project)
	if err != nil {
		return nil, err
	}
	if !plan.preview.IsProject && len(plan.containerIDs) == 0 {
		return nil, i18n.NewEngineError(i18n.MsgDeleteAppNotFound, req.AppName)
	}

	allowedByID := make(map[string]platform.DeleteFileItem, len(plan.preview.Files))
	for _, f := range plan.preview.Files {
		if !f.Protected {
			allowedByID[f.ID] = f
		}
	}
	selectedSet := make(map[string]bool, len(req.FileIDs))
	for _, id := range req.FileIDs {
		id = strings.TrimSpace(id)
		if _, ok := allowedByID[id]; ok {
			selectedSet[id] = true
		}
	}

	res := &platform.DeleteAppResult{}

	// 1. 停止并移除容器（若为项目则连带清理整个项目容器与专属网络）
	if plan.preview.IsProject {
		if cleanTr, ok := a.tr.(transport.CleanupTransport); ok && plan.projectName != "" {
			_, _ = cleanTr.ExecComposeDown(ctx, plan.workDir, plan.configFiles, plan.envFiles, plan.projectName, false)
		}
		// 兜底确保该项目标签下的全部残留容器与专属网络均通过 Docker Engine API 彻底移除
		removedEngine, err := a.removeComposeContainersAndNetworks(ctx, execTr, plan.projectName, plan.containerIDs)
		if err != nil {
			return nil, err
		}
		if len(plan.containerIDs) > removedEngine {
			res.RemovedContainers = len(plan.containerIDs)
		} else {
			res.RemovedContainers = removedEngine
		}
	} else {
		for _, cid := range plan.containerIDs {
			resp, err := execTr.RequestDocker(ctx, http.MethodDelete, "/containers/"+url.PathEscape(cid)+"?force=1&v=0", nil, nil)
			if err != nil {
				return nil, err
			}
			status := resp.StatusCode
			body := readBodyString(resp.Body)
			_ = resp.Body.Close()
			if status >= 300 && status != http.StatusNotFound {
				return nil, fmt.Errorf("remove container %s: HTTP %d: %s", shortID(cid), status, body)
			}
			res.RemovedContainers++
		}
	}

	// 2. 删除用户明确勾选并经二次确认的 Docker 数据卷与宿主机路径
	if len(selectedSet) > 0 {
		var hostPaths []string
		for _, item := range plan.preview.Files {
			if item.Protected || !selectedSet[item.ID] {
				continue
			}
			switch item.Kind {
			case platform.DeleteFileVolume:
				resp, err := execTr.RequestDocker(ctx, http.MethodDelete, "/volumes/"+url.PathEscape(item.Path)+"?force=1", nil, nil)
				if err != nil {
					res.Warnings = append(res.Warnings, fmt.Sprintf("volume %s: %v", item.Path, err))
					continue
				}
				status := resp.StatusCode
				body := readBodyString(resp.Body)
				_ = resp.Body.Close()
				if status >= 300 && status != http.StatusNotFound {
					res.Warnings = append(res.Warnings, fmt.Sprintf("volume %s: HTTP %d %s", item.Path, status, body))
					continue
				}
				res.RemovedFiles++

			case platform.DeleteFileProjectDir:
				if strings.HasPrefix(item.ID, "project_file:") {
					hostPaths = append(hostPaths, item.Path)
					res.RemovedFiles++
					continue
				}
				// 若用户勾选了项目目录，但特意取消了项目目录内某个子挂载目录，则仅删除编排配置文件以保护未勾选的子目录数据
				hasUncheckedChild := false
				for _, other := range plan.preview.Files {
					if other.Kind == platform.DeleteFileBindMount && !other.Protected &&
						platform.IsSubPathOrSame(item.Path, other.Path) && !selectedSet[other.ID] {
						hasUncheckedChild = true
						break
					}
				}
				if hasUncheckedChild {
					for _, cf := range plan.resolvedCfgPaths {
						if !platform.IsProtectedHostPath(cf) {
							hostPaths = append(hostPaths, cf)
						}
					}
				} else {
					hostPaths = append(hostPaths, item.Path)
				}
				res.RemovedFiles++

			case platform.DeleteFileBindMount:
				hostPaths = append(hostPaths, item.Path)
				res.RemovedFiles++
			}
		}

		dedupPaths := dedupHostPathsForRemoval(hostPaths)
		if len(dedupPaths) > 0 {
			cleanTr, ok := a.tr.(transport.CleanupTransport)
			if !ok {
				res.Warnings = append(res.Warnings, i18n.EngineMessage(i18n.ZhCN, i18n.MsgDeleteUnsupported))
			} else if err := cleanTr.RemoveHostPaths(ctx, dedupPaths); err != nil {
				return nil, err
			}
		}
	}

	// 3. 按需删除本地关联镜像（force=0：若仍有其他容器共用则自动跳过）
	if req.DeleteImages {
		for _, imgRef := range plan.imageRefs {
			resp, err := execTr.RequestDocker(ctx, http.MethodDelete, "/images/"+url.PathEscape(imgRef)+"?force=0", nil, nil)
			if err != nil {
				res.SkippedImages = append(res.SkippedImages, imgRef)
				continue
			}
			status := resp.StatusCode
			_ = resp.Body.Close()
			switch {
			case status >= 200 && status < 300:
				res.RemovedImages++
			case status == http.StatusNotFound:
				// 已被前一个同 ID 标签一并清理，忽略
			default:
				res.SkippedImages = append(res.SkippedImages, imgRef)
			}
		}
	}

	return res, nil
}

func (a *Adapter) removeComposeContainersAndNetworks(
	ctx context.Context,
	execTr transport.ExecutableTransport,
	projectName string,
	knownIDs []string,
) (int, error) {
	targetIDs := make(map[string]bool, len(knownIDs))
	for _, id := range knownIDs {
		if id != "" {
			targetIDs[id] = true
		}
	}
	var list []containerSummary
	if err := a.getJSON(ctx, "/containers/json?all=1", &list); err == nil {
		for _, cs := range list {
			if projectName != "" && cs.Labels["com.docker.compose.project"] == projectName {
				targetIDs[cs.ID] = true
			}
		}
	}

	removed := 0
	for cid := range targetIDs {
		resp, err := execTr.RequestDocker(ctx, http.MethodDelete, "/containers/"+url.PathEscape(cid)+"?force=1&v=0", nil, nil)
		if err != nil {
			return removed, err
		}
		status := resp.StatusCode
		body := readBodyString(resp.Body)
		_ = resp.Body.Close()
		if status >= 300 && status != http.StatusNotFound {
			return removed, fmt.Errorf("remove project container %s: HTTP %d: %s", shortID(cid), status, body)
		}
		removed++
	}

	if projectName != "" {
		var nets []struct {
			ID     string            `json:"Id"`
			Name   string            `json:"Name"`
			Labels map[string]string `json:"Labels"`
		}
		if a.getJSON(ctx, "/networks", &nets) == nil {
			for _, n := range nets {
				if n.Labels["com.docker.compose.project"] == projectName && n.ID != "" {
					if resp, err := execTr.RequestDocker(ctx, http.MethodDelete, "/networks/"+url.PathEscape(n.ID), nil, nil); err == nil {
						_ = resp.Body.Close()
					}
				}
			}
		}
	}
	return removed, nil
}

func (a *Adapter) inspectDeletePlan(
	ctx context.Context,
	appName string,
	source platform.SourceKind,
	project *platform.ProjectInfo,
) (*inspectedDeletePlan, error) {
	if source == platform.SourceVendorManaged {
		return nil, &platform.UpdateError{
			Code:    "vendor_managed_readonly",
			Message: i18n.EngineDetail(i18n.MsgDockerVendorReadonly),
		}
	}

	isProject := source == platform.SourceCompose || source == platform.SourceDSMProject || project != nil
	projName := strings.TrimSpace(appName)
	var workDir string
	var cfgFiles, envFiles []string
	if project != nil {
		if strings.TrimSpace(project.Name) != "" {
			projName = strings.TrimSpace(project.Name)
		}
		workDir = strings.TrimSpace(project.WorkingDir)
		cfgFiles = append([]string(nil), project.ConfigFiles...)
		envFiles = append([]string(nil), project.EnvFiles...)
	}

	var list []containerSummary
	if err := a.getJSON(ctx, "/containers/json?all=1", &list); err != nil {
		return nil, i18n.NewEngineError(i18n.MsgDockerListFailed, i18n.DetailOf(err))
	}

	var matchedSummaries []containerSummary
	for _, cs := range list {
		if cs.Labels["dkstore.self"] == "true" {
			continue
		}
		if isProject {
			if cs.Labels["com.docker.compose.project"] == projName {
				matchedSummaries = append(matchedSummaries, cs)
			}
			continue
		}
		for _, n := range cs.Names {
			if strings.TrimPrefix(n, "/") == appName {
				matchedSummaries = append(matchedSummaries, cs)
				break
			}
		}
	}

	var (
		containerIDs   []string
		containerNames []string
		imageRefs      []string
		seenImages     = make(map[string]bool)
		fileMap        = make(map[string]platform.DeleteFileItem)
		fileOrder      []string
	)
	addImage := func(ref string) {
		ref = strings.TrimSpace(ref)
		if ref != "" && !seenImages[ref] {
			seenImages[ref] = true
			imageRefs = append(imageRefs, ref)
		}
	}
	putFile := func(item platform.DeleteFileItem) {
		if existing, ok := fileMap[item.ID]; ok {
			if item.DefaultSelect && !existing.Protected {
				existing.DefaultSelect = true
				existing.Reason = ""
			}
			fileMap[item.ID] = existing
			return
		}
		fileMap[item.ID] = item
		fileOrder = append(fileOrder, item.ID)
	}

	type rawMountItem struct {
		typ string
		name string
		src string
		dst string
		rw  bool
	}
	var collectedMounts []rawMountItem

	for _, cs := range matchedSummaries {
		containerIDs = append(containerIDs, cs.ID)
		raw := make(map[string]any)
		if err := a.getJSON(ctx, "/containers/"+url.PathEscape(cs.ID)+"/json", &raw); err != nil {
			cName := cs.ID[:minLen(len(cs.ID), 12)]
			if len(cs.Names) > 0 {
				cName = strings.TrimPrefix(cs.Names[0], "/")
			}
			containerNames = append(containerNames, cName)
			addImage(cs.Image)
			continue
		}

		cName, _ := raw["Name"].(string)
		cName = strings.TrimPrefix(cName, "/")
		if cName == "" && len(cs.Names) > 0 {
			cName = strings.TrimPrefix(cs.Names[0], "/")
		}
		if cName != "" {
			containerNames = append(containerNames, cName)
		}

		cfgMap, _ := raw["Config"].(map[string]any)
		labels := map[string]string{}
		if cfgMap != nil {
			if rawLabels, ok := cfgMap["Labels"].(map[string]any); ok {
				for k, v := range rawLabels {
					if s, ok := v.(string); ok {
						labels[k] = s
					}
				}
			}
		}
		if len(labels) == 0 {
			labels = cs.Labels
		}
		if isProject {
			if workDir == "" {
				workDir = strings.TrimSpace(labels["com.docker.compose.project.working_dir"])
			}
			if len(cfgFiles) == 0 {
				cfgFiles = splitFiles(labels["com.docker.compose.project.config_files"])
			}
			if len(envFiles) == 0 {
				envFiles = splitFiles(labels["com.docker.compose.project.environment_file"])
			}
		}

		var envSlice []string
		if cfgMap != nil {
			envSlice = toStringSlice(cfgMap["Env"])
			if cfgImg, _ := cfgMap["Image"].(string); cfgImg != "" {
				addImage(cfgImg)
			}
		}
		if len(imageRefs) == 0 || cs.Image != "" {
			addImage(cs.Image)
		}

		var mountSources []string
		mountsParsed := false
		if mList, ok := raw["Mounts"].([]any); ok && len(mList) > 0 {
			mountsParsed = true
			for _, m := range mList {
				mm, ok := m.(map[string]any)
				if !ok {
					continue
				}
				typ, _ := mm["Type"].(string)
				name, _ := mm["Name"].(string)
				src, _ := mm["Source"].(string)
				dst, _ := mm["Destination"].(string)
				rw := true
				if rwVal, ok := mm["RW"].(bool); ok {
					rw = rwVal
				}
				if src != "" {
					mountSources = append(mountSources, src)
				}
				collectedMounts = append(collectedMounts, rawMountItem{
					typ:  strings.ToLower(strings.TrimSpace(typ)),
					name: strings.TrimSpace(name),
					src:  strings.TrimSpace(src),
					dst:  strings.TrimSpace(dst),
					rw:   rw,
				})
			}
		}
		if !mountsParsed {
			if hostCfg, ok := raw["HostConfig"].(map[string]any); ok && hostCfg != nil {
				for _, b := range toStringSlice(hostCfg["Binds"]) {
					parts := strings.Split(b, ":")
					if len(parts) < 2 {
						continue
					}
					rw := true
					if len(parts) >= 3 && strings.Contains(parts[2], "ro") {
						rw = false
					}
					src := strings.TrimSpace(parts[0])
					dst := strings.TrimSpace(parts[1])
					if strings.HasPrefix(src, "/") || (len(src) >= 2 && src[1] == ':') {
						mountSources = append(mountSources, src)
						collectedMounts = append(collectedMounts, rawMountItem{
							typ: "bind",
							src: src,
							dst: dst,
							rw:  rw,
						})
					} else if src != "" {
						collectedMounts = append(collectedMounts, rawMountItem{
							typ:  "volume",
							name: src,
							dst:  dst,
							rw:   rw,
						})
					}
				}
			}
		}

		if isVendorManagedContainer(labels, envSlice, mountSources) {
			return nil, &platform.UpdateError{
				Code:    "vendor_managed_readonly",
				Message: i18n.EngineDetail(i18n.MsgDockerVendorReadonly),
			}
		}
	}

	sort.Strings(containerNames)
	cleanWorkDir := platform.NormalizeHostPath(workDir)

	var resolvedCfgPaths []string
	if isProject {
		seenCfg := make(map[string]bool)
		addCfg := func(p string) {
			np := platform.NormalizeHostPath(p)
			if np != "" && !seenCfg[np] {
				seenCfg[np] = true
				resolvedCfgPaths = append(resolvedCfgPaths, np)
			}
		}
		for _, cf := range cfgFiles {
			full := resolvePath(cleanWorkDir, cf)
			if b, err := a.tr.ReadFile(ctx, full); err == nil {
				addCfg(full)
				for _, img := range parseComposeImages(b, nil) {
					addImage(img)
				}
			}
		}
		if len(resolvedCfgPaths) == 0 && cleanWorkDir != "" {
			for _, alt := range []string{"compose.yaml", "docker-compose.yml", "docker-compose.yaml", "compose.yml"} {
				full := resolvePath(cleanWorkDir, alt)
				if b, err := a.tr.ReadFile(ctx, full); err == nil {
					addCfg(full)
					for _, img := range parseComposeImages(b, nil) {
						addImage(img)
					}
					break
				}
			}
		}
		for _, ef := range envFiles {
			full := resolvePath(cleanWorkDir, ef)
			if _, err := a.tr.ReadFile(ctx, full); err == nil {
				addCfg(full)
			}
		}
		if cleanWorkDir != "" {
			dotEnv := resolvePath(cleanWorkDir, ".env")
			if _, err := a.tr.ReadFile(ctx, dotEnv); err == nil {
				addCfg(dotEnv)
			}
		}

		if cleanWorkDir != "" {
			if platform.IsProtectedHostPath(cleanWorkDir) {
				for _, cf := range resolvedCfgPaths {
					if !platform.IsProtectedHostPath(cf) {
						putFile(platform.DeleteFileItem{
							ID:            "project_file:" + cf,
							Kind:          platform.DeleteFileProjectDir,
							Path:          cf,
							Target:        "compose.yaml",
							DefaultSelect: true,
							Protected:     false,
						})
					}
				}
			} else {
				putFile(platform.DeleteFileItem{
					ID:            "project_dir:" + cleanWorkDir,
					Kind:          platform.DeleteFileProjectDir,
					Path:          cleanWorkDir,
					Target:        "compose.yaml / .env",
					DefaultSelect: true,
					Protected:     false,
				})
			}
		} else {
			for _, cf := range resolvedCfgPaths {
				if !platform.IsProtectedHostPath(cf) {
					putFile(platform.DeleteFileItem{
						ID:            "project_file:" + cf,
						Kind:          platform.DeleteFileProjectDir,
						Path:          cf,
						Target:        "compose.yaml",
						DefaultSelect: true,
						Protected:     false,
					})
				}
			}
		}
	}

	for _, m := range collectedMounts {
		switch m.typ {
		case "volume":
			if m.name == "" {
				continue
			}
			putFile(platform.DeleteFileItem{
				ID:            "volume:" + m.name,
				Kind:          platform.DeleteFileVolume,
				Path:          m.name,
				Target:        m.dst,
				DefaultSelect: true,
				Protected:     false,
			})
		case "bind", "":
			if m.src == "" {
				continue
			}
			normSrc := platform.NormalizeHostPath(m.src)
			if normSrc == "" {
				continue
			}
			if platform.IsProtectedHostPath(normSrc) {
				putFile(platform.DeleteFileItem{
					ID:            "bind:" + normSrc,
					Kind:          platform.DeleteFileBindMount,
					Path:          normSrc,
					Target:        m.dst,
					DefaultSelect: false,
					Protected:     true,
					Reason:        "system_protected",
				})
				continue
			}
			// 若挂载目录恰好就是项目根目录本身，已在 project_dir 中展示，避免重复出现两条完全相同的路径
			if isProject && cleanWorkDir != "" && normSrc == cleanWorkDir {
				continue
			}
			defaultSel := true
			reason := ""
			if !m.rw {
				defaultSel = false
				reason = "readonly_bind"
			} else if isProject && cleanWorkDir != "" && !platform.IsSubPathOrSame(cleanWorkDir, normSrc) {
				defaultSel = false
				reason = "external_bind"
			}
			putFile(platform.DeleteFileItem{
				ID:            "bind:" + normSrc,
				Kind:          platform.DeleteFileBindMount,
				Path:          normSrc,
				Target:        m.dst,
				DefaultSelect: defaultSel,
				Protected:     false,
				Reason:        reason,
			})
		}
	}

	files := make([]platform.DeleteFileItem, 0, len(fileOrder))
	for _, id := range fileOrder {
		files = append(files, fileMap[id])
	}
	sort.SliceStable(files, func(i, j int) bool {
		if files[i].Protected != files[j].Protected {
			return !files[i].Protected
		}
		rank := func(k platform.DeleteFileKind) int {
			switch k {
			case platform.DeleteFileProjectDir:
				return 0
			case platform.DeleteFileVolume:
				return 1
			default:
				return 2
			}
		}
		return rank(files[i].Kind) < rank(files[j].Kind)
	})

	return &inspectedDeletePlan{
		preview: &platform.DeleteAppPreview{
			AppName:    appName,
			Source:     source,
			IsProject:  isProject,
			Containers: containerNames,
			Images:     imageRefs,
			Files:      files,
		},
		containerIDs:     containerIDs,
		imageRefs:        imageRefs,
		projectName:      projName,
		workDir:          cleanWorkDir,
		configFiles:      cfgFiles,
		envFiles:         envFiles,
		resolvedCfgPaths: resolvedCfgPaths,
	}, nil
}

// dedupHostPathsForRemoval 去重并剔除已被所选父目录完整包含的子路径。
func dedupHostPathsForRemoval(rawPaths []string) []string {
	var cleaned []string
	seen := make(map[string]bool, len(rawPaths))
	for _, p := range rawPaths {
		np := platform.NormalizeHostPath(p)
		if np == "" || platform.IsProtectedHostPath(np) || seen[np] {
			continue
		}
		seen[np] = true
		cleaned = append(cleaned, np)
	}
	sort.Slice(cleaned, func(i, j int) bool {
		return len(cleaned[i]) < len(cleaned[j])
	})
	var out []string
	for _, p := range cleaned {
		covered := false
		for _, parent := range out {
			if platform.IsSubPathOrSame(parent, p) {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, p)
		}
	}
	return out
}
