package gamereplay

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"testserver/internal/models"
)

// gameState is the lifecycle state of one game's cache entry.
//
//	ACTIVE ──(PBP serves game-end)──► ENDED ──(MP queried)──► evicted
//	                                                │
//	                               entry removed; next poll is a cache miss.
//	                               A short tombstone prevents re-fetch thrash.
type gameState int

const (
	stateActive    gameState = iota
	stateEnded               // PBP has delivered game-end; waiting for one final MP poll
	stateTombstone           // evicted; serve stable terminal response without re-fetching
)

// tombstoneTTL is how long a finished game's tombstone lives before expiry.
// Prevents post-evict re-fetch thrash (P2).
const tombstoneTTL = time.Hour

// sweepThreshold evicts abandoned entries (games nobody polled past game-end).
// Applied lazily on cache access (P1).
const sweepThreshold = time.Hour

// cacheEntry holds the fetched game data and its lifecycle state.
type cacheEntry struct {
	plays      []models.Play
	mpRows     []MPRow
	state      gameState
	gameEndAt  time.Time // wall-clock when game-end was served (for tombstone TTL)
	tombExpiry time.Time // when the tombstone expires
	fetchedAt  time.Time // wall-clock when this entry was fetched (backs the "age" log attribute)
}

// Cache is a shared, thread-safe store of fetched game data.
// A single Cache instance is injected into both the PBP and stats handlers
// so they coordinate on eviction across ports (A2).
//
// Lifecycle per game (D3):
//
//	miss → fetch (may double-fetch under concurrent load; safe, same data) → ACTIVE
//	PBP serves game-end play → ENDED
//	first MP request after ENDED → serve final stats → evict → tombstone(1h)
//	re-poll during tombstone → stable terminal response, no re-fetch
//	tombstone expires → entry removed entirely
type Cache struct {
	mu      sync.Mutex
	entries map[string]*cacheEntry
	src     Source
	clock   func() time.Time // injectable for testing
	logger  *slog.Logger
}

// StartTimeProvider is implemented by ScheduleServer to map a game's string ID
// to its shifted start time. Defined in this package so services can import
// gamereplay without creating an import cycle (A1).
type StartTimeProvider interface {
	StartTime(gameID string) (time.Time, bool)
}

// NewCache creates a Cache backed by the given Source. A nil logger falls
// back to slog.Default(); the returned Cache never holds a nil logger, so no
// call site needs to guard against one.
func NewCache(src Source, logger *slog.Logger) *Cache {
	return newCacheWithClock(src, time.Now, logger)
}

// NewCacheForTest creates a Cache with injectable Source and clock (for testing).
func NewCacheForTest(src Source, clock func() time.Time, logger *slog.Logger) *Cache {
	return newCacheWithClock(src, clock, logger)
}

// newCacheWithClock creates a Cache with an injectable clock (for testing).
func newCacheWithClock(src Source, clock func() time.Time, logger *slog.Logger) *Cache {
	if logger == nil {
		logger = slog.Default()
	}
	return &Cache{
		entries: make(map[string]*cacheEntry),
		src:     src,
		clock:   clock,
		logger:  logger,
	}
}

