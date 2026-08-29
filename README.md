# 二呆智能体

二呆智能体的群内昵称是“豆包”。项目统一提供渠道接入、扩展运行、持久状态、能力发现、可解释路由、角色与知识资产、模型与工具执行，以及生产观测；只有一套后台和配置源。

完整的中文系统说明、配置入口、API、运行链路、测试口径和未完成边界见 [`SYSTEM_GUIDE.md`](./SYSTEM_GUIDE.md)。

## Architecture

- One Stable release bundle owns the complete production application: the `erdai-agent` Core image plus pinned local embedding and private monitor-browser images.
- One Go process owns the control plane, agent loop, durable state, platform connections, Outbox delivery and WebUI.
- Platform catalog entries have native Go connectors for authentication, inbound normalization, Core ownership, Outbox delivery, media and health state. The production Go runtime does not require a Python or AstrBot process.
- Two listeners in the same Go process: runtime/API on `6280`, authenticated local management UI on `6282`; the screenshot browser has no published host port.
- SQLite WAL with FTS5 trigram search, durable runs, media quota accounting and Outbox delivery state.
- Provider credentials are read from environment variables or the data-volume managed credential file; they are never stored in SQLite or returned by management APIs.
- Provider connections own their protocol, API base, credential reference, timeout and active health samples. Model endpoints bind to a connection, so chat, task, group-decision and media routes can fail over across real suppliers without sharing one global URL or key.
- Runs persist routing choice, model calls and stage timing from event intake through connector ACK. The management UI exposes provider tests, health history and run timelines without returning credentials.
- Media availability is based on real task outcomes and generated artifacts, not endpoint probes alone. Knowledge retrieval, embedding queries and memory recall emit count-only observability events without storing raw user queries.
- Automatic learning remains review-gated and accepts a candidate only when at least two independent, topic-relevant web sources pass the URL quality gate.

## Included

- Schema for execution endpoints and health, personas and worldbook entries, knowledge documents and FTS, tools, MCP servers, runs, deliveries and audit events.
- A capability catalog plus persisted, editable routing lane profiles.
- Endpoint execution kinds (`llm`, `tool`, `media`) and adapter references keep model switching separate from tool invocation and media jobs.
- Deterministic routing: hard capability, health freshness, context, latency and cost filters followed by a visible quality/reliability/latency/cost/priority score. Automatic mode returns ordered fallbacks; manual mode strictly enforces lane locks.
- Clean-room SillyTavern Character Card V2 conversion. Only known text and worldbook fields are selected; unknown extensions are discarded and never executed.
- Appearance libraries are independent visual assets that role cards select by reference; one library can be shared by multiple roles without copying files or prompts.
- Namespace-scoped persona, worldbook and knowledge CRUD, content-hash deduplication, and Chinese retrieval previews through FTS5 trigram. Enabled worldbook entries participate in live context through constant, primary-key and selective secondary-key matching.
- Health observation and routing-control APIs used by the cockpit.
- Privacy-minimized shadow routing records for comparing compatibility behavior with Core decisions without storing message text or platform identifiers.
- A same-origin responsive cockpit for overview, endpoint metadata, health, automatic/manual routing, editable lane profiles, decision explanations, capabilities, Persona/runtime policy editing, administrator directives, knowledge/RAG management and reviewed learning candidates.
- Four persistent WebUI themes shared by the login screen and authenticated cockpit: native, standard, anime and wasteland industrial. Theme assets are embedded in the Core image and do not require an external CDN.
- Secret-redacted management payloads. Unknown fields, including credential fields, are rejected.
- Unified secret-redacted settings for the 18-platform catalog, provider policy and message behavior. Every catalog type resolves to a native Go connector; secrets remain server-side and connector health errors are redacted.
- Runtime-owned conversation style settings for concise but complete replies, approximate length guidance and repeated-opener avoidance. These settings are editable in the Go console and compiled above the Persona.
- An explicit configuration-layer contract for new operators: `public_config` and `public_policy` define Core-wide limits and defaults; `role_config` and `role_policy` define a character; `instance_config` and `instance_policy` isolate one account/channel. Read the live contract from `GET /api/v1/config/layers` or start with [`examples/README.md`](./examples/README.md).
- A privacy-minimized channel contract with idempotent event intake, durable delivery leasing, acknowledgement, retry and cancellation. Transient text, display names and signed attachment URLs are validated but never persisted.
- A Core-owned Agent loop for model calls, Grok search, image/video jobs, OPS, memory and allow-listed Streamable HTTP MCP tools. Progress and terminal messages are written to Outbox before the Go connector manager delivers them.
- A scheduled Grok learning worker turns configured topics into source-linked, reviewable knowledge candidates. It never promotes candidates or edits Persona/rules without administrator review.
- Configurable per-user daily image/video quotas with administrator overrides and auditable usage.
- A configurable Core content boundary classifies explicit sexual requests, real-world harm, severe abuse and ordinary provocation before model execution. Each category can use model/persona handling, refusal, a short non-escalating counter or silent ignore; ignored messages do not enter conversation context.

