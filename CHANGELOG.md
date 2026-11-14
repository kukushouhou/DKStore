# 更新日志 / Changelog

本文件记录 **DKStore** 的所有重要版本变更。

All notable changes to **DKStore** are documented in this file.

## [开发中 / Unreleased]

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
