# DKStore — 家庭 Docker 应用更新中心

[English](./README.md) | **简体中文**

> [!WARNING]
> **当前状态：早期开发中（Work in Progress / Pre-Alpha）**
> 本项目正在进行第一阶段开发与内部测试，尚未发布可用版本，请勿用于生产或日常环境。

DKStore 是面向家庭 NAS、软路由与 Linux 主机的 Docker 应用更新中心。只需在家中任意一台设备安装一次，即可像手机应用商店一样统一查看多台设备上的 Docker 应用更新，并与群晖 Container Manager 等原生管理器无感并存。

## 核心特点

- **更新优先，而非运维优先**：围绕“可更新 / 需谨慎更新（跨大版本与数据库）/ 已忽略”三组卡片展示，告别复杂运维参数表。
- **一处安装，多端管理**：基于 SSH 与平台原生接口的无 Agent 远程接入。
- **与原生管理器并存**：第一阶段严格只读检测，保留 Compose 项目与 NAS 原生项目身份。
- **中英双语界面**：内置简体中文（`zh-CN`）与英文（`en-US`）完整国际化支持。

## 开发构建

需要 Go 1.27+、Node 24+。

```powershell
# 后端（开发模式）
go run ./cmd/dkstore -dev -log-level debug

# 前端
cd web
npm install
npm run dev
```
