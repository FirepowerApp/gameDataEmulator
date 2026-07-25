# gamereplay

Time-aware replay of completed NHL game data. Given a game's shifted start time and the current wall-clock, this package decides which plays and statistics "have happened so far" and returns only those.

It exists because the emulator serves a *shifted* season: the real 2025-26 games are complete, but the emulator presents them as if they are happening now, on a calendar offset into the future. `gamereplay` is the engine that turns a finished game into a live-looking feed.

---

## How it fits together

```
                          ┌─────────────────────────────────────────┐
   HTTP handlers          │              gamereplay                  │
  (internal/services)     │                                          │
                          │   ┌─────────┐      ┌──────────────┐      │
  HandlePlayByPlay ──────────►│  Cache  │◄────►│   Source     │──────────► api-web.nhle.com
  HandleStats      ──────────►│ (shared)│      │ (http/fake)  │──────────► moneypuck.com
                          │   └────┬────┘      └──────────────┘      │
                          │        │                                 │
   StartTimeProvider ─────────► Position() ──► GamePosition          │
   (ScheduleServer)       │        │                                 │
                          │   ┌────▼────┐                            │
                          │   │ Slicer  │  SlicePBP / SliceMP        │
                          │   └─────────┘                            │
                          └─────────────────────────────────────────┘
```

The handler asks the schedule for a game's shifted `startTimeUTC`, calls `Position(start, now)` to find where the game clock is, then asks the `Cache` for the data (which fetches from `Source` on a miss) and the `Slicer` trims it to the current position.

---

## Reference

### `Position(start, now time.Time) GamePosition`

Pure function. Maps wall-clock to game-clock using the realistic pacing model.

```go
pos := gamereplay.Position(startTimeUTC, time.Now())
```

`GamePosition` fields:

| Field | Type | Meaning |
|-------|------|---------|
| `Period` | `int` | `0` = pre-game, `1-3` = regulation, `4` = OT, `5` = shootout / ended |
| `GameSecs` | `int` | Elapsed play-clock seconds (0–3900). Frozen during intermissions. |
| `InIntermission` | `bool` | True when wall-clock is between periods |
| `Ended` | `bool` | True once the game is fully complete |

Pacing constants (in `pacing.go`): each 20-minute period stretches to ~38 minutes of wall-clock (1.9×, accounting for stoppages); two 18-minute intermissions; OT is a 1-minute intermission then up to 5 minutes of play at the same stretch; shootout is instantaneous. A regulation game spans ~2.5 hours.

### `Source` interface

```go
type Source interface {
	FetchPlayByPlay(ctx context.Context, gameID string) ([]models.Play, error)
	FetchMoneyPuck(ctx context.Context, gameID string) ([]MPRow, error)
}
```

Constructors:

