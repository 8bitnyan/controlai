# Learnings — 20260523-112504-spec-add-controlai-core


_2026-05-23T02:50:52.208Z_

## [driver] iteration 1 agent timeout

Session: `ses_1ad58c6a9ffereuEbDzGrtGPw7`
Model: `anthropic/claude-sonnet-4-6`
Consecutive agent-failure count: 1/3
Detail: Ralph session did not finish within 1500s



_2026-05-23T02:50:52.305Z_

## [driver] ambiguous-evidence (after previous turn)

- task **1.2**: evidence overlaps with task(s) [3.1] on files: internal/audit/audit.go, internal/config/config_test.go, internal/config/site.go, .... Marking deferred.
- task **3.1**: evidence overlaps with task(s) [1.2] on files: internal/config/site.go, internal/config/tenant.go. Marking deferred.
- task **4.1**: evidence overlaps with task(s) [4.2, 4.4, 4.5, 4.7, 8.3, 4.6, 4.3] on files: internal/render/render.go, internal/render/templates/shared/docker-compose.yml.tmpl, internal/render/templates/shared/traefik/dynamic/site-mqtt.yml.tmpl, .... Marking deferred.
- task **4.2**: evidence overlaps with task(s) [4.1, 4.4, 4.5] on files: internal/render/templates/shared/docker-compose.yml.tmpl, internal/render/templates/shared/traefik/static.yml.tmpl. Marking deferred.
- task **4.3**: evidence overlaps with task(s) [4.1, 4.4, 4.5] on files: internal/render/templates/tenant/tsdb/docker-compose.yml.tmpl, internal/render/templates/tenant/tsdb/init.sql.tmpl. Marking deferred.
- task **4.4**: evidence overlaps with task(s) [4.1, 4.2, 4.5, 4.6, 4.3] on files: internal/render/templates/shared/docker-compose.yml.tmpl, internal/render/templates/site/emqx/docker-compose.yml.tmpl, internal/render/templates/site/ingest/docker-compose.yml.tmpl, .... Marking deferred.
- task **4.5**: evidence overlaps with task(s) [4.1, 4.2, 4.4, 4.6, 4.3] on files: internal/render/templates/shared/docker-compose.yml.tmpl, internal/render/templates/site/emqx/docker-compose.yml.tmpl, internal/render/templates/site/ingest/docker-compose.yml.tmpl, .... Marking deferred.
- task **4.6**: evidence overlaps with task(s) [4.1, 4.4, 4.5] on files: internal/render/templates/site/ingest/docker-compose.yml.tmpl. Marking deferred.
- task **4.7**: evidence overlaps with task(s) [4.1, 8.3] on files: internal/render/templates/shared/traefik/dynamic/site-mqtt.yml.tmpl. Marking deferred.
- task **8.3**: evidence overlaps with task(s) [4.1, 4.7] on files: internal/render/templates/shared/traefik/dynamic/site-mqtt.yml.tmpl. Marking deferred.

