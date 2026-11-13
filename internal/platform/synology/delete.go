package synology

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/kukushouhou/DKStore/internal/i18n"
	"github.com/kukushouhou/DKStore/internal/platform"
)

type synoWebAPIProjectDeleter interface {
	ExecSynoWebAPIProjectDelete(ctx context.Context, projectID string) error
}

func (a *Adapter) invalidateProjectCache() {
	a.projMu.Lock()
	a.cachedProjs = nil
	a.projMu.Unlock()
}

// PreviewDeleteApp 预览群晖容器或整项目关联的容器、镜像与挂载路径。
func (a *Adapter) PreviewDeleteApp(
	ctx context.Context,
	appName string,
	source platform.SourceKind,
	project *platform.ProjectInfo,
) (*platform.DeleteAppPreview, error) {
	if source == platform.SourceVendorManaged {
		return nil, &platform.UpdateError{
			Code:    "vendor_managed_readonly",
			Message: bi("群晖套件中心托管的容器受只读保护", "This container is managed by Synology Package Center"),
		}
	}
	if a.dock != nil {
		return a.dock.PreviewDeleteApp(ctx, appName, source, project)
	}
	if a.dsmCli == nil {
		return nil, i18n.NewHostError("dsm.not_configured", nil)
	}

	isProject := source == platform.SourceCompose || source == platform.SourceDSMProject || project != nil
	seenImg := make(map[string]bool)
	var images []string
	addImg := func(ref string) {
		ref = strings.TrimSpace(ref)
		if ref != "" && !seenImg[ref] {
			seenImg[ref] = true
			images = append(images, ref)
		}
	}

	if project != nil && strings.TrimSpace(project.DSMProjectID) != "" {
		detail, err := a.dsmCli.GetProject(ctx, project.DSMProjectID)
		if err != nil {
			return nil, err
		}
		if detail.IsPackage || strings.HasPrefix(detail.Path, "/var/packages/") {
			return nil, &platform.UpdateError{
				Code:    "vendor_managed_readonly",
				Message: bi("群晖套件中心托管的项目受只读保护", "This project is managed by Synology Package Center"),
			}
		}
		var containers []string
		for _, c := range detail.Containers {
			n := strings.TrimPrefix(c.Name, "/")
			if n != "" {
				containers = append(containers, n)
			}
			if c.Config.Image != "" {
				addImg(c.Config.Image)
			}
		}
		sort.Strings(containers)
		if detail.Content != "" {
			for _, img := range parseServicesFromComposeYAML(detail.Content) {
				addImg(img)
			}
		}
		var files []platform.DeleteFileItem
		if normPath := platform.NormalizeHostPath(detail.Path); normPath != "" {
			files = append(files, platform.DeleteFileItem{
				ID:            "project_dir:" + normPath,
				Kind:          platform.DeleteFileProjectDir,
				Path:          normPath,
				Target:        "compose.yaml",
				DefaultSelect: false,
				Protected:     true,
				Reason:        "dsm_api_host_path_unsupported",
			})
		}
		return &platform.DeleteAppPreview{
			AppName:    appName,
			Source:     source,
			IsProject:  true,
			Containers: containers,
			Images:     images,
			Files:      files,
		}, nil
	}

	containers, err := a.dsmCli.ListContainers(ctx)
	if err != nil {
		return nil, err
	}

	if isProject {
		projName := strings.TrimSpace(appName)
		if project != nil && strings.TrimSpace(project.Name) != "" {
			projName = strings.TrimSpace(project.Name)
		}
		var cNames []string
		for _, c := range containers {
			if strings.EqualFold(strings.TrimSpace(c.Labels["com.docker.compose.project"]), projName) {
				cNames = append(cNames, strings.TrimPrefix(c.Name, "/"))
				addImg(containerImageRef(c))
			}
		}
		sort.Strings(cNames)
		return &platform.DeleteAppPreview{
			AppName:    appName,
			Source:     source,
			IsProject:  true,
			Containers: cNames,
			Images:     images,
			Files:      []platform.DeleteFileItem{},
		}, nil
	}

	standalone := findStandaloneContainer(containers, platform.UpdateRequest{AppName: appName, ServiceName: appName})
	if standalone == nil {
		return nil, i18n.NewEngineError(i18n.MsgDeleteAppNotFound, appName)
	}
	cName := strings.TrimPrefix(standalone.Name, "/")
	if cName == "" {
		cName = appName
	}
	addImg(containerImageRef(*standalone))

	var files []platform.DeleteFileItem
	if prof, _, pErr := a.dsmCli.GetContainerProfile(ctx, cName); pErr == nil {
		for _, v := range extractDSMVolumes(prof) {
			if v.HostPath != "" {
				files = append(files, platform.DeleteFileItem{
					ID:            "bind:" + v.HostPath,
					Kind:          platform.DeleteFileBindMount,
					Path:          v.HostPath,
					Target:        v.ContainerPath,
					DefaultSelect: false,
					Protected:     true,
					Reason:        "dsm_api_host_path_unsupported",
				})
			}
		}
	}

	return &platform.DeleteAppPreview{
		AppName:    appName,
		Source:     source,
		IsProject:  false,
		Containers: []string{cName},
		Images:     images,
		Files:      files,
	}, nil
}

