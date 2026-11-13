# Changelog / 更新日志

All notable changes to **DKStore** will be documented in this file.
本项目的所有重要变更均会以中英双语记录在此文件中。

---

## [Unreleased]

### 新功能 / Features
- **原生多平台发行包生态**：完整支持群晖 DSM 7 套件（`.spk`）、飞牛 fnOS 原生应用（`.fpk`）与 OpenWrt / iStoreOS LuCI 原生插件（`.ipk` / `.apk` / `.run`），内置宿主登录态免二次密码直进与本机容器自动接入 (`2af488f`, `934d007`, `adf4d2b`)
- **Native Multi-Platform Packages**: Added native installers for Synology DSM 7 (`.spk`), fnOS App Center (`.fpk`), and OpenWrt / iStoreOS LuCI (`.ipk` / `.apk` / `.run`) with seamless host admin session reuse and automatic local Docker discovery (`2af488f`, `934d007`, `adf4d2b`)
- **一处安装，多端无 Agent 管理**：支持本机 Unix Socket、群晖免 SSH 的 DSM WebAPI（含 2FA 动态码与设备令牌持久化）及标准 SSH（Socket 转发 / `dial-stdio` / `sudo` 提权）统一管理多台 NAS、软路由与 Linux 服务器 (`a84bdd3`, `34b414a`, `dc34423`)
- **Install Once, Manage Everywhere**: Agentless management across local Docker sockets, SSH-free Synology DSM WebAPI (with 2FA OTP & persistent device tokens), and standard SSH (`streamlocal` / `dial-stdio` / `sudo`) (`a84bdd3`, `34b414a`, `dc34423`)
- **单服务精准升级、分层进度与有条件回退**：支持执行前冲突校验、镜像分层流式进度展示、实时只读 `xterm` 更新日志、多段版本号前缀锁定、YAML/独立容器配置修改更新，以及基于远端 Digest 核验的安全回退 (`a5833e4`, `21a3b21`, `d2855be`)
- **Safe Service-Level Upgrades & Conditional Rollback**: Pre-flight conflict checks, real-time per-layer pull progress, live `xterm` log viewer, multi-segment version prefix locking, in-place YAML/container config editing, and verified digest-safe rollbacks (`a5833e4`, `21a3b21`, `d2855be`)
- **容器运行监控与清理能力**：提供树状项目/容器实时资源监控视图，并支持独立容器与整项目的可选镜像/目录清理移除 (`27349c0`, `14b6c2b`, `986e1fd`)
- **Runtime Container Tree & App Removal**: Added hierarchical project/container runtime monitor and safe container/project removal with optional image and directory cleanup (`27349c0`, `14b6c2b`, `986e1fd`)

### 性能优化 / Performance
- **容器监控页本地快照秒开**：将设备容器运行态快照持久化至本地 SQLite、复用远程连接池并改为 `one-shot` 并发采样，消除冷启动白屏等待并拆分独立的「所属设备」与「当前镜像」列 (`02fd42d`, `09f47ab`)
- **Instant Container View via Local Snapshots**: Persisted host runtime snapshots in SQLite with pooled connections and concurrent `one-shot` stats sampling for instant cold loads, plus dedicated Device and Current Image columns (`02fd42d`, `09f47ab`)

### 问题修复 / Fixes
- **认证与仓库兼容性增强**：主动探测过期会话并在 `401` 时自动跳转、保留飞牛 `docker.fnnas.com` 镜像加速源前缀、优化版本选择器 SemVer 倒序排列与变体过滤 (`2600492`, `08f614e`, `782a01c`)
- **Auth & Registry Refinements**: Proactive expired-session redirection on `401`, preserved `docker.fnnas.com` mirror prefixes on tag upgrades, and improved SemVer sorting/filtering in the version picker (`2600492`, `08f614e`, `782a01c`)
