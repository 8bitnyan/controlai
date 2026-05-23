# SCOPE — 20260523-112504-spec-add-controlai-core

_Cross-iteration memory. Last 3 turns only. ralph-agent reads this in Step 1.5._
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

<!-- iter 3 -->
## iter 3
### what landed:
- 16 file(s): .slash/ralph/20260523-112504-spec-add-controlai-core/SCOPE.md, .slash/ralph/20260523-112504-spec-add-controlai-core/iterations/iter-02.json, .slash/ralph/20260523-112504-spec-add-controlai-core/iterations/iter-02.verdict.json, …(+13)
- +970 / -44 lines @ 658d0df0 (agent=completed)
### lens hint:
tasks.md unchecked items; spec acceptance criteria; edge cases of the touched surface
### missing (from verifier):
- Task 6.9: Integration test for ingest (missing `// +build integration` test with real MQTT + Postgres batcher flush validation)
- Task 7.6: Integration tests for reconciler provision → apply → modify → restart cycle (missing test with actual docker compose state changes)
- Task 8.1: Reachability verification missing (shared init command must verify :443 + :8883 are reachable before returning success)
- Task 9.8: Integration test missing (daemon handlers must be tested against a live daemon, not just httptest.Server)
- Task 8.5: Integration test for MQTT SNI routing (still marked [ ])
- Task 13.1: End-to-end integration test validating entire provision → apply → modify → downlink publish cycle (still marked [ ])

<!-- iter 4 -->
## iter 4
### what landed:
- 11 file(s): .gitignore, .slash/ralph/20260523-112504-spec-add-controlai-core/SCOPE.md, .slash/ralph/20260523-112504-spec-add-controlai-core/iterations/iter-03.json, …(+8)
- +1159 / -5 lines @ e00d3a49 (agent=completed)
### lens hint:
tasks.md unchecked items; spec acceptance criteria; edge cases of the touched surface
### missing (from verifier):
- Task 8.1: Port reachability verification missing (shared init command prints :443 + :8883 listening but does not verify they are actually reachable before returning success)
- Task 6.9: Integration test for ingest missing (no `// +build integration` test with real MQTT + Postgres batcher flush validation)
- Task 7.6: Integration tests for reconciler missing (no `// +build integration` test for provision → apply → modify → restart cycle with actual docker compose state changes)
- Task 9.8: Integration test for daemon handlers missing (only httptest.Server unit tests; no test against a live daemon instance)
- Task 8.5: Integration test for MQTT SNI routing not implemented (still marked [ ] in tasks.md)
- Task 13.1: End-to-end integration test not implemented (still marked [ ]; should validate entire provision → apply → modify → downlink publish cycle)
- Task 10.5: scripts/measure-rss.sh and CI assertion test not implemented (still marked [ ])