// GetPBP returns the plays visible at pos for the given game.
// On a cache miss, it fetches from upstream (may double-fetch under concurrency;
// the mutex-guarded last-write-wins is safe because the data is identical, C1).
// If PBP has been served with a game-end play, the entry transitions to ENDED.
// Fetch failures are returned as errors (caller returns 5xx, A4).
//
// Logging: fields to log are collected while c.mu is held, then emitted after
// Unlock — slog writes are synchronous I/O and must not run under the lock
// that serializes every other game's cache access.
func (c *Cache) GetPBP(ctx context.Context, gameID string, pos GamePosition) ([]models.Play, error) {
	c.mu.Lock()

	evicted, remaining := c.sweepLocked()
	logSweep := func() { c.logSweep(gameID, evicted, remaining) }

	// Tombstone check: game is done; serve complete play history without re-fetching.
	// Without this, GetPBP would return the tombstone entry with nil plays, producing
	// an empty response indistinguishable from a pre-game response.
	if e, ok := c.entries[gameID]; ok && e.state == stateTombstone {
		if c.clock().Before(e.tombExpiry) {
			finalPos := GamePosition{Period: 5, GameSecs: 3*periodGameSecs + otGameSecs, Ended: true}
			plays := SlicePBP(e.plays, finalPos)
			c.mu.Unlock()
			logSweep()
			c.logger.Info("tombstone serve", LogKeyGame, gameID, LogKeyFeed, "pbp")
			return plays, nil
		}
		delete(c.entries, gameID)
	}

	entry, fetched, err := c.ensureLocked(ctx, gameID)
	if err != nil {
		c.mu.Unlock()
		logSweep()
		return nil, err
	}

	plays := SlicePBP(entry.plays, pos)
	playsTotal := len(entry.plays)
	age := c.clock().Sub(entry.fetchedAt)

	// Detect game-end: the terminal play is present in the slice.
	endedNow := false
	if !pos.Ended && len(plays) > 0 && plays[len(plays)-1].TypeDescKey == "game-end" {
		if entry.state == stateActive {
			entry.state = stateEnded
			entry.gameEndAt = c.clock()
			endedNow = true
		}
	}
	// Post-game position always marks as ended.
	if pos.Ended && entry.state == stateActive {
		entry.state = stateEnded
		entry.gameEndAt = c.clock()
		endedNow = true
	}

	c.mu.Unlock()

	logSweep()
	if fetched {
		c.logger.Info("cache miss", LogKeyGame, gameID, LogKeyFeed, "pbp")
	} else {
		c.logger.Debug("cache hit", LogKeyGame, gameID, LogKeyFeed, "pbp", "age", age, "plays", playsTotal)
	}
	c.logger.Debug("slice result", LogKeyGame, gameID, LogKeyFeed, "pbp", "plays_total", playsTotal, "plays_served", len(plays))
	if endedNow {
		c.logger.Info("game ended", LogKeyGame, gameID, LogKeyFeed, "pbp")
	}

	return plays, nil
}

// GetMP returns the MoneyPuck CSV row for the given game at pos, plus the
// same row as structured data (last) so callers can log the current score
// without re-parsing the CSV.
// If the entry is in ENDED state (PBP already served game-end), serves the
// final row and then evicts both PBP+MP, installing a tombstone.
//
// See GetPBP's doc comment for why logging happens after Unlock, not under it.
func (c *Cache) GetMP(ctx context.Context, gameID string, pos GamePosition) (csv string, last MPRow, err error) {
	c.mu.Lock()

	evicted, remaining := c.sweepLocked()
	logSweep := func() { c.logSweep(gameID, evicted, remaining) }

	// Tombstone check: game is done but entry still exists as a placeholder.
	if e, ok := c.entries[gameID]; ok && e.state == stateTombstone {
		if c.clock().Before(e.tombExpiry) {
			finalPos := GamePosition{Period: 5, GameSecs: 3*periodGameSecs + otGameSecs, Ended: true}
			csv = SliceMP(e.mpRows, finalPos)
			last, _ = LastMPRow(e.mpRows, finalPos)
			c.mu.Unlock()
			logSweep()
			c.logger.Info("tombstone serve", LogKeyGame, gameID, LogKeyFeed, "stats")
			return csv, last, nil
		}
		// Tombstone expired; remove and fall through to re-fetch.
		delete(c.entries, gameID)
	}

	entry, fetched, fetchErr := c.ensureLocked(ctx, gameID)
	if fetchErr != nil {
		c.mu.Unlock()
		logSweep()
		return "", MPRow{}, fetchErr
	}

	csv = SliceMP(entry.mpRows, pos)
	last, _ = LastMPRow(entry.mpRows, pos)
	rowsTotal := len(entry.mpRows)
	age := c.clock().Sub(entry.fetchedAt)

	if entry.state == stateEnded {
		// Serve the final (complete) stats then evict both feeds, leaving a tombstone.
		finalPos := GamePosition{Period: 5, GameSecs: 3*periodGameSecs + otGameSecs, Ended: true}
		finalCSV := SliceMP(entry.mpRows, finalPos)
		finalLast, _ := LastMPRow(entry.mpRows, finalPos)
		// Install tombstone so re-polls get a stable response without re-fetching.
		// plays is stored alongside mpRows so a PBP tombstone-hit can serve
		// the complete play history rather than returning empty plays.
		c.entries[gameID] = &cacheEntry{
			state:      stateTombstone,
			plays:      entry.plays,  // kept for PBP tombstone responses
			mpRows:     entry.mpRows, // kept for stats tombstone responses
			tombExpiry: c.clock().Add(tombstoneTTL),
		}
		c.mu.Unlock()

		logSweep()
		if fetched {
			c.logger.Info("cache miss", LogKeyGame, gameID, LogKeyFeed, "stats")
		} else {
			c.logger.Debug("cache hit", LogKeyGame, gameID, LogKeyFeed, "stats", "age", age, "rows", rowsTotal)
		}
		c.logger.Info("evict and tombstone", LogKeyGame, gameID, LogKeyFeed, "stats")
		return finalCSV, finalLast, nil
	}

	c.mu.Unlock()

	logSweep()
	if fetched {
		c.logger.Info("cache miss", LogKeyGame, gameID, LogKeyFeed, "stats")
	} else {
		c.logger.Debug("cache hit", LogKeyGame, gameID, LogKeyFeed, "stats", "age", age, "rows", rowsTotal)
	}
	c.logger.Debug("slice result", LogKeyGame, gameID, LogKeyFeed, "stats", "rows_total", rowsTotal)

	return csv, last, nil
}

