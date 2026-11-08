# DKStore — Home Docker App Update Center

**English** | [简体中文](./README_zh-CN.md)

> [!WARNING]
> **Status: Work in Progress (Pre-Alpha)**
> DKStore is currently under active Phase-1 development and internal testing. No stable release is available yet — please do not use it in production environments.

DKStore is an app-store-like update center designed for home NAS, routers, and Linux servers running Docker. Install once on any device in your home network to check updates across all your Docker hosts and NAS devices, without replacing existing managers such as Synology Container Manager.

## Key Principles

- **Update-first, not ops-first**: Clean app-store style view grouped into *Available Updates*, *Caution Updates* (major version bumps & databases), and *Ignored*.
- **Install once, manage everywhere**: Agentless remote management via SSH / native platform APIs.
- **Coexist with native managers**: Read-only discovery in Phase 1; preserves Compose projects and native NAS manager metadata.
- **Bilingual UI**: Built-in English (`en-US`) and Simplified Chinese (`zh-CN`) interface.

## Development

Requires Go 1.27+ and Node 24+.

```bash
# Backend (dev mode)
go run ./cmd/dkstore -dev -log-level debug

# Frontend
cd web
npm install
npm run dev
```
