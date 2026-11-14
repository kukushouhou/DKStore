# 更新日志 / Changelog

本文件记录 **DKStore** 的所有重要版本变更。

All notable changes to **DKStore** are documented in this file.

## [开发中 / Unreleased]

## [v0.4.0] - 2026-11-15

### 新功能
- **全平台单二进制原地自更新与自定义镜像加速**：新增容器与原生套件（Docker、群晖 `.spk`、飞牛 `.fpk`、OpenWrt、Linux 服务与 Windows）统一单二进制自更新能力，通过数据目录持久化自举、启动失败连续熔断自动回退与进程原地热切换（`execve` 保持 PID 与登录会话不断联）实现零辅助容器升级；支持 `HTTP Range` 断点续传与 `SHA-256` 完整性校验、本地更新包（含各平台安装包与自编译二进制）直接上传升级，并在首次初始化向导第 3 步与设置页统一提供「源码仓库（支持 Fork 分叉定制）」及「GitHub / 自定义镜像（内置可用加速节点下拉与前缀/换域名双格式）」切换与实时连通测速 ([`1508ad4`](https://github.com/kukushouhou/DKStore/commit/1508ad4), [`445c9de`](https://github.com/kukushouhou/DKStore/commit/445c9de))
- **容器与首页 Web 访问入口及双粒度本地信息编辑**：首页卡片与容器列表新增容器 Web 访问入口（自动识别桥接对外映射端口与 `host` 主机网络暴露端口并拼接访问地址，全面兼容群晖免 SSH WebAPI、远程 SSH 与本机直连），并在名称紧右侧提供直观的信息编辑入口；支持按整个项目或具体子容器维度设置备注名、标签、访问入口（固定映射端口与协议 / 自定义地址 / 设为无入口）及本地备注，内置同款分段标签筛选栏（无标签时自动隐藏）与卡片底部基线对齐排版 ([`ce013de`](https://github.com/kukushouhou/DKStore/commit/ce013de))
- **容器列表双行布局与真实应用图标渲染**：容器运行状态页升级为双行结构并打通真实应用图标加载（补齐此前仅显示名称前两位字母而未加载真实图标的问题，多容器项目自动选取主服务图标），左侧应用图标覆盖两行高度，右侧名称与来源/标签分上下两行，`CPU` 与 `内存` 同步拆分为双行对齐展示 ([`ce013de`](https://github.com/kukushouhou/DKStore/commit/ce013de))

---

### Features
- **Cross-Platform In-Place Self-Update & Custom Mirror Channels**: Added unified single-binary self-update across Docker containers and native installs (Synology `.spk`, fnOS `.fpk`, OpenWrt, Linux systemd, and Windows) via persistent `<DataDir>/bin` bootstrap, 3-strike crash-loop auto-rollback, and PID/session-preserving `execve` hot-swaps without helper containers; supports `HTTP Range` resumable downloads with `SHA-256` verification, direct local package/binary uploads, and unified Repository (Fork-ready) + Update Method (`GitHub` / `Custom Mirror` with verified presets and prefix or host-replacement modes) controls with live latency testing across both Setup Wizard Step 3 and Settings ([`1508ad4`](https://github.com/kukushouhou/DKStore/commit/1508ad4), [`445c9de`](https://github.com/kukushouhou/DKStore/commit/445c9de))
- **Web Access Entries & Dual-Scope Local Metadata Editing**: Added one-click Web access entries across Home cards and the Containers list (automatically resolving published bridge ports and `host`-network exposed ports across Synology DSM WebAPI, remote SSH, and local Docker sockets) alongside an inline edit trigger next to each project/container name; supports per-project and per-container aliases, tags, entry modes (fixed mapped port & scheme / custom URL / no entry), and local notes, with a native segmented tag filter bar (auto-hidden when unused) and baseline-aligned card footers ([`ce013de`](https://github.com/kukushouhou/DKStore/commit/ce013de))
- **Two-Line Container List Layout & Real App Icons**: Upgraded the Containers list to a clean two-line row structure with real application icons spanning both lines (replacing static two-letter badges and automatically selecting primary service icons for multi-container projects), pairing two-line name/tag metadata with stacked `CPU` and `Memory` metrics ([`ce013de`](https://github.com/kukushouhou/DKStore/commit/ce013de))

## [v0.3.0] - 2026-11-14

### 新功能
- **设备管理实时资源与多盘空间监控**：设备卡片新增宿主机真实 CPU 占用、物理内存占用与存储空间剩余容量展示（全面兼容群晖管理后台免 SSH 连接、远程 SSH 与本机直连模式），支持本地快照秒开与定时/手动刷新；多存储空间设备采用同行多段进度条独立着色并支持悬停查看单卷明细，离线态保持同构等高骨架对齐 ([`971a7c8`](https://github.com/kukushouhou/DKStore/commit/971a7c8))

### 问题修复
- **按新版配置更新时的容器移除保护与清理确认**：按多配置文件组合核对新版配置是否减少原有容器（准确区分更换镜像地址、修改容器名称与移除容器），在多容器项目与单容器配置更新双入口实时给出客观提醒，并在提交时直接复用删除确认弹窗选择是否同步清理旧镜像与关联目录（已被新配置中其他容器或同设备其他应用引用的目录与子文件夹自动置灰禁用，并在路径后括号标明具体引用方）；未确认前服务端直接拦截改写、常规更新不再附带孤儿容器清理，同时补齐配置回退重建失败时的错误提示 ([`e554c82`](https://github.com/kukushouhou/DKStore/commit/e554c82))

---

### Features
- **Real-Time Device CPU, Memory & Multi-Disk Storage Metrics**: Added real-time host CPU usage, physical memory usage, and remaining disk capacity to device cards across Synology DSM WebAPI (SSH-free), remote SSH, and local socket modes, backed by instant SQLite snapshots plus periodic and manual refresh; multi-volume hosts render side-by-side segmented progress bars with per-volume hover details while keeping uniform card skeletons when offline ([`971a7c8`](https://github.com/kukushouhou/DKStore/commit/971a7c8))

### Fixes
- **Container Removal Protection & Reused Cleanup Confirmation on Config Updates**: Compare effective multi-file Compose container sets before applying updated configurations (accurately distinguishing image repository swaps, container renames, and container removals), show live objective reminders across both project and single-container config update modals, and reuse the deletion modal to choose optional image and folder cleanup while automatically disabling any parent/child folder or volume referenced by other containers with inline referencer labels; block unconfirmed container removals on the server, restrict orphan container cleanup strictly to confirmed removals, and report rebuild errors during automatic configuration rollback ([`e554c82`](https://github.com/kukushouhou/DKStore/commit/e554c82))

## [v0.2.0] - 2026-11-14

### 新功能
- **原生多平台发行包生态**：新增群晖 DSM 7 套件（`.spk`）、飞牛 fnOS 应用包（`.fpk`）与 OpenWrt / iStoreOS LuCI 插件（`.ipk` / `.apk` / `.run`），支持复用宿主管理员登录态免二次密码直进并自动接入本机容器 ([`2af488f`](https://github.com/kukushouhou/DKStore/commit/2af488f), [`934d007`](https://github.com/kukushouhou/DKStore/commit/934d007), [`adf4d2b`](https://github.com/kukushouhou/DKStore/commit/adf4d2b))
- **群晖免 SSH 接入与设备管理增强**：新增群晖 DSM WebAPI 免 SSH 接入模式（支持 2FA 动态码与设备令牌持久化），完善首次初始化向导，重构设备卡片展示（独立呈现连接地址、CPU 架构与系统环境）并补全编辑设备时的已存密码/私钥留空提示与解绑管理 ([`34b414a`](https://github.com/kukushouhou/DKStore/commit/34b414a), [`dc34423`](https://github.com/kukushouhou/DKStore/commit/dc34423), [`ced256b`](https://github.com/kukushouhou/DKStore/commit/ced256b), [`52f128f`](https://github.com/kukushouhou/DKStore/commit/52f128f))
- **单服务精准升级、分层进度与安全回退**：新增一键单服务升级（自动保留自定义镜像加速源前缀）、多段版本号前缀锁定、镜像分层流式进度、实时只读 `xterm` 更新日志、简洁直观的 YAML/容器配置在线修改更新（支持防覆盖风险检测与一键恢复原配置），以及基于结构化版本标签的安全回退与历史记录快捷清理 ([`a5833e4`](https://github.com/kukushouhou/DKStore/commit/a5833e4), [`d2855be`](https://github.com/kukushouhou/DKStore/commit/d2855be), [`08f614e`](https://github.com/kukushouhou/DKStore/commit/08f614e), [`6dab9c8`](https://github.com/kukushouhou/DKStore/commit/6dab9c8), [`389d6d6`](https://github.com/kukushouhou/DKStore/commit/389d6d6))
- **容器运行监控与一键移除**：新增树状项目/容器实时资源监控视图（内置本地 SQLite 快照与后台并发采样实现秒开，独立展示所属设备与当前镜像），支持独立容器与整项目的可选镜像及目录清理移除 ([`27349c0`](https://github.com/kukushouhou/DKStore/commit/27349c0), [`09f47ab`](https://github.com/kukushouhou/DKStore/commit/09f47ab), [`14b6c2b`](https://github.com/kukushouhou/DKStore/commit/14b6c2b), [`986e1fd`](https://github.com/kukushouhou/DKStore/commit/986e1fd))
- **私有仓库凭据原位快捷配置**：更新页私有仓库未授权提示支持当前页直接弹窗、自动带出目标仓库域名并在保存后立即触发重新检查 ([`f2421c6`](https://github.com/kukushouhou/DKStore/commit/f2421c6))

### 问题修复
- **鉴权会话与版本候选列表修复**：修复登录会话过期后前端未主动感知并跳转重登的问题，修正版本候选标签 SemVer 倒序排列与变体噪音过滤逻辑 ([`2600492`](https://github.com/kukushouhou/DKStore/commit/2600492), [`782a01c`](https://github.com/kukushouhou/DKStore/commit/782a01c))

---

### Features
- **Native Multi-Platform Packages**: Added native installers for Synology DSM 7 (`.spk`), fnOS App Center (`.fpk`), and OpenWrt / iStoreOS LuCI (`.ipk` / `.apk` / `.run`) with seamless host admin session reuse and automatic local Docker discovery ([`2af488f`](https://github.com/kukushouhou/DKStore/commit/2af488f), [`934d007`](https://github.com/kukushouhou/DKStore/commit/934d007), [`adf4d2b`](https://github.com/kukushouhou/DKStore/commit/adf4d2b))
- **SSH-Free Synology Access & Host Management**: Added direct Synology DSM WebAPI mode (with 2FA OTP and persistent device tokens), guided initial setup wizard, redesigned device cards (explicitly displaying connection address, CPU architecture, and OS/Docker environment), and added leave-blank credential hints for device editing alongside explicit unbinding ([`34b414a`](https://github.com/kukushouhou/DKStore/commit/34b414a), [`dc34423`](https://github.com/kukushouhou/DKStore/commit/dc34423), [`ced256b`](https://github.com/kukushouhou/DKStore/commit/ced256b), [`52f128f`](https://github.com/kukushouhou/DKStore/commit/52f128f))
- **Safe Service-Level Upgrades & Conditional Rollback**: Added one-click single-service upgrades (preserving custom registry mirror prefixes), multi-segment version prefix locking, real-time per-layer pull progress, read-only `xterm` live logs, streamlined in-place YAML/container config updates (with overwrite risk detection and one-click original config restore), structured-tag verified rollbacks, and quick history cleanup ([`a5833e4`](https://github.com/kukushouhou/DKStore/commit/a5833e4), [`d2855be`](https://github.com/kukushouhou/DKStore/commit/d2855be), [`08f614e`](https://github.com/kukushouhou/DKStore/commit/08f614e), [`6dab9c8`](https://github.com/kukushouhou/DKStore/commit/6dab9c8), [`389d6d6`](https://github.com/kukushouhou/DKStore/commit/389d6d6))
- **Runtime Container Tree & App Removal**: Added hierarchical project/container resource monitor (with persisted SQLite snapshots and concurrent sampling for instant cold loads, plus separate Device and Current Image columns) and container/project removal with optional image and directory cleanup ([`27349c0`](https://github.com/kukushouhou/DKStore/commit/27349c0), [`09f47ab`](https://github.com/kukushouhou/DKStore/commit/09f47ab), [`14b6c2b`](https://github.com/kukushouhou/DKStore/commit/14b6c2b), [`986e1fd`](https://github.com/kukushouhou/DKStore/commit/986e1fd))
- **Inline Private Registry Credentials**: Private registry auth prompts on the Updates page now open an inline modal with the target registry domain pre-filled and automatically trigger a recheck upon saving ([`f2421c6`](https://github.com/kukushouhou/DKStore/commit/f2421c6))

### Fixes
- **Auth Session & Version Picker Fixes**: Fixed expired login sessions not redirecting proactively on `401`, and corrected SemVer descending sort and noisy variant filtering in the version picker ([`2600492`](https://github.com/kukushouhou/DKStore/commit/2600492), [`782a01c`](https://github.com/kukushouhou/DKStore/commit/782a01c))

## [v0.1.0] - 2026-11-09

### 新功能
- **多设备远程直连容器更新检测**：支持通过本机 Unix Socket 与远程 SSH 只读扫描 Docker Compose 项目、群晖 Container Manager 项目及独立容器（被管设备无需安装客户端），直连远端镜像仓库执行 Digest 比对与镜像源归一化 ([`52ea8c5`](https://github.com/kukushouhou/DKStore/commit/52ea8c5), [`a84bdd3`](https://github.com/kukushouhou/DKStore/commit/a84bdd3))
- **智能版本识别与风险分级**：支持语义化版本与 `latest` 等浮动标签的真实版本号反查，自动识别跨大版本升级、数据库/中间件组件与同标签重推镜像，将待更新服务拆分为「常规更新」与「谨慎更新」展示 ([`52ea8c5`](https://github.com/kukushouhou/DKStore/commit/52ea8c5), [`f578be7`](https://github.com/kukushouhou/DKStore/commit/f578be7))
- **单二进制双语控制台与通知推送**：采用 Go 内嵌前端单二进制架构（SQLite 存储 + AES-GCM 凭据加密），内置管理员密码鉴权、版本忽略规则、定时巡检任务及 Webhook / Telegram / Bark 消息推送 ([`d0a0102`](https://github.com/kukushouhou/DKStore/commit/d0a0102), [`52ea8c5`](https://github.com/kukushouhou/DKStore/commit/52ea8c5))

---

### Features
- **Direct Multi-Host Update Detection**: Read-only discovery of Docker Compose projects, Synology Container Manager projects, and standalone containers via local Unix sockets and remote SSH without installing client daemons, with direct registry digest comparison and mirror normalization ([`52ea8c5`](https://github.com/kukushouhou/DKStore/commit/52ea8c5), [`a84bdd3`](https://github.com/kukushouhou/DKStore/commit/a84bdd3))
- **Smart Version Resolution & Risk Classification**: Automatic version label resolution for SemVer and floating tags (such as `latest`), classifying major bumps, databases/middleware, and republished tags into Normal and Caution update groups ([`52ea8c5`](https://github.com/kukushouhou/DKStore/commit/52ea8c5), [`f578be7`](https://github.com/kukushouhou/DKStore/commit/f578be7))
- **Single-Binary Bilingual Console & Notifications**: Self-contained Go binary with embedded web UI (SQLite storage + AES-GCM credential encryption), built-in admin authentication, ignore rules, scheduled scans, and Webhook / Telegram / Bark notifications ([`d0a0102`](https://github.com/kukushouhou/DKStore/commit/d0a0102), [`52ea8c5`](https://github.com/kukushouhou/DKStore/commit/52ea8c5))
