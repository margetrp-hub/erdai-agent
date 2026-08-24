# CURRENT_RELEASE — 二呆智能体当前生产口径(机器可读)

```yaml
current_release: erdai-agent:themes-20260824-r13
runtime_version: 0.11.1
schema_version: 73
source_repository: https://github.com/margetrp-hub/erdai-agent (private)
source_tag: v0.11.1
built_at: 2026-08-24T14:02:36+08:00
built_from: D:\wiki\lab\repos\doubao-agent-core 工作树(ERDAI_SOURCE_REVISION=ui-themes-20260824-schema73)
host: OVH vps-5c84ed97-vps-ovh-us (Tailscale 100.116.21.100)
container: erdai-agent (healthy, RestartCount=0, OOMKilled=false)
started_at: 2026-08-24T14:34:42+08:00
data_db: /opt/erdai-agent/data/erdai-agent-core.sqlite3 (quick_check=ok)
legacy_db: /opt/erdai-agent/data/erdai-runtime.sqlite3
rollback_images:
  - erdai-agent:observability-20260824-r12
rollback_backup: /opt/erdai-agent/backups/20260824T062524Z-pre-themes-r13 (SQLite quick_check=ok, SHA256SUMS=ok)
release_manifest_sha256: 07b3a54f3243ddf822c0f33b1e080216e98ac17a32fa7f7572fdec07e859ef55
app_tar_sha256: dc57e277ab258ee6a61066ea63cd60c58c583c94e252b38e4b6d8605ed1889a2
images_tar_sha256: 41968bf880bafd0393333bf66c3e5457ac8a6ad56920d6e70fc8e1e96eff24b6
core_image_id: sha256:20f41feb5e61d65c4bbf5506324658c3e4d29dba9d64dcb608d4dd26af28769e
source_revision: ui-themes-20260824-schema73
verified_at: 2026-08-24T14:37:20+08:00
acceptance_level: 前端构建、聚焦 Go 嵌入资源测试、生产管理员 API/SQLite/容器回读、QQ connector 状态、真实管理员 Playwright 四主题桌面切换与 390px 移动零溢出;完整 Go 回归未运行;未发送真实 QQ 测试消息
cleanup_verified_at: 2026-08-24T15:11:00+08:00 (root 65% -> 40%; current r13 + direct r12 rollback + one verified database backup retained)
```

维护规则:每次发布/回滚后更新本文件;`GO_MIGRATION.md` 顶部口径必须与本文件一致。发现两处不一致时,以最近一次现场回读为准,先修文档再动线上。
