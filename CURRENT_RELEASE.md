# CURRENT_RELEASE - public release manifest

```yaml
current_release: erdai-agent:stable
runtime_version: 0.12.3
schema_version: 74
source_repository: https://github.com/margetrp-hub/erdai-agent
source_tag: v0.12.3
deployment: docker-compose
acceptance_level: full Go tests, go vet, WebUI production build, clean-database container smoke test
stable_updates: GitHub Releases only
```

This file is intentionally limited to public source and release metadata. Host
names, addresses, credentials, database locations, image digests, backups and
rollback evidence belong in the private operator record and must not be added
to the repository.