## Run

```powershell
# Run from a clean checkout. The script builds and tests locally, then emits
# an import-only bundle for a linux/amd64 VPS.
sh ./scripts/build-release.sh <release> <schema> <output-dir>
```

For source tests, build the WebUI first with `npm ci && npm run build` in `go/webui/`, then run `go test ./...` and `go vet ./...` from `go/`. The clean-checkout path is `docker build --target verify .`; it builds the WebUI inside Docker. The Compose example listens on `6280` and `6282`; persistent state is mounted at the operator-selected data directory.

An active production instance uses two distinct service tokens of at least 32 characters. `ERDAI_ADMIN_TOKEN` protects the management listener and remains the automation fallback; human login can additionally use the single `ERDAI_ADMIN_USERNAME` plus `ERDAI_ADMIN_PASSWORD` (or its SHA-256 form). Browser login exchanges valid credentials for an eight-hour HttpOnly, SameSite session, while automation uses `X-Erdai-Admin-Token`. Session-authenticated writes also require a same-origin request. `ERDAI_RUNTIME_TOKEN` is accepted as `Authorization: Bearer ...` only for runtime preparation, shadow observations, model-health observations, and transport event/delivery/cancellation calls. A fresh install may leave `ERDAI_RUNTIME_TOKEN` and `ERDAI_MODEL_API_KEY` empty: the authenticated cockpit starts in `setup_required`, rejects runtime bearer requests, and lets the operator fill the business credentials before activation. Tokens and passwords are read from the process environment or the managed data-volume file and are never stored in the Core database or served to the cockpit. The cockpit reports configuration readiness and checks GitHub Releases for Stable versions; upgrade execution remains a host-side operation with backup and health-gated rollback.

The cockpit is served from `/` by the same process, so the browser and API remain same-origin. The Integrations → Credentials tab writes only the allow-listed provider/platform references to the data-volume `managed-credentials.env` and keeps one `.bak` before replacement. Database encryption roots, administrator tokens and listener/database paths remain host-only and cannot be changed from the UI.

Stable upgrades use a host-side executor. `scripts/build-release.sh` emits `erdai-agent-stable-<release>.tar.gz`; the Stable tag workflow runs the full verify target and uploads that archive to GitHub Releases. Run the bundled `install-update-agent.sh` once on the host. The cockpit only selects a Stable asset, writes an authenticated request into the data volume, and displays the executor state. The host agent validates the repository-specific GitHub URL, request freshness, asset size/digest, archive paths, embedded `SHA256SUMS`, and delegates to the existing backup/health-gated `deploy-250.sh` rollback path.

## API

```text
GET  /healthz
GET  /
GET  /api/v1/overview
GET  /api/v1/observability
GET  /api/v1/installation/status
GET  /api/v1/update/check
GET  /api/v1/update/status
POST /api/v1/update/request
GET  /api/v1/credentials
PUT/DELETE /api/v1/credentials/:name
GET  /api/v1/config/layers
GET  /api/v1/capabilities
GET  /api/v1/model-endpoints
PUT  /api/v1/model-endpoints/:id
DELETE /api/v1/model-endpoints/:id
GET  /api/v1/provider-connections
PUT  /api/v1/provider-connections/:id
DELETE /api/v1/provider-connections/:id
POST /api/v1/provider-connections/:id/test
POST /api/v1/provider-connections/:id/pricing-sync
GET  /api/v1/model-health/:id/history
GET  /api/v1/runs/:id
GET  /api/v1/integrations
GET/PUT /api/v1/integrations/:id
GET  /api/v1/model-health
PUT  /api/v1/model-health/:id
GET  /api/v1/routing/control
PUT  /api/v1/routing/control
POST /api/v1/routing/simulate
GET/POST       /api/v1/personas
GET/PUT/DELETE /api/v1/personas/:id
GET/POST       /api/v1/appearance-libraries
GET/PUT/DELETE /api/v1/appearance-libraries/:id
GET/POST       /api/v1/appearance-libraries/:id/references
GET/PUT/DELETE /api/v1/appearance-libraries/:id/references/:referenceId
GET/PUT        /api/v1/personas/:id/appearance-library
GET/POST       /api/v1/personas/:id/worldbook
GET/PUT/DELETE /api/v1/personas/:id/worldbook/:entryId
GET/POST       /api/v1/knowledge/documents
GET/PUT/DELETE /api/v1/knowledge/documents/:id
POST           /api/v1/knowledge/search-preview
GET/PUT        /api/v1/routing/lanes
GET/PUT        /api/v1/runtime/config
GET/POST       /api/v1/runtime/directives
GET/PUT/DELETE /api/v1/runtime/directives/:id
GET/POST       /api/v1/runtime/knowledge-candidates
GET/PUT/DELETE /api/v1/runtime/knowledge-candidates/:id
POST           /api/v1/runtime/knowledge-candidates/:id/review
POST           /api/v1/runtime/prepare
GET/PUT/DELETE /api/v1/runtime/media-quotas
POST           /api/v1/mcp/servers/:id/discover
POST           /api/v1/mcp/servers/:id/call
POST           /api/v1/transport/events
POST           /api/v1/transport/deliveries/lease
POST           /api/v1/transport/deliveries/:id/ack
POST           /api/v1/transport/deliveries/:id/fail
POST           /api/v1/runs/:id/cancel
GET  /api/v1/shadow/interactions
POST /api/v1/shadow/interactions
```

