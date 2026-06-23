package gamereplay

import (
	"context"
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
	stateActive   gameState = iota
	stateEnded              // PBP has delivered game-end; waiting for one final MP poll
	stateTombstone          // evicted; serve stable terminal response without re-fetching
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
}

// StartTimeProvider is implemented by ScheduleServer to map a game's string ID
// to its shifted start time. Defined in this package so services can import
// gamereplay without creating an import cycle (A1).
type StartTimeProvider interface {
	StartTime(gameID string) (time.Time, bool)
}

// NewCache creates a Cache backed by the given Source.
func NewCache(src Source) *Cache {
	return &Cache{
		entries: make(map[string]*cacheEntry),
		src:     src,
		clock:   time.Now,
	}
}

// NewCacheForTest creates a Cache with injectable Source and clock (for testing).
func NewCacheForTest(src Source, clock func() time.Time) *Cache {
	return newCacheWithClock(src, clock)
}

// newCacheWithClock creates a Cache with an injectable clock (for testing).
func newCacheWithClock(src Source, clock func() time.Time) *Cache {
	return &Cache{
		entries: make(map[string]*cacheEntry),
		src:     src,
		clock:   clock,
	}
}

// GetPBP returns the plays visible at pos for the given game.
// On a cache miss, it fetches from upstream (may double-fetch under concurrency;
// the mutex-guarded last-write-wins is safe because the data is identical, C1).
// If PBP has been served with a game-end play, the entry transitions to ENDED.
// Fetch failures are returned as errors (caller returns 5xx, A4).
func (c *Cache) GetPBP(ctx context.Context, gameID string, pos GamePosition) ([]models.Play, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.sweepLocked()

	entry, err := c.ensureLocked(ctx, gameID)
	if err != nil {
		return nil, err
	}

	plays := SlicePBP(entry.plays, pos)

	// Detect game-end: the terminal play is present in the slice.
	if !pos.Ended && len(plays) > 0 && plays[len(plays)-1].TypeDescKey == "game-end" {
		if entry.state == stateActive {
			entry.state = stateEnded
			entry.gameEndAt = c.clock()
		}
	}
	// Post-game position always marks as ended.
	if pos.Ended && entry.state == stateActive {
		entry.state = stateEnded
		entry.gameEndAt = c.clock()
	}

	return plays, nil
}

// GetMP returns the MoneyPuck CSV row for the given game at pos.
// If the entry is in ENDED state (PBP already served game-end), serves the
// final row and then evicts both PBP+MP, installing a tombstone.
// Returns the CSV string directly (stats handler re-emits it verbatim).
func (c *Cache) GetMP(ctx context.Context, gameID string, pos GamePosition) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.sweepLocked()

	// Tombstone check: game is done but entry still exists as a placeholder.
	if e, ok := c.entries[gameID]; ok && e.state == stateTombstone {
		if c.clock().Before(e.tombExpiry) {
			return SliceMP(e.mpRows, GamePosition{Period: 5, GameSecs: 3*periodGameSecs + otGameSecs, Ended: true}), nil
		}
		// Tombstone expired; remove and fall through to re-fetch.
		delete(c.entries, gameID)
	}

	entry, err := c.ensureLocked(ctx, gameID)
	if err != nil {
		return "", err
	}

	csv := SliceMP(entry.mpRows, pos)

	if entry.state == stateEnded {
		// Serve the final (complete) stats then evict both feeds, leaving a tombstone.
		finalCSV := SliceMP(entry.mpRows, GamePosition{Period: 5, GameSecs: 3*periodGameSecs + otGameSecs, Ended: true})
		// Install tombstone so re-polls get a stable response without re-fetching.
		c.entries[gameID] = &cacheEntry{
			state:      stateTombstone,
			mpRows:     entry.mpRows, // kept for tombstone responses
			tombExpiry: c.clock().Add(tombstoneTTL),
		}
		return finalCSV, nil
	}

	return csv, nil
}

// ensureLocked populates the cache entry for gameID if absent.
// Must be called with c.mu held.
func (c *Cache) ensureLocked(ctx context.Context, gameID string) (*cacheEntry, error) {
	if e, ok := c.entries[gameID]; ok {
		return e, nil
	}
	// Release lock during fetch so other games aren't blocked.
	// Concurrent fetches for the same game may race (C1 decision: accept dup fetches;
	// last-write-wins; data is identical).
	c.mu.Unlock()
	plays, pbpErr := c.src.FetchPlayByPlay(ctx, gameID)
	mpRows, mpErr := c.src.FetchMoneyPuck(ctx, gameID)
	c.mu.Lock()

	if pbpErr != nil {
		return nil, pbpErr
	}
	if mpErr != nil {
		return nil, mpErr
	}

	// Another goroutine may have populated the entry while we fetched.
	// Only write if absent (or overwrite — both data sets are identical).
	e := &cacheEntry{
		plays:  plays,
		mpRows: mpRows,
		state:  stateActive,
	}
	c.entries[gameID] = e
	return e, nil
}

// sweepLocked drops entries whose game-end was >sweepThreshold ago and whose
// tombstone has expired, preventing leaks for games abandoned before game-end (P1).
// Must be called with c.mu held.
func (c *Cache) sweepLocked() {
	now := c.clock()
	for id, e := range c.entries {
		switch e.state {
		case stateEnded:
			if !e.gameEndAt.IsZero() && now.Sub(e.gameEndAt) > sweepThreshold {
				delete(c.entries, id)
			}
		case stateTombstone:
			if now.After(e.tombExpiry) {
				delete(c.entries, id)
			}
		}
	}
}