- `NewSource(logger *slog.Logger)` — production. Fetches from `https://api-web.nhle.com` and `https://moneypuck.com`, 10-second timeout, sends a non-blank `User-Agent` (required — MoneyPuck's Cloudflare edge returns a license page to blank-UA requests).
- `NewSourceWithBaseURLs(nhlBase, mpBase string, logger *slog.Logger)` — same client, overridable base URLs. Used by tests to point at an `httptest.Server`.

The MoneyPuck CSV is parsed **by header name**, not column position, so an upstream column reorder cannot silently corrupt values. A missing required column returns an error (surfaced as a 5xx by the handler).

### `Cache`

```go
cache := gamereplay.NewCache(src, logger)             // production
cache := gamereplay.NewCacheForTest(src, clk, logger) // injectable clock for tests
```

One `Cache` is shared between the play-by-play and stats handlers so eviction coordinates across both feeds. Methods:

- `GetPBP(ctx, gameID, pos) ([]models.Play, error)`
- `GetMP(ctx, gameID, pos) (string, MPRow, error)` — returns the CSV body and the last structured row (for score logging without re-parsing).

### `Slicer`

Pure functions:

- `SlicePBP(plays, pos) []models.Play` — keeps plays at or before `pos`, in upstream order.
- `SliceMP(rows, pos) string` — returns the CSV header plus the single last row whose `time` ≤ `pos.GameSecs`; a zeroed row pre-game. When `pos.Ended`, returns the true final row (greatest `time`) instead, so a shootout result — which MoneyPuck timestamps past regulation+OT — is included rather than dropped.
- `LastMPRow(rows, pos) (MPRow, bool)` — returns the same row as `SliceMP` but as a structured `MPRow`, for callers that need score values without re-parsing CSV. Returns `false` if no row qualifies.

### Logging helpers

Used by HTTP handlers for structured log fields:

- `FormatClock(pos GamePosition) string` — returns a `"MM:SS"` string for periods 1-4 (OT); empty string for pre-game (period 0) and shootout/ended (period 5).
- `StateLabel(pos GamePosition) string` — returns `"pregame"`, `"live"`, or `"over"` for coarse state logging.

Log attribute keys are defined in `log.go`: `LogKeyGame = "game"`, `LogKeyFeed = "feed"` (`"pbp"` or `"stats"`), `LogKeyUpstream = "upstream"` (resolved upstream ID for synthetic duplicate game IDs).

### `StartTimeProvider`

```go
type StartTimeProvider interface {
	StartTime(gameID string) (time.Time, bool)
}
```

Defined here (not in `services`) so `services` can import `gamereplay` without an import cycle. `ScheduleServer` satisfies it implicitly.

---

## Why time-sliced replay (explanation)

**The problem.** The backend's notification logic must be exercised against a full season during the off-season, when no real games are being played. Hand-written fixtures don't capture the cadence, scoring patterns, or edge cases (overtime, shootouts, comebacks) of real hockey — and maintaining 1,312 of them is hopeless.

**The approach.** The 2025-26 season is complete, so the real upstreams return the *final* state of every game. The emulator fetches that final data once, caches it, and replays it against a shifted clock: at wall-clock T, return everything that would have happened by T. The backend sees the exact data progression it will see in production, because it *is* production data, just time-shifted.

**The eviction state machine.** A game's cache entry moves through three states:

```
   ACTIVE ──(PBP slice reaches "game-end")──► ENDED ──(next MoneyPuck request)──► evicted
                                                                                     │
                                                                          tombstone (~1h)
                                                                                     │
                                                                              entry removed
```

This mirrors how the backend consumes the feeds: it polls play-by-play until it sees `game-end`, then makes one final MoneyPuck request for closing stats, then sends its notification. The emulator evicts only after that final stats read, so the backend always gets complete data. A short tombstone prevents a re-poll from triggering a wasteful re-fetch, and a lazy sweep drops entries for games abandoned before they ended.

**Trade-offs.**
- *Runtime network dependency.* The emulator needs outbound HTTPS the first time each game is requested (cached thereafter). This weakens the "works fully offline" property the embedded schedule has. A disk write-through cache (deferred) would restore it.
- *Concurrent cold-start double-fetch.* Two simultaneous first requests for the same game may both hit upstream. We accept this (no singleflight dependency) because the data is identical and the map write is mutex-guarded — last-write-wins is harmless.
- *Pacing is approximate.* The 1.9× stretch is uniform within a period; real stoppages cluster. Fine unless the backend is sensitive to sub-minute timing.

---

## How to change the pacing model

Edit the constants at the top of `pacing.go` (`periodWallSecs`, `intermissionSecs`, `otGameSecs`, etc.), then run the table-driven tests:

```bash
go test ./internal/gamereplay/ -run TestPosition -v
```

The test cases assert the period/intermission/OT boundaries, so they will tell you immediately if a constant change shifts a boundary unexpectedly.

## How to test against fake upstream data

Inject a fake `Source` and a fixed clock — no network required:

```go
src := &fakeSource{plays: ..., mpRows: ...}     // implements gamereplay.Source
clk := func() time.Time { return fixedMoment }
cache := gamereplay.NewCacheForTest(src, clk, nil) // nil logger falls back to slog.Default()
```

See `cache_test.go` and `source_test.go` for working examples. To exercise the real HTTP `Source` against a local server, use `NewSourceWithBaseURLs(srv.URL, srv.URL, nil)`.

---

## Related

- [`../services`](../services) — the HTTP handlers that drive this engine
- [`../../README.md`](../../README.md) — the "How the replay works" section and API reference
- [`../models`](../models) — `Play` / `PlayByPlayResponse` shapes the slicer produces
