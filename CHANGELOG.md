# 更新日志 / Changelog

本文件记录 **DKStore** 的所有重要版本变更。

All notable changes to **DKStore** are documented in this file.

## [开发中 / Unreleased]

## [v0.7.0] - 2026-11-18

### 新功能
- **按配置更新与详情页操作完善**：统一全站文案为「按配置更新」，为项目模式（`compose.yaml`）与独立容器模式（默认展示当前生效的 `docker run` 命令）提供与上方文本实时双向同步的可视化表单（支持网络、重启策略、运行用户、特权模式、环境变量、端口映射、目录挂载与硬件设备直通）；当个性化配置值发生变动时展示黄色「恢复个性化配置」按钮及 `!` 三分类悬停对比说明（仅还原同名被改值的项并保留当前镜像版本号与新增项，同时提示新增与缺少的配置项）；在详情抽屉顶栏补齐「立即更新」主按钮，并结合容器/镜像标签与内置规则自动识别展示发行说明、源码仓库、官方网站、官方文档及镜像仓库链接 ([`cdaa7ca`](https://github.com/kukushouhou/DKStore/commit/cdaa7ca))
- **版本选择预发布开关与排序搜索优化**：将选择版本弹窗扩宽为左宽右精布局并支持超长版本号完整折行展示（不再截断末尾变体后缀），新增「预发布」开关（当前已运行 `alpha / beta / rc` 等预发布版本或预发布通道时自动默认开启）与「按版本号 / 按更新时间」排序切换，将 `latest` 固定置顶并标注滚动通道对应的具体版本号；打开弹窗后自动在后台同步云端最新标签，点击「搜索」时对所有镜像仓库强制绕过缓存直连云端拉取并按版本前缀优先排列结果，同时打通从预发布版升级到更新预发布版及正式版的检测提醒 ([`7d018ae`](https://github.com/kukushouhou/DKStore/commit/7d018ae), [`7ba454a`](https://github.com/kukushouhou/DKStore/commit/7ba454a))

### 问题修复
- **原配置文件缺失或无权限时的按配置更新**：当项目原 `compose.yaml` 文件已不存在或当前账号无读写权限时（同时补齐 SSH 普通账号读写受限文件时的自动 `sudo` 提权），不再直接报错，自动根据运行中的容器生成等效配置，并提供「按原位置保存」与可视化「选择保存文件夹」弹窗（支持远程与本机目录浏览、安全英文目录新建、按项目名建子文件夹，选定已有 `compose.yaml` 的目录时自动区分同项目、未运行容器与正在运行的容器并禁止覆盖运行中容器的配置，换目录另存时自动将相对挂载路径转为绝对路径） ([`75759b8`](https://github.com/kukushouhou/DKStore/commit/75759b8))
- **应用卡片更新进度排版修复**：将应用卡片的版本号行固定在标题正下方保持同行水平对称，并在执行更新时将底部操作栏直接切换为等高的实时进度、百分比与「查看日志」入口，消除更新过程中卡片增高及同行卡片拉伸错位的问题 ([`8a98302`](https://github.com/kukushouhou/DKStore/commit/8a98302))

---

### Features
- **Update with Config & Details Drawer Actions**: Unified UI wording to `Update with Config`, providing a visual form bidirectionally synced in real time with the top `compose.yaml` (project mode) or reconstructed `docker run` command (standalone mode) covering network, restart policy, user, privileged mode, environment variables, ports, volume mounts, and hardware devices; added a warning-yellow `Restore Custom Values` button (shown only when matching values change) with a 3-category `!` diff popover that restores only modified values while preserving the image tag and newly added items and listing added/missing entries; added a primary `Update Now` button to the details drawer header and enriched `Related Links` (Release Notes, Source Repo, Website, Docs, and Image Registry) from OCI image labels and built-in rules ([`cdaa7ca`](https://github.com/kukushouhou/DKStore/commit/cdaa7ca))
- **Version Picker Pre-release Toggle, Sorting & Search**: Widened the version picker modal with a wider version-list column and full multi-line wrapping for long tags (without truncating trailing variant suffixes), added a `Pre-release` toggle (enabled by default when the container already runs an `alpha / beta / rc` or pre-release channel tag) and a `By Version / By Time` sort switch, and pinned `latest` at the top while showing the concrete version behind rolling channels; opening the modal syncs the latest cloud tags in the background while clicking `Search` bypasses local cache across all registries with prefix-first ranking, and supports seamless update detection from pre-release tags to newer pre-releases and stable releases ([`7d018ae`](https://github.com/kukushouhou/DKStore/commit/7d018ae), [`7ba454a`](https://github.com/kukushouhou/DKStore/commit/7ba454a))

### Fixes
- **Update with Config When Original File Is Missing or Read-Only**: Instead of failing with a file read error when a project's original `compose.yaml` no longer exists or lacks read/write permission (also adding automatic `sudo` elevation fallback for SSH file access), DKStore now reconstructs an equivalent `compose.yaml` from the live containers and offers `Save to Original Location` alongside a visual `Choose Save Folder` picker (supporting remote/local directory browsing, ASCII-safe folder creation, optional project-named subfolder, 3-way existing `compose.yaml` ownership check that blocks overwriting running containers, and automatic conversion of relative mount paths when relocating) ([`75759b8`](https://github.com/kukushouhou/DKStore/commit/75759b8))
- **App Card Update Progress Layout Fix**: Anchored the version row directly below the card header for horizontal alignment across cards in the same row, and switched the bottom action bar in place to an equal-height live progress and `View Log` bar while updating so cards no longer grow taller or distort sibling cards ([`8a98302`](https://github.com/kukushouhou/DKStore/commit/8a98302))

## [v0.6.1] - 2026-11-17

### 问题修复
- **镜像异常挂载兼容与错误提示显示修复**：当上游镜像自带非绝对路径的异常匿名卷（如 `--net=host`）导致重建容器报错时，在确认项目配置自身无误的前提下自动先移除旧容器实例再重新构建，完整保留宿主机挂载目录数据；群晖相关错误提示改为按当前界面语言单语显示（同时兼容已有历史记录），并将应用卡片报错框调整为全宽上下布局（超长内容支持展开与收起），修复单张卡片展开进度或报错时将同行其他卡片等高拉长留白的问题 ([`da34e9e`](https://github.com/kukushouhou/DKStore/commit/da34e9e))

---

### Fixes
- **Invalid Image Volume Mount Compatibility & Error Display Fix**: Automatically removes the old container instance before a clean rebuild when an upstream image carries a non-absolute anonymous volume (such as `--net=host`) and the project YAML itself is valid, preserving host bind-mount data; localized Synology error messages to the active UI language without bilingual duplication (including existing history records), and switched card error alerts to a full-width stacked layout with expand/collapse support while preventing tall cards from stretching sibling cards in the same row ([`da34e9e`](https://github.com/kukushouhou/DKStore/commit/da34e9e))

## [v0.6.0] - 2026-11-17

### 新功能
- **容器启停管理与未运行更新选择**：在容器运行状态页操作列（双行紧凑纯图标布局）与首页应用卡片下拉菜单中新增单个容器、子容器及整项目的启动、停止、重启操作（均附带气泡二次确认，兼容 SSH、本机直连与群晖管理后台连接）；当对未运行的容器或包含未运行容器的项目发起更新、切换版本或按新配置更新时，弹出无默认倾向的确认框供选择「仅更新不启动 / 保持原状态」或「更新并启动」 ([`e047399`](https://github.com/kukushouhou/DKStore/commit/e047399))

### 问题修复
- **更新进度条修复**：移除任务刚启动及获取镜像层信息前硬编码的 `5%` / `10%` 假起步进度，未开始下载前真实保持 `0%`；拉取前从目标镜像清单预读全部层的真实压缩体积并区分下载与解压权重计算总进度，修复前几个小层先下完时总进度条提前跳到 `80%` 卡住的问题，同时让历史记录页实时同步阶段提示与层进度 ([`3c9b5e6`](https://github.com/kukushouhou/DKStore/commit/3c9b5e6))

---

### Features
- **Container Start/Stop/Restart & Stopped-Update Choice**: Added start, stop, and restart actions with popconfirm safeguards for standalone containers, child containers, and whole projects across the Containers list (compact two-line icon actions) and Home card menus (compatible with SSH, local socket, and Synology DSM WebAPI); updating a stopped container or a project with stopped containers now prompts an unbiased choice between `Update Only / Keep Current State` and `Update & Start` ([`e047399`](https://github.com/kukushouhou/DKStore/commit/e047399))

### Fixes
- **Update Progress Bar Fix**: Removed hardcoded `5%` / `10%` initial progress jumps so jobs stay at `0%` before layer downloads actually start; pre-fetched full compressed layer sizes from the target image manifest and separated download vs. extraction weights to stop the overall progress bar from jumping prematurely to `80%` after small early layers complete, while syncing live localized phase and layer stats on the History page ([`3c9b5e6`](https://github.com/kukushouhou/DKStore/commit/3c9b5e6))

## [v0.5.3] - 2026-11-17

### 问题修复
- **历史记录重试按钮修复**：同一容器仅在最新一条记录仍处于失败状态（且当前无正在执行的更新任务）时显示「重试」按钮，已被后续成功或新失败记录取代的旧记录及对应日志弹窗仅保留查看日志；同时修复在历史页查看过往日志会将旧记录重新带回顶栏更新队列的问题 ([`05ed794`](https://github.com/kukushouhou/DKStore/commit/05ed794))

---

### Fixes
- **History Retry Button Fix**: Restricted the `Retry` button so each container only shows it when its newest history record is still `failed` with no active update running, keeping `View Log` only on older superseded records and their log modals; fixed viewing historical logs on `/history` re-inserting old records into the header update queue ([`05ed794`](https://github.com/kukushouhou/DKStore/commit/05ed794))

## [v0.5.2] - 2026-11-17

### 新功能
- **更新失败提示与一键重试**：将下载断流（`EOF`）、连接超时、仓库限流、无访问权限、找不到版本、存储空间不足、端口占用等底层报错统一转为直白易懂的原因说明（同时兼容已有历史记录展示）；在更新队列卡片、日志弹窗顶栏、首页失败提示条与历史记录页新增「重试」主按钮，点击后原地接着已下载进度重新执行更新，并优化超长容器名称与长版本号的单行省略排版 ([`3311c0c`](https://github.com/kukushouhou/DKStore/commit/3311c0c))

### 问题修复
- **镜像下载断流自动续传与更新队列浮层修复**：镜像下载遇到临时网络中断时支持自动续传重试并同行刷新倒计时日志，为远程 SSH 连接补充心跳保活；修复非首页点击顶栏更新队列按钮无法展开浮层及打开日志弹窗时浮层遮挡弹窗的问题 ([`3311c0c`](https://github.com/kukushouhou/DKStore/commit/3311c0c))

---

### Features
- **Update Error Messages & One-Click Retry**: Replaced raw low-level error strings (`EOF`, timeouts, rate limits, auth failures, missing tags, insufficient disk space, and port conflicts) with clear plain-language explanations across live updates and existing history records; added a primary `Retry` button to update queue cards, the log modal header, Home failure alerts, and History cards with single-line ellipsis protection for long container names ([`3311c0c`](https://github.com/kukushouhou/DKStore/commit/3311c0c))

### Fixes
- **Auto-Resume on Image Download Drops & Queue Popover Fix**: Added automatic resume retries on transient image download interruptions, same-line countdown log updates, and SSH keepalive heartbeats; fixed the header update queue popover toggle on non-Home pages and hid the popover cleanly while viewing the log modal ([`3311c0c`](https://github.com/kukushouhou/DKStore/commit/3311c0c))

## [v0.5.1] - 2026-11-16

### 新功能
- **设备 Docker 版本显示**：设备卡片独立展示具体的 Docker 版本号（补齐群晖管理后台免 SSH 连接模式下的真实 Docker 版本读取，并在页面刷新时自动同步最新设备环境信息），当版本低于 `20.10.10` 时附带「版本过旧」提醒说明不支持解压新版镜像的 `zstd` 压缩层 ([`a559f76`](https://github.com/kukushouhou/DKStore/commit/a559f76))

### 问题修复
- **商店更新提醒推送修复**：修复「仅推送提醒」模式在尚未配置通知渠道时提前将新版本标记为已提醒、导致后续补配渠道后漏发推送的问题；保存通知渠道配置后支持即时补推待送达的新版本提醒，并支持自动升级重启后如遇临时网络错误保留通知重试 ([`1348471`](https://github.com/kukushouhou/DKStore/commit/1348471))

---

### Features
- **Device Docker Version Display**: Added a dedicated Docker version row on device cards (including real Docker version detection in SSH-free Synology DSM WebAPI mode and automatic host info sync on refresh), showing an `Outdated` warning badge with a `zstd` layer compatibility hint when the version is older than `20.10.10` ([`a559f76`](https://github.com/kukushouhou/DKStore/commit/a559f76))

### Fixes
- **Store Update Notification Fix**: Fixed `Notify Only` store update mode prematurely marking a new release as notified before any notification channel was configured; saving a notification channel now immediately delivers pending store update reminders, and post-auto-update notifications retry on transient network errors ([`1348471`](https://github.com/kukushouhou/DKStore/commit/1348471))

## [v0.5.0] - 2026-11-15

### 新功能
- **商店更新方式设置**：在初始化向导第 3 步与设置页新增「商店更新(非容器更新)」选项，支持切换「自动更新」（默认，无容器任务运行时自动升级商店并推送通知）、「仅推送提醒」（发现新版本时通过已配通知渠道提醒一次）与「手动更新」（不自动升级、不发通知推送，仅在页面展示待更新状态），并将向导页 GitHub 连通状态改为独立状态徽章展示 ([`3fd7e37`](https://github.com/kukushouhou/DKStore/commit/3fd7e37), [`a333586`](https://github.com/kukushouhou/DKStore/commit/a333586))

### 问题修复
- **自更新兼容与版本号显示**：兼容数据目录挂载带 `noexec` 标记的系统环境（如部分 OpenMediaVault / Linux 数据盘），保留数据盘持久化文件的同时自动拷入可执行目录运行；修复设置页等待远端版本检查返回前当前版本号短暂显示旧占位符的问题 ([`a66a25b`](https://github.com/kukushouhou/DKStore/commit/a66a25b))

---

### Features
- **Store Update Mode**: Added `Store Update (Not Containers)` mode selection to both Setup Wizard Step 3 and Settings, supporting `Auto Update` (default, upgrades DKStore when idle and sends a notification), `Notify Only` (pushes a notification once per new release without auto-upgrading), and `Manual Only` (shows update status in the UI without auto-upgrading or sending push notifications), with GitHub connectivity status displayed as a standalone badge in the wizard ([`3fd7e37`](https://github.com/kukushouhou/DKStore/commit/3fd7e37), [`a333586`](https://github.com/kukushouhou/DKStore/commit/a333586))

### Fixes
- **Self-Update Compatibility & Version Display**: Added automatic fallback execution staging when the data directory resides on a `noexec`-mounted volume while keeping update persistence intact, and fixed Settings briefly showing a stale current-version placeholder before remote update checks complete ([`a66a25b`](https://github.com/kukushouhou/DKStore/commit/a66a25b))

## [v0.4.0] - 2026-11-15

### 新功能
- **DKStore 自更新**：支持在「设置」页直接在线更新自身或上传本地更新包升级（兼容 Docker、群晖、飞牛、OpenWrt、Linux 与 Windows，升级保留全部数据与登录状态，重启容器不退回旧版，启动异常自动回退）；初始化向导与设置页支持配置 GitHub 镜像加速、测速及关联自己的 Fork 仓库 ([`1508ad4`](https://github.com/kukushouhou/DKStore/commit/1508ad4), [`445c9de`](https://github.com/kukushouhou/DKStore/commit/445c9de))
- **容器访问入口与备注标签**：首页卡片与容器列表新增一键打开容器网页入口（自动识别映射端口与 `host` 网络端口），支持为项目或单个容器设置备注名、标签、自定义访问地址与备注，并支持按标签筛选 ([`ce013de`](https://github.com/kukushouhou/DKStore/commit/ce013de))
- **容器列表显示优化**：容器列表改为双行排版并显示真实应用图标，状态与 CPU、内存信息对齐展示 ([`ce013de`](https://github.com/kukushouhou/DKStore/commit/ce013de))

---

### Features
- **DKStore Self-Update**: Upgrade DKStore directly in Settings or by uploading a local package across Docker, Synology, fnOS, OpenWrt, Linux, and Windows while keeping all data and login sessions intact (survives container restarts with automatic rollback on startup failure); supports GitHub mirrors, connectivity testing, and custom Fork repositories ([`1508ad4`](https://github.com/kukushouhou/DKStore/commit/1508ad4), [`445c9de`](https://github.com/kukushouhou/DKStore/commit/445c9de))
- **Container Web Links, Notes & Tags**: Added one-click container web links on Home cards and the Containers list, with per-project and per-container aliases, tags, custom URLs, notes, and tag filtering ([`ce013de`](https://github.com/kukushouhou/DKStore/commit/ce013de))
- **Container List Display Improvements**: Updated the Containers list with real app icons and a cleaner two-line layout ([`ce013de`](https://github.com/kukushouhou/DKStore/commit/ce013de))

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
