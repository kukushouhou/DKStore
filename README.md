# DKStore — Home Docker App Update Center

**English** | [简体中文](./README_zh-CN.md)

> [!WARNING]
> **Status: Work in Progress (Pre-Alpha)**
> DKStore is currently under active development and internal testing. No stable release is available yet — please do not use it in production environments.

DKStore is an app-store-like update center designed for home NAS, routers, and Linux servers running Docker. Install once on any device in your home network to discover, check, safely upgrade, and conditionally roll back Docker applications across all your Docker hosts and Synology NAS devices — without replacing or breaking native managers such as Synology Container Manager.

## Key Capabilities

- **Update-First Experience**: Clean app-store style view grouped into **Available Updates**, **Caution Updates** (major version bumps, database services, and breaking-change rules), and **Ignored** (`this_version` or `forever`).
- **Install Once, Manage Everywhere**: Agentless remote management via SSH (`docker.sock` forwarding, `sudo -n`, or passwordless `sudo -S` over SSH) and local Unix socket.
- **Native Coexistence with Synology DSM & Compose**:
  - Automatically discovers native Synology Container Manager projects (`SYNO.Docker.Project`), standard Docker Compose projects (`docker-compose.yml` + `.env`), and standalone containers.
  - Identifies platform Package Center containers (`vendor_managed`) as read-only so system packages are never damaged.
- **Safe One-Click Update & Single-Service Isolation**:
  - Pre-check conflict detection (verifies container `ImageID` and Compose YAML SHA-256 before modifying).
  - Real-time streaming layer download progress via SSE (`POST /api/v1/updates`).
  - Single-service Compose/DSM upgrades (`docker compose up -d --no-deps <service>`) that preserve YAML comments/formatting and **never restart or touch sibling database containers**.
  - Standalone container recreation that preserves name, ports, volumes, env vars, all attached networks (`Aliases` & `IPAMConfig`), labels, and **original running/stopped state** (stopped containers stay stopped), with automatic fallback to the previous container if startup fails.
- **Update History & Conditional Rollback**:
  - Records every upgrade and rollback job with before/after digests, matched tags, and execution logs.
  - Dynamically verifies rollback eligibility (`GET /api/v1/updates/{id}/rollback-check`) against remote registry digests and local image cache before allowing one-click rollback (`POST /api/v1/updates/{id}/rollback`).
- **Full-Stack Internationalization (`zh-CN` & `en-US`)**: Localized Web UI, backend diagnostics, error messages, and push notifications (Webhook, WeCom, Bark, Telegram, ntfy).

## Packaging & Deployment

- **Single Binary / Docker Container**: Embedded React + Ant Design frontend (`cmd/dkstore`), zero external runtime dependencies (pure-Go SQLite).
- **Synology DSM 7 SPK Scaffold**: [`packaging/synology/`](./packaging/synology)
- **OpenWrt / iStoreOS IPK & LuCI Scaffold**: [`packaging/openwrt/`](./packaging/openwrt)
- **Linux systemd Service**: [`packaging/linux/dkstore.service`](./packaging/linux/dkstore.service)

## Development

Requires Go 1.24+ and Node 20+.

```bash
# Build frontend
cd web && npm install && npm run build && cd ..

# Run backend
go run ./cmd/dkstore --listen :8090 --data ./data

# Run unit tests
go test -v ./...
```
