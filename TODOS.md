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

**Depends on / blocked by:** The Podman migration (Makefile + delete compose) landing
first. Tracked in the design doc:
`~/.gstack/projects/FirepowerApp-gameDataEmulator/blakenelson-NelsonBlakeN-migrate-to-podman-design-20260615-175707.md`
(Open Question 1, option a).

---

## Scenario content library for shifted game IDs (Approach C)

**What:** Build a per-game-ID play-by-play fixture library keyed to the shifted game IDs from `season_2025-26_shifted.json`. Instead of every unknown game ID cycling through the same 10-event default sequence, individual games could carry distinct fixtures (e.g. a tight 1-0 defensive game, a high-scoring overtime thriller, a shootout) that mirror realistic NHL game narratives.

**Why:** The current default fixture is intentionally generic (useful for smoke-testing), but for end-to-end integration runs spanning a full shifted season, every game looks identical. A richer fixture library makes the emulator more useful for testing Firepower's notification logic across varied game outcomes.

**Approach:**
- Add a `fixtures/` directory under `internal/services/` with one JSON file per scenario (e.g. `tight_1-0.json`, `ot_thriller.json`, `shootout.json`).
- Extend `NewTestPlayByPlayServer()` to load and register fixtures by shifted game ID from a seed mapping (could be a YAML or Go map literal).
- Keep the 10-event default as the fallback for unmapped IDs.

**Where to start:**
- `internal/services/testdata.go` — extend `gameEvents` map population; add a `loadFixtures()` helper.
- `internal/services/fixtures/` — new directory for per-scenario JSON fixtures.
- `internal/services/testdata_test.go` — extend `TestPlayByPlayHaltsAtGameEnd` to cover a fixture-mapped shifted game ID.

**Depends on:** `season_2025-26_shifted.json` being stable (already committed).
