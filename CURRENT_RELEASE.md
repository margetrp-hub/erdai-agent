# CURRENT_RELEASE - public release manifest

```yaml
current_release: erdai-agent:stable
runtime_version: 0.15.29
schema_version: 85
source_repository: https://github.com/margetrp-hub/erdai-agent
source_tag: v0.15.29
deployment: docker-compose
acceptance_level: central points regression, Go vet, WebUI build and isolated runtime checks; deployment evidence is recorded separately
stable_updates: GitHub Releases only
```

This file is intentionally limited to public source and release metadata. Host
names, addresses, credentials, database locations, image digests, backups and
rollback evidence belong in the private operator record and must not be added
to the repository.
