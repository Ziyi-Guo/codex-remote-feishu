# Configurable model settings implementation plan

**Goal:** Change Remote model defaults and message presets through config without rebuilding or restarting the service.
**Architecture:** Keep `codex.defaultModel/defaultReasoningEffort` and add `codex.modelPresets`. Admin saves and explicit file apply share validation and publish an in-memory snapshot. Running and queued requests retain resolved values; topic overrides remain concrete. Fixed API profiles and VS Code retain existing default precedence.
**Tech stack:** Go daemon/CLI, JSON config, React admin, Go tests and Vitest.

## Approved behavior

- `codex-remote config check [--config PATH]` validates the file without writing or contacting the daemon.
- `codex-remote config apply [--config PATH]` asks the local daemon to load its own configured file. A supplied path must match the daemon's configured path, avoiding accidental application to another instance. Only Codex model settings are applied; unrelated config changes require their existing workflows.
- Apply is explicit; no filesystem watcher or per-message disk reads.
- Missing `modelPresets` preserves legacy presets; an explicit map replaces them (empty disables prefixes). Each preset has `model` and `reasoningEffort`; alias keys are constrained, and model/effort are validated together.
- Configuration publication is atomic under the daemon lock. Persistence failures and known unsupported models/efforts leave the previous runtime selection unchanged. Manually edited files remain on disk if apply fails, so responses distinguish persisted candidate from applied state.
- Online native/OAuth catalogs provide model suggestions and preflight checks. Unavailable/incomplete catalogs produce `unverified`, never a false unsupported claim. Model availability is not a successful generation guarantee.
- CLI runtime upgrades remain separate from model configuration and are never triggered by config apply.

## Tasks and ownership

- [x] Config + orchestrator: implement presets schema, normalization/validation, parser integration, concrete queued/topic freezing; test legacy compatibility, changed aliases, disabled aliases and queue stability.
- [x] Daemon + CLI: implement catalog projection, shared availability validation, apply endpoint, command routing and errors; test invalid files, unavailable catalogs, known incompatibility, successful atomic apply, path mismatch and non-restart behavior.
- [x] Admin UI: use runtime catalog suggestions and supported efforts; report unverified status and save failures; remove model-version literals.
- [x] Documentation: update config usage and canonical remote precedence/apply semantics.
- [x] Verification: focused Go tests and admin Vitest, relevant Go integration suite, Web build, `scripts/check/pre-commit.sh`; inspect diff and review fixes.
- [ ] Delivery: commit only this worktree, publish via `safe-push.sh`, open PR against the fork's `local/bridge-customizations` base (existing deployed feature combination), verify remote head and PR URL. No live deployment.

## Verification record

- `go test ./...`: passed; final catalog intersection/unknown-status changes rechecked with focused daemon tests.
- Admin focused Vitest: 20 passed; Web production build passed.
- CLI `config check` exercised against an existing configuration without mutations.
- Independent reviews found candidate/validation inconsistency and unknown/unsupported effort UI edges; fixed with regression tests.
- Remote-state audit: only future parsing reads aliases; accepted queue records keep concrete overrides; explicit apply is the only post-start file reload path for model settings. No new route or gate lifecycle introduced.
- Pre-commit guardrails passed; existing nonblocking test-path warnings remain outside this change.
