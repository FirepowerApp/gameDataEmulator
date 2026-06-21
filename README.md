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
- **Example**: `http://localhost:8125/v1/schedule/2026-06-22`
- **Response**: `{"gameWeek":[{"date":"2026-06-22","games":[...]}]}` — one-element `gameWeek` array containing that day's games
- **Date range**: 2026-06-22 (shifted Day 1) through 2026-12-30 (end of shifted 2025-26 regular season)
- **Out-of-range dates**: returns `{"gameWeek":[]}` (empty), matching the real API's off-day behaviour

### Play-by-Play API (Port 8125)
- **Endpoint**: `GET /v1/gamecenter/{gameId}/play-by-play`
- **Example**: `http://localhost:8125/v1/gamecenter/2024030411/play-by-play`

### Statistics API (Port 8124)
- **Endpoint**: `GET /moneypuck/gameData/20242025/{gameId}.csv`
- **Example**: `http://localhost:8124/moneypuck/gameData/20242025/2024030411.csv`

## Configuration

Environment variables (default values are baked into the image):
- `PLAYBYPLAY_PORT`: Port for the play-by-play server (default: 8125)
- `STATS_PORT`: Port for the statistics server (default: 8124)

Override ports at run time:

```bash
make run STATS_PORT=9000 PBP_PORT=9001
```

## Test Data

### Game Events (halts at game-end)
1. faceoff
2. shot-on-goal
3. blocked-shot
4. missed-shot
5. goal
6. hit
7. period-end
8. giveaway
9. penalty
10. game-end

### Predefined Game Statistics (homeTeamExpectedGoals / awayTeamExpectedGoals)
- Game `2024030411`: Home 2.35, Away 1.87 — 3-2 final
- Game `2024030412`: Home 3.12, Away 2.94 — 2-2 regulation, home wins 2-1 in shootout
- Game `2024030413`: Home 1.95, Away 2.68 — 4-3 final
- Game `2024030414`: playoff fixture, no `maxPeriods` field
- Other games: Default xG Home 2.50, Away 2.50

## Integration

### Pointing the backend at the emulator (summer end-to-end testing)

1. Start the test server: `make up`
2. Set `PLAYBYPLAY_API_BASE_URL` in the backend's environment to the emulator's address. This single env var routes both the schedule fetcher and the play-by-play/stats fetchers to the emulator:

```bash
PLAYBYPLAY_API_BASE_URL=http://localhost:8125
```

The backend's `Scheduler.Run(ctx, "2026-06-22")` will then:
1. Call `GET http://localhost:8125/v1/schedule/2026-06-22` → receives the shifted Day 1 games
2. Enqueue Cloud Tasks for each game (all have `GameState: "FUT"` as required)
3. Poll `GET http://localhost:8125/v1/gamecenter/{gameId}/play-by-play` as each game progresses

No backend code changes are required.

### Rebuilding the shifted schedule data

The schedule is baked into the binary via `go:embed`. To regenerate it (e.g. with a different start date):

```bash
# With Go installed:
go run ./cmd/buildschedule \
  [-day1 2025-10-07] [-target-day1 2026-06-22] \
  [-base-url https://api-web.nhle.com] \
  [-raw-dir data/raw] \
  [-out internal/services/data/season_2025-26_shifted.json]

# Without Go (Node.js):
node ./cmd/buildschedule/generate.js [--day1 2025-10-07] [--target-day1 2026-06-22]
```

Both write to `internal/services/data/season_2025-26_shifted.json`. Raw weekly responses are cached under `data/raw/` (`-raw-dir` to override) so a failed fetch can be resumed without re-hitting the NHL API.

## Development

To modify the test data:
1. Edit `internal/services/testdata.go`
2. Rebuild the image: `make build`
3. Restart the container: `make down && make up`
