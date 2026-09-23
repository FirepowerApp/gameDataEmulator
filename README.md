# Test Server

This is a standalone test server that simulates the NHL API and MoneyPuck API for
testing purposes. It provides the same functionality as the embedded test servers but
runs independently in its own container (OCI-compatible — built and run with Podman).

## Prerequisites

- [Podman Desktop](https://podman-desktop.io/) for container builds and runs
- Go 1.23+ for running locally without a container

## Quick Start

### Using Make (Recommended)

```bash
make build   # build the local image
make up      # start detached (ports 8124 + 8125)
make logs    # follow logs
make down    # stop and remove the container
```

`make up` depends on `make machine`, which starts the Podman VM on macOS automatically.

### Running Locally (no container)

```bash
make dev
# or: go run ./cmd/testserver
```

### Pulling the published image

```bash
make pull                               # pull ghcr.io/firepowerapp/gamedataemulator:latest
make up RUN_IMAGE=ghcr.io/firepowerapp/gamedataemulator:latest
```

## Makefile targets

| Target        | Description                                                    |
|---------------|----------------------------------------------------------------|
| `make machine`| Start the Podman VM (macOS; no-op on Linux)                    |
| `make build`  | Build `gamedataemulator:local` from the Dockerfile             |
| `make run`    | Run in the foreground (Ctrl-C to stop)                         |
| `make up`     | Run detached                                                   |
| `make down`   | Stop and remove the detached container                         |
| `make logs`   | Follow logs from the detached container                        |
| `make pull`   | Pull the published image from ghcr.io                          |
| `make clean`  | Stop container and remove the local image                      |
| `make dev`    | `go run ./cmd/testserver` — fastest inner loop, no container   |
| `make test`   | `go test ./...`                                                |
| `make cover`  | Test with coverage report                                      |

Set `ENGINE=docker` to use Docker instead of Podman: `make build ENGINE=docker`.

## API Endpoints

### Schedule API (Port 8125)
- **Endpoint**: `GET /v1/schedule/{date}` — same shape as `api-web.nhle.com/v1/schedule/{date}`
- **Example**: `http://localhost:8125/v1/schedule/{today}` (any offseason date)
- **Response**: `{"gameWeek":[{"date":"{today}","games":[...]}]}` — one-element `gameWeek` array containing that day's rebased games
- **Date range**: from the first offseason day (the anchor, from the NHL API — see below) through the preseason, each day serves the next saved game-day. Once the regular season starts, or the saved game-days run out, it serves none. Year-agnostic — the anchor follows the real calendar every year.
- **Out-of-range dates**: returns `{"gameWeek":[]}` (empty), matching the real API's off-day behaviour

### Play-by-Play API (Port 8125)
- **Endpoint**: `GET /v1/gamecenter/{gameId}/play-by-play`
- **Example**: `http://localhost:8125/v1/gamecenter/2025020001/play-by-play`
- **Response**: plays that would have occurred before the wall-clock moment the request arrived, fetched from the real NHL API on first access and sliced by the rebased game clock

### Statistics API (Port 8124)
- **Endpoint**: `GET /moneypuck/gameData/20252026/{gameId}.csv`
- **Example**: `http://localhost:8124/moneypuck/gameData/20252026/2025020001.csv`
- **Response**: while the game is live, the last MoneyPuck row whose elapsed-game-seconds ≤ current game position; once the game is over, the true final row, including any shootout result (MoneyPuck timestamps shootout rows past regulation+OT); zeroed row if the game has not started

## Configuration

Environment variables (default values are baked into the image):
- `PLAYBYPLAY_PORT`: Port for the play-by-play server (default: 8125)
- `STATS_PORT`: Port for the statistics server (default: 8124)
- `LOG_LEVEL`: Minimum log level — `debug`, `info`, `warn`, or `error` (default: `info`). Invalid values warn and fall back to `info`.

Override ports at run time:

```bash
make run STATS_PORT=9000 PBP_PORT=9001
```

**Runtime egress:** the emulator fetches real game data from `api-web.nhle.com` (NHL play-by-play) and `moneypuck.com` (per-event statistics) on first request for each game. Both are cached in memory for the game's lifetime. The container needs outbound HTTPS access to these hosts. The distroless image ships with CA certificates so TLS works out of the box.

## How the replay works

The emulator serves **real, time-sliced data** from the completed 2025-26 NHL season, not synthetic fixtures.

When the backend first requests a game, the emulator:
1. Fetches the full final play-by-play from `api-web.nhle.com` and the per-event MoneyPuck CSV from `moneypuck.com`.
2. Caches both in memory.
3. On every subsequent request, computes how far into the game the current wall-clock is (using the game's rebased `startTimeUTC` as the anchor) and returns only the plays and stats that would have occurred by that moment.

**Pacing model:** each 20-minute period takes ~38 minutes of wall-clock time (accounting for stoppages), with 18-minute intermissions between periods. A regulation game spans approximately 2.5 hours.

**Eviction:** once the backend receives the terminal `game-end` play and makes its final MoneyPuck request, the emulator evicts both caches and installs a short-lived tombstone so re-polls don't trigger unnecessary upstream fetches.

**Data currency:** game IDs in the saved schedule (e.g. `2025020001`) are real 2025-26 IDs that resolve to completed games at both upstreams.

**Season window (stack of days, year-agnostic):** the embedded season is a dense stack of every game-day of the 2025-26 season — regular season and playoffs (no off-days; preseason games are left out because MoneyPuck has no data for them, and the non-NHL Olympic-break games too). From the first day of the offseason through the end of the preseason, offseason day 0 serves the first saved game-day, day 1 the second, and so on; once the NHL regular season starts the emulator serves no games. Both decisions come from the NHL schedule API alone: the anchor (day 0) is the day after the previous season's `playoffEndDate` (found by walking `previousStartDate` back to the season that just ended), and the cutoff is the API's `regularSeasonStartDate`. The state is fetched lazily and cached for an hour, so the emulator starts at any time of year (it makes no network call at startup) and tracks the real calendar with no hardcoded dates. If the NHL API can't be reached it keeps using the last state it got; with none at all it answers `/v1/schedule/{date}` with 503 rather than guess.

## Integration

### Pointing the backend at the emulator (summer end-to-end testing)

1. Start the test server: `make up`
2. Set `PLAYBYPLAY_API_BASE_URL` in the backend's environment to the emulator's address. This single env var routes both the schedule fetcher and the play-by-play/stats fetchers to the emulator:

```bash
PLAYBYPLAY_API_BASE_URL=http://localhost:8125
```

The backend's `Scheduler.Run(ctx, today)` will then, on any offseason day:
1. Call `GET http://localhost:8125/v1/schedule/{today}` → receives that offseason day's game-day, rebased onto today's date
2. Enqueue Cloud Tasks for each game (all have `GameState: "FUT"` as required)
3. Poll `GET http://localhost:8125/v1/gamecenter/{gameId}/play-by-play` as each game progresses

No backend code changes are required.

### Rebuilding the canonical schedule data

The schedule is baked into the binary via `go:embed`, in real (unshifted) calendar dates — the emulator rebases it onto the requesting date at runtime. To regenerate it (e.g. with a different source season):

```bash
# With Go installed:
go run ./cmd/buildschedule \
  [-day1 2025-10-07] \
  [-base-url https://api-web.nhle.com] \
  [-raw-dir data/raw] \
  [-out internal/services/data/season_2025-26.json]

# Without Go (Node.js):
node ./cmd/buildschedule/generate.js [--day1 2025-10-07]
```

Both write to `internal/services/data/season_2025-26.json` and produce byte-identical output. Raw weekly responses are cached under `data/raw/` (`-raw-dir` to override) so a failed fetch can be resumed without re-hitting the NHL API.

## Deployment

The emulator ships as `ghcr.io/firepowerapp/gamedataemulator` via the `Build and Push Docker Image` workflow on every merge to `main`.

A `Deploy to Kubernetes` workflow then auto-deploys to staging (`firepower-staging` namespace). Production deployment is always a manual `workflow_dispatch`. Both environments use Kustomize overlays under `k8s/overlays/{staging,production}`.

**Required secrets** (org-level, already available to all FirepowerApp repos):
- `TS_CLIENT_ID` / `TS_CLIENT_SECRET` — Tailscale OAuth for cluster access
- `KUBECONFIG` — kubeconfig for the target cluster

To test deployment manifests from a feature branch before merging, run the `Deploy to Kubernetes` workflow manually with `environment=staging` from that branch.

See [`k8s/README.md`](k8s/README.md) for namespace bootstrap, required secrets, and troubleshooting.

## Development

The replay engine is in `internal/gamereplay/` (Pacing, Source, Cache, Slicer) — see [`internal/gamereplay/README.md`](internal/gamereplay/README.md) for the architecture, the eviction state machine, and how to change the pacing model. To change pacing constants (stretch factor, intermission length, OT timing), edit `internal/gamereplay/pacing.go`. To point the fetcher at a different upstream, pass `gamereplay.NewSourceWithBaseURLs(nhlBase, mpBase, logger)` in tests.

To rebuild the canonical schedule (e.g. to use a different source season):
1. Run `go run ./cmd/buildschedule [-day1 YYYY-MM-DD]`
2. Rebuild the image: `make build`
3. Restart: `make down && make up`