// DeleteApp 删除群晖独立容器或整个项目（兼容 SSH 模式与纯 DSM WebAPI 模式）。
func (a *Adapter) DeleteApp(ctx context.Context, req platform.DeleteAppRequest) (*platform.DeleteAppResult, error) {
	if req.Source == platform.SourceVendorManaged {
		return nil, &platform.UpdateError{
			Code:    "vendor_managed_readonly",
			Message: bi("群晖套件中心托管的容器受只读保护", "This container is managed by Synology Package Center"),
		}
	}

	// SSH / 本机模式：先通过 Docker 引擎与传输层完成完整的容器、网络、文件/卷与镜像删除，再同步注销 DSM Project 记录
	if a.dock != nil {
		res, err := a.dock.DeleteApp(ctx, req)
		if err != nil {
			return nil, err
		}
		if req.Project != nil && strings.TrimSpace(req.Project.DSMProjectID) != "" {
			if a.dsmCli != nil {
				_ = a.dsmCli.CleanAndDeleteProject(ctx, req.Project.DSMProjectID)
			} else if delExec, ok := a.tr.(synoWebAPIProjectDeleter); ok {
				_ = delExec.ExecSynoWebAPIProjectDelete(ctx, req.Project.DSMProjectID)
			}
		}
		a.invalidateProjectCache()
		return res, nil
	}

	if a.dsmCli == nil {
		return nil, i18n.NewHostError("dsm.not_configured", nil)
	}

	prev, err := a.PreviewDeleteApp(ctx, req.AppName, req.Source, req.Project)
	if err != nil {
		return nil, err
	}

	res := &platform.DeleteAppResult{}
	if req.Project != nil && strings.TrimSpace(req.Project.DSMProjectID) != "" {
		if err := a.dsmCli.CleanAndDeleteProject(ctx, req.Project.DSMProjectID); err != nil {
			return nil, err
		}
		res.RemovedContainers = len(prev.Containers)
		if res.RemovedContainers == 0 {
			res.RemovedContainers = 1
		}
	} else {
		containers, err := a.dsmCli.ListContainers(ctx)
		if err != nil {
			return nil, err
		}
		wantSet := make(map[string]bool, len(prev.Containers))
		for _, n := range prev.Containers {
			wantSet[n] = true
		}
		for _, c := range containers {
			cName := strings.TrimPrefix(c.Name, "/")
			if !wantSet[cName] {
				continue
			}
			if c.State.Running || strings.EqualFold(c.State.Status, "running") || strings.EqualFold(c.Status, "running") {
				_ = a.dsmCli.StopContainer(ctx, cName)
			}
			if err := a.dsmCli.DeleteContainer(ctx, cName); err != nil {
				return nil, err
			}
			res.RemovedContainers++
		}
	}
	a.invalidateProjectCache()

	if req.DeleteImages {
		for _, imgRef := range prev.Images {
			if err := a.dsmCli.DeleteImage(ctx, imgRef); err != nil {
				res.SkippedImages = append(res.SkippedImages, imgRef)
			} else {
				res.RemovedImages++
			}
		}
	}
	return res, nil
}

// CleanAndDeleteProject 通过 SYNO.Docker.Project 先清理项目容器与网络，再注销该项目定义。
func (c *dsmClient) CleanAndDeleteProject(ctx context.Context, projectID string) error {
	_ = c.streamOrCallProjectMethod(ctx, projectID, "clean_stream", "clean")
	return c.streamOrCallProjectMethod(ctx, projectID, "delete", "delete_stream")
}

func (c *dsmClient) streamOrCallProjectMethod(ctx context.Context, projectID, primaryMethod, fallbackMethod string) error {
	runOne := func(method string) error {
		if strings.HasSuffix(method, "_stream") {
			sid, err := c.currentSID(ctx)
			if err != nil {
				return err
			}
			cctx, cancel := context.WithTimeout(ctx, dsmLongCallTimeout)
			defer cancel()
			vals := url.Values{
				"api":     {"SYNO.Docker.Project"},
				"version": {"1"},
				"method":  {method},
				"id":      {jsonParam(projectID)},
				"_sid":    {sid},
			}
			req, err := http.NewRequestWithContext(cctx, http.MethodPost, c.cfg.URL+"/webapi/entry.cgi", strings.NewReader(vals.Encode()))
			if err != nil {
				return err
			}
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			resp, err := c.httpClient.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<20))
			if resp.StatusCode >= 400 {
				return &DSMError{API: "SYNO.Docker.Project", Method: method, Code: resp.StatusCode}
			}
			return nil
		}
		return c.call(ctx, "SYNO.Docker.Project", "1", method, map[string]string{
			"id": jsonParam(projectID),
		}, nil)
	}
	if err := runOne(primaryMethod); err == nil {
		return nil
	}
	return runOne(fallbackMethod)
}

// DeleteImage 通过 SYNO.Docker.Image 删除本地未被其他容器占用的镜像标签。
func (c *dsmClient) DeleteImage(ctx context.Context, imageRef string) error {
	repo, tag := splitDSMRepoTag(imageRef)
	if tag == "" {
		tag = "latest"
	}
	_, strippedRepo := splitDSMRegistryAndRepo(repo)
	if strippedRepo == "" {
		strippedRepo = repo
	}
	items := []map[string]string{
		{
			"repository": strippedRepo,
			"tag":        tag,
		},
	}
	if err := c.call(ctx, "SYNO.Docker.Image", "1", "delete", map[string]string{
		"images": jsonParam(items),
	}, nil); err != nil {
		if strippedRepo != repo {
			itemsFull := []map[string]string{{"repository": repo, "tag": tag}}
			return c.call(ctx, "SYNO.Docker.Image", "1", "delete", map[string]string{
				"images": jsonParam(itemsFull),
			}, nil)
		}
		return err
	}
	return nil
}
