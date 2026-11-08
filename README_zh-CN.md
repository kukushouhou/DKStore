# DKStore — 家庭 Docker 应用更新中心

[English](./README.md) | **简体中文**

> [!WARNING]
> **当前状态：早期开发中（Work in Progress / Pre-Alpha）**
> 本项目正在进行开发与内部测试，尚未发布正式稳定版本，请勿用于生产环境。

DKStore 是面向家庭 NAS、软路由（OpenWrt / iStoreOS）与 Linux 主机的跨平台 Docker 应用更新中心。只需在家中任意一台设备安装一次，即可像手机应用商店一样统一检查、一键安全更新并在必要时回退多台设备上的 Docker 应用，同时与群晖 Container Manager 等系统原生管理器无感并存。

## 核心能力

- **更新优先，而非运维优先**：围绕**“可更新 / 需谨慎更新（跨大版本、数据库服务与高风险规则）/ 已忽略（仅忽略当前版本或永久忽略）”**三组卡片展示，提供清晰的更新原因与官方 Release Notes 跳转。
- **一处安装，多端无 Agent 管理**：通过本机 `docker.sock` 或标准 SSH（支持 Socket 转发、免密 `sudo -n` 以及群晖管理员 `sudo -S` 标准输入提权）接入局域网内多台 Linux、群晖 NAS 与软路由设备。
- **与群晖 Container Manager 及 Compose 原生并存**：
  - 自动识别群晖 Container Manager 原生项目（`SYNO.Docker.Project`）、标准 Docker Compose 项目（解析 `docker-compose.yml` 与 `.env` 变量）及独立容器。
  - 自动识别群晖套件中心等平台托管容器（`vendor_managed`）并设为只读保护，防止误改系统套件。
- **安全的一键更新与单服务隔离**：
  - **执行前冲突检测**：核验容器 `ImageID` 与 Compose YAML 文件 `SHA-256`，若被外部修改则主动中止并提示重新扫描。
  - **分层下载实时进度**：通过 SSE 实时推送镜像总层数、已完成层数、已下载字节与当前阶段（`1/4` 至 `4/4`）。
  - **单服务精准升级**：Compose 与群晖项目更新仅替换目标服务的 `image:` 行（完整保留原 YAML 注释与缩进），并通过 `--no-deps` 仅重建目标服务，**绝不重启或改动同项目的数据库等其他服务**。
  - **独立容器原位重建**：完整保留容器名称、端口映射、挂载卷、环境变量、全部自定义网络（含 `Aliases` 与 `IPAMConfig`）、Labels 以及**原始运行/停止状态**（原为停止状态的容器更新后保持停止）；若新容器启动失败自动恢复原容器。
- **更新历史与有条件安全回退**：
  - 完整记录每次升级与回退任务的旧/新版本、Digest、匹配标签及执行日志。
  - 提供动态回退可行性核验（`GET /api/v1/updates/{id}/rollback-check`）：实时校验历史精确版本标签在远端仓库是否仍指向升级前 Digest（或宿主机本地仍保留旧镜像 ID），对于无精确版本标签的纯浮动标签（如 `:latest`）或已被覆盖的标签诚实提示不可回退原因。
- **全栈中英双语国际化（`zh-CN` / `en-US`）**：覆盖前端界面、后端 API 诊断与错误消息、回退说明以及多渠道推送通知（Webhook、企业微信、Bark、Telegram、ntfy）。

## 多平台打包与部署形态

- **单文件二进制 / Docker 容器**：内嵌前端静态资源（`cmd/dkstore`），纯 Go SQLite 零外部依赖。
- **群晖 DSM 7 SPK 套件工程**：[`packaging/synology/`](./packaging/synology)
- **OpenWrt / iStoreOS IPK 与 LuCI 入口工程**：[`packaging/openwrt/`](./packaging/openwrt)
- **Linux systemd 服务配置**：[`packaging/linux/dkstore.service`](./packaging/linux/dkstore.service)

## 开发构建

需要 Go 1.24+、Node 20+。

```powershell
# 构建前端静态资源
cd web; npm install; npm run build; cd ..

# 启动后端服务
go run ./cmd/dkstore --listen :8090 --data ./data

# 运行全部单元测试
go test -v ./...
```
