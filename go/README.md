# 二呆智能体 Core

This directory contains the single Go implementation for runtime ownership and the embedded administration UI.

- Port `6280` is the authenticated runtime API and native connector event surface.
- Port `6282` is the administration UI and management API. API access requires a browser session or the explicit server-side administrator token; network reachability alone grants no authority.
- Persona, worldbook, RAG, routing, tools, MCP, memory, media quotas, Runs and Outbox use the same Core-owned SQLite data.
- Native Go connectors receive platform events and deliver Outbox messages in this process.

The canonical image, Compose file and release scripts live in the parent directory. The target production topology runs one scratch-based `erdai-agent` container with one Go process. Previous images and data snapshots are retained for rollback until the real QQ cutover gates pass.

Run `go test ./...` and `go vet ./...` from this directory. QQ connector intake, text delivery and rich-media delivery are covered by Go tests.
