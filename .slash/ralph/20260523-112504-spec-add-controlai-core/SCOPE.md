# SCOPE — 20260523-112504-spec-add-controlai-core

_Cross-iteration memory. Last 3 turns only. ralph-agent reads this in Step 1.5._
<!-- iter 1 -->
## iter 1
### what landed:
- 49 file(s): .github/workflows/ci.yml, .gitignore, .slash/ralph/20260523-112504-spec-add-controlai-core/STATUS, …(+46)
- +4962 / -0 lines @ c57b465e (agent=timeout)
### lens hint:
tasks.md unchecked items; spec acceptance criteria; edge cases of the touched surface
### missing (from verifier):
- Task 6.5: handlePublish() incomplete — returns stub instead of forwarding to ingest downlink endpoint
- Task 9.5: handleLogs() incomplete — returns NotImplemented instead of streaming docker compose logs
- Unit tests for config, capacity, pki, render, store, ingest modules (tasks 2.4, 4.9, 5.7, 6.9, 7.6)
- Integration tests validating end-to-end provision → apply → modify cycle (task 13.1)
- tasks.md not updated to reflect actual implementation state (many items marked [ ] are actually implemented)
- No verification that code compiles and basic endpoints respond correctly

<!-- iter 2 -->
## iter 2
### what landed:
- 26 file(s): .slash/ralph/20260523-112504-spec-add-controlai-core/SCOPE.md, .slash/ralph/20260523-112504-spec-add-controlai-core/iterations/iter-01.json, .slash/ralph/20260523-112504-spec-add-controlai-core/iterations/iter-01.verdict.json, …(+23)
- +1874 / -233 lines @ e2bad59a (agent=completed)
### lens hint:
tasks.md unchecked items; spec acceptance criteria; edge cases of the touched surface
### missing (from verifier):
- Task 9.8: Unit tests for daemon API handlers (GET/POST/DELETE handlers for tenants, sites, health, capacity, publish, logs, apply)
- Task 6.8: Dockerfile for controlai-ingest (multi-stage, distroless base image)
- Task 6.9: Unit tests for ingest codec, batcher, downlink MQTT path; integration test with real MQTT + Postgres
- Task 7.6: Unit tests for reconciler backoff state machine; integration tests for provision → apply → modify → restart cycle
- Task 8.1, 8.3, 8.5: Shared init command, reconciler wiring for traefik dynamic files, and integration test for MQTT SNI routing
- Task 13.1: End-to-end integration test validating entire provision → apply → modify → downlink publish cycle
