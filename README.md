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
- **Example**: `http://localhost:8125/v1/schedule/2026-06-29`
- **Response**: `{"gameWeek":[{"date":"2026-06-29","games":[...]}]}` — one-element `gameWeek` array containing that day's games
- **Date range**: Day 1 is June 29; games run through a hard cutoff of **September 30** (the embedded data continues into January, but dates after Sept 30 serve no games). Replay is year-agnostic — the same slate appears for `2026-06-29`, `2027-06-29`, etc.
- **Out-of-range dates**: returns `{"gameWeek":[]}` (empty), matching the real API's off-day behaviour

### Play-by-Play API (Port 8125)
- **Endpoint**: `GET /v1/gamecenter/{gameId}/play-by-play`
- **Example**: `http://localhost:8125/v1/gamecenter/2025020001/play-by-play`
- **Response**: plays that would have occurred before the wall-clock moment the request arrived, fetched from the real NHL API on first access and sliced by the shifted game clock

### Statistics API (Port 8124)
- **Endpoint**: `GET /moneypuck/gameData/20252026/{gameId}.csv`
- **Example**: `http://localhost:8124/moneypuck/gameData/20252026/2025020001.csv`
- **Response**: the last MoneyPuck row whose elapsed-game-seconds ≤ current game position; zeroed row if the game has not started

## Configuration

Environment variables (default values are baked into the image):
- `PLAYBYPLAY_PORT`: Port for the play-by-play server (default: 8125)
- `STATS_PORT`: Port for the statistics server (default: 8124)

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
3. On every subsequent request, computes how far into the game the current wall-clock is (using the game's shifted `startTimeUTC` as the anchor) and returns only the plays and stats that would have occurred by that moment.

**Pacing model:** each 20-minute period takes ~38 minutes of wall-clock time (accounting for stoppages), with 18-minute intermissions between periods. A regulation game spans approximately 2.5 hours.

**Eviction:** once the backend receives the terminal `game-end` play and makes its final MoneyPuck request, the emulator evicts both caches and installs a short-lived tombstone so re-polls don't trigger unnecessary upstream fetches.

**Data currency:** game IDs in the shifted schedule (e.g. `2025020001`) are real 2025-26 IDs that resolve to completed games at both upstreams.

**Season window (year-agnostic):** Day 1 is June 29 and the season runs through a hard cutoff of **September 30** — requests after Sept 30 return an empty `gameWeek` even though the embedded data physically continues into January. The whole season is anchored by month-day, not absolute year, so the emulator replays the same slate in whatever year it runs: `2026-06-29`, `2027-06-29`, `2031-06-29` all return Day 1. The split between "this season" and "next season" is the start of June (dates in Jan-May belong to the prior June's season). Play-by-play and stats anchor each game's start time to the current year's instance, so slicing works no matter the year.

## Integration

### Pointing the backend at the emulator (summer end-to-end testing)

1. Start the test server: `make up`
2. Set `PLAYBYPLAY_API_BASE_URL` in the backend's environment to the emulator's address. This single env var routes both the schedule fetcher and the play-by-play/stats fetchers to the emulator:

```bash
PLAYBYPLAY_API_BASE_URL=http://localhost:8125
```

The backend's `Scheduler.Run(ctx, "2026-06-29")` will then:
1. Call `GET http://localhost:8125/v1/schedule/2026-06-29` → receives the shifted Day 1 games
2. Enqueue Cloud Tasks for each game (all have `GameState: "FUT"` as required)
3. Poll `GET http://localhost:8125/v1/gamecenter/{gameId}/play-by-play` as each game progresses

No backend code changes are required.

### Rebuilding the shifted schedule data

The schedule is baked into the binary via `go:embed`. To regenerate it (e.g. with a different start date):

```bash
# With Go installed:
go run ./cmd/buildschedule \
  [-day1 2025-10-07] [-target-day1 2026-06-29] \
  [-base-url https://api-web.nhle.com] \
  [-raw-dir data/raw] \
  [-out internal/services/data/season_2025-26_shifted.json]

# Without Go (Node.js):
node ./cmd/buildschedule/generate.js [--day1 2025-10-07] [--target-day1 2026-06-29]
```

Both write to `internal/services/data/season_2025-26_shifted.json`. Raw weekly responses are cached under `data/raw/` (`-raw-dir` to override) so a failed fetch can be resumed without re-hitting the NHL API.

## Deployment

The emulator ships as `ghcr.io/firepowerapp/gamedataemulator` via the `Build and Push Docker Image` workflow on every merge to `main`.

A `Deploy to Kubernetes` workflow then auto-deploys to staging (`firepower-staging` namespace). Production deployment is always a manual `workflow_dispatch`. Both environments use Kustomize overlays under `k8s/overlays/{staging,production}`.

**Required secrets** (org-level, already available to all FirepowerApp repos):
- `TS_CLIENT_ID` / `TS_CLIENT_SECRET` — Tailscale OAuth for cluster access
- `KUBECONFIG` — kubeconfig for the target cluster

To test deployment manifests from a feature branch before merging, run the `Deploy to Kubernetes` workflow manually with `environment=staging` from that branch.

See [`k8s/README.md`](k8s/README.md) for namespace bootstrap, required secrets, and troubleshooting.

## Development

The replay engine is in `internal/gamereplay/` (Pacing, Source, Cache, Slicer) — see [`internal/gamereplay/README.md`](internal/gamereplay/README.md) for the architecture, the eviction state machine, and how to change the pacing model. To change pacing constants (stretch factor, intermission length, OT timing), edit `internal/gamereplay/pacing.go`. To point the fetcher at a different upstream, pass `gamereplay.NewSourceWithBaseURLs(nhlBase, mpBase)` in tests.

To rebuild the shifted schedule (e.g. to change the season start date):
1. Run `go run ./cmd/buildschedule [-target-day1 YYYY-MM-DD]`
2. Rebuild the image: `make build`
3. Restart: `make down && make up`