Example simulation body:

```json
{
  "lane": "tools",
  "requiredCapabilities": ["vision"],
  "preferredCapabilities": ["reasoning"],
  "minimumContextTokens": 32000,
  "maximumLatencyMs": 5000,
  "maximumBlendedCostPerMillion": 20,
  "maxHealthAgeMs": 300000
}
```

Simulation does not create a run or mutate health. With `maxHealthAgeMs`, recorded health older than the limit is rejected with an explicit reason. Endpoints with no health record remain eligible at a lower score. Provider credentials are intentionally absent from the schema; the management API stores only references while the managed credential file keeps the actual values on the data volume.

## Boundaries

Core is the source of truth for Persona, protected rules, administrator directives, RAG, memory, routing, platform metadata, provider policy, message policy, quotas and channel policy. It does not store provider credentials or raw platform administrator IDs. Ordinary group members cannot mutate configuration or promote learned material; automatic learning only creates reviewable candidates.

The Go Runtime owns event acceptance, the model/tool loop and connector delivery. It only returns `owned` when the channel policy permits it, persists the Run and outbound progress/terminal deliveries, then leases and acknowledges them through the connector manager. `off` rejects takeover and `shadow` observes without owning the message. Streamable HTTP, legacy SSE and controlled stdio MCP execution enforce enablement, allow-lists, sender authority, approval mode, timeout, private-network blocking, bounded responses and audit logging.

The legacy Node/AstrBot runtime and the old static WebUI have been removed from the active checkout and final image. The final image contains one Go process and the built React WebUI. Production upgrades still require a database backup, real QQ `@豆包` canary, restart/redelivery check and rollback verification.

Each release must pass the applicable Go suite, `go vet`, protocol checks, migration checks and a health-gated active rollout. The production verifier also checks the management credential path with a temporary allow-listed secret, confirms no value is returned, and removes the temporary record even on failure. QQ Official requires the group-message intent and reconnect check; real-account canaries for other platforms remain separate acceptance gates. The release bundle records its source revision and schema in a public manifest; private operator records retain deployment evidence. 豆包是用于跑通通用多角色流程的首张角色卡，不是 Core 中写死的唯一角色。

## Clean-room design references

The project studies external agent and role-play systems without treating them as drop-in runtimes:

- `pi-rp` (MIT): tool lifecycle, prompt slots and observable state.
- `talk` (MIT): group participation signals and evidence-backed structured memory.
- `Luker` (AGPL-3.0): worldbook and durable generation-job concepts only; no source is copied.
- `Liyuan` (PolyForm Noncommercial): context retention and MCP permission concepts only; no source is copied.
- `SleepTavernHome` (unclear AFPL-derived terms): role-state and context-plan concepts only; no source or role content is copied.
- `DeterminFlow` (AGPL-3.0): immutable workflow snapshots, checkpoints, node-level tool permissions, attempt history and token/cost ledgers are useful future reliability patterns. No source or dependency is copied into this project.

The implemented clean-room slices include group participation policy, evidence-linked memory, durable task/Outbox delivery and MCP execution. Future additions must remain Core-owned, configurable, audited and default-deny for group members.
## Release

Stable releases are built from a clean checkout through the Docker `verify` and `build` targets. The image embeds the WebUI and contains no Node, Python or AstrBot runtime. Schema and public release metadata live in [`CURRENT_RELEASE.md`](./CURRENT_RELEASE.md); host-specific backup, rollout and rollback evidence stays in the private operator record.
