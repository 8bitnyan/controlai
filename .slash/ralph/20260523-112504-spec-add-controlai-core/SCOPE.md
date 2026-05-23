# SCOPE — 20260523-112504-spec-add-controlai-core

_Cross-iteration memory. Last 3 turns only. ralph-agent reads this in Step 1.5._
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

<!-- iter 5 -->
## iter 5
### what landed:
- 15 file(s): .slash/ralph/20260523-112504-spec-add-controlai-core/SCOPE.md, .slash/ralph/20260523-112504-spec-add-controlai-core/iterations/iter-04.json, .slash/ralph/20260523-112504-spec-add-controlai-core/iterations/iter-04.verdict.json, …(+12)
- +2035 / -20 lines @ 8182285d (agent=completed)
### lens hint:
tasks.md unchecked items; spec acceptance criteria; edge cases of the touched surface
### missing (from verifier):
- Task 8.5: Integration test for MQTT SNI routing (bring up shared + 2 sites on *.controlai.local, assert MQTT connections route to correct broker via SNI; still marked [ ] in tasks.md)
- Task 13.1: End-to-end integration test (mosquitto/low/uni + EMQX/mid/bi, telemetry rows published and stored, bi-mode downlink works, broker swap succeeds, retention change applies; still marked [ ] in tasks.md)
- Task 12.2: controlai install command implementation (deploy/install.sh script is present but the `controlai install` CLI command that invokes it is missing from main.go)
- Task 12.4: README runbook (partially done—has Install, First Tenant, First Site, Retention Change, Broker Swap, Capacity Check, Backup, Uninstall; missing verification of reconciler convergence after manual docker compose down scenario)

<!-- iter 6 -->
## iter 6
### what landed:
- 14 file(s): .slash/ralph/20260523-112504-spec-add-controlai-core/SCOPE.md, .slash/ralph/20260523-112504-spec-add-controlai-core/iterations/iter-05.json, .slash/ralph/20260523-112504-spec-add-controlai-core/iterations/iter-05.verdict.json, …(+11)
- +1112 / -54 lines @ 12d6c0e2 (agent=timeout)
### lens hint:
tasks.md unchecked items; spec acceptance criteria; edge cases of the touched surface
### missing (from verifier):
- Task 13.1: End-to-end integration test (must validate full provision→apply→modify→downlink→broker-swap→retention-change cycle with mosquitto/low/uni + EMQX/mid/bi topology, telemetry rows published/stored, bi-mode downlink working, broker swap succeeds, retention policy change applies)
- Task 12.2 checkbox: controlai install command is fully implemented in main.go (lines 764-860) but tasks.md still shows [ ] instead of [x]
- Task 12.4 checkbox: README runbook is comprehensive (Install, First Tenant, First Site, Retention Change, Broker Swap, Reconciler Auto-Convergence, Capacity Check, Backup, Uninstall sections) but tasks.md still shows [ ] instead of [x]
- Tasks 13.2-13.5: Verification tasks remaining (openspec validate --strict, capacity guard 3-tenant test, reconciler 30s convergence after docker compose down, manual cleanup test)
