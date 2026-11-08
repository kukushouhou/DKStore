# DKStore — 家庭 Docker 应用更新中心

> [!WARNING]
> **当前状态：早期开发中（Work in Progress / Pre-Alpha）**
> 本项目正在进行第一阶段开发与内部测试，尚未发布可用版本，请勿用于生产或日常环境。

面向家庭 NAS / Linux 主机的 Docker 应用更新提醒与管理中心。

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