// logSweep logs the eviction-sweep event, but only when the sweep actually
// evicted something. sweepLocked runs on every GetPBP/GetMP call, so logging
// unconditionally would fire on every poll even when nothing was evicted.
func (c *Cache) logSweep(gameID string, evicted []string, remaining int) {
	if len(evicted) > 0 {
		c.logger.Info("eviction sweep", LogKeyGame, gameID, "evicted", evicted, "remaining", remaining)
	}
}

// ensureLocked populates the cache entry for gameID if absent, reporting
// whether a fetch happened (cache miss) or the entry already existed (hit).
// Must be called with c.mu held.
func (c *Cache) ensureLocked(ctx context.Context, gameID string) (entry *cacheEntry, fetched bool, err error) {
	if e, ok := c.entries[gameID]; ok {
		return e, false, nil
	}
	// Release lock during fetch so other games aren't blocked.
	// Concurrent fetches for the same game may race (C1 decision: accept dup fetches;
	// last-write-wins; data is identical).
	c.mu.Unlock()
	plays, pbpErr := c.src.FetchPlayByPlay(ctx, gameID)
	mpRows, mpErr := c.src.FetchMoneyPuck(ctx, gameID)
	c.mu.Lock()

	if pbpErr != nil {
		return nil, true, pbpErr
	}
	if mpErr != nil {
		return nil, true, mpErr
	}

	// Another goroutine may have populated the entry while we fetched.
	// Only write if absent (or overwrite — both data sets are identical).
	e := &cacheEntry{
		plays:     plays,
		mpRows:    mpRows,
		state:     stateActive,
		fetchedAt: c.clock(),
	}
	c.entries[gameID] = e
	return e, true, nil
}

// sweepLocked drops entries whose game-end was >sweepThreshold ago and whose
// tombstone has expired, preventing leaks for games abandoned before game-end (P1).
// Returns the evicted game IDs and the entry count remaining after the sweep.
// Must be called with c.mu held.
func (c *Cache) sweepLocked() (evicted []string, remaining int) {
	now := c.clock()
	for id, e := range c.entries {
		switch e.state {
		case stateEnded:
			if !e.gameEndAt.IsZero() && now.Sub(e.gameEndAt) > sweepThreshold {
				delete(c.entries, id)
				evicted = append(evicted, id)
			}
		case stateTombstone:
			if now.After(e.tombExpiry) {
				delete(c.entries, id)
				evicted = append(evicted, id)
			}
		}
	}
	return evicted, len(c.entries)
}
