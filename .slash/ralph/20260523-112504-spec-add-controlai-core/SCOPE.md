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
