# TODOs

## Restore container healthcheck after Podman migration

**What:** Add a `-healthcheck` subcommand to the `testserver` Go binary and wire it
into container runs via `podman run --health-cmd`. Include a Go test for the subcommand.

**Why:** The original compose healthcheck (`docker-compose.yml`) ran `wget` inside the
container, but the base image is `gcr.io/distroless/static` — no shell, no wget — so it
could never pass. The Podman migration deletes `docker-compose.yml`, which removes the
(broken) healthcheck entirely, leaving **no health signal** for the container. A
`-healthcheck` subcommand on the Go binary is the clean fix: the binary is the only
executable present in a distroless image, so it can health-check itself by hitting its
own play-by-play endpoint.

**Where to start:**
- `cmd/testserver/main.go` — add a `-healthcheck` flag that does a localhost GET against
  `PLAYBYPLAY_PORT` (e.g. `/v1/gamecenter/test/play-by-play`) and exits 0/1.
- `Dockerfile` or the Makefile `run`/`up` targets — add `--health-cmd` /
  `--health-interval` to `podman run`.
- Add a Go test asserting the subcommand exits 0 when the server responds and non-zero
  otherwise.

**Depends on / blocked by:** ~~The Podman migration~~ — migration has landed (Makefile + compose deleted). This TODO is now unblocked.

## Add JSON log handler when a log ingester exists

**What:** Re-add a `LOG_FORMAT=json` env option that swaps the `slog.TextHandler`
for `slog.NewJSONHandler` in `cmd/testserver/main.go`.

**Why:** The decision-trace logging (branch `NelsonBlakeN/add-decision-logging`)
ships text-only because the sole consumer is human substring-grep in the Aptakube
GUI, where JSON is harder to read (YAGNI). The day logs feed a structured ingester
(Loki, ELK, Datadog, etc.), JSON becomes worth it — machine-parseable per-event
attributes instead of `key=value` text.

**Where to start:**
- `cmd/testserver/main.go` — where `LOG_LEVEL` is already parsed and the root
  `slog.TextHandler` is built; add a `LOG_FORMAT` branch (`text` default | `json`),
  same invalid-value-warn-and-fallback pattern as `LOG_LEVEL`.
- Add an env-parse test mirroring the `LOG_LEVEL` one.

**Pros:** One-line handler swap; slog makes text↔json trivial. **Cons:** No
consumer today — deferred deliberately, not forgotten.

**Depends on / blocked by:** A log-ingestion consumer actually existing. Until
then this stays deferred.

<!-- Removed 2026-06-21: "Scenario content library for shifted game IDs (Approach C)"
     superseded by the time-aware replay-proxy design — the emulator now serves real
     fetched per-game play-by-play + MoneyPuck data, so every game already has a
     distinct realistic narrative. Hand-curated fixtures are no longer needed.
     See design: blakenelson-NelsonBlakeN-test-games-by-date-season-design-20260621-202325.md -->
