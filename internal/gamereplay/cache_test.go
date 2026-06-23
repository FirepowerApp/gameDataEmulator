package gamereplay

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"testserver/internal/models"
)

var gameEndPlay = models.Play{
	TypeDescKey: "game-end",
	PeriodDescriptor: models.PeriodDescriptor{
		Number: 3, PeriodType: "REG", MaxRegulationPeriods: 3,
	},
	TimeInPeriod: "20:00", TimeRemaining: "00:00",
}

func newTestCache(plays []models.Play, mpRows []MPRow, clk func() time.Time) *Cache {
	src := &fakeSource{
		plays:  map[string][]models.Play{"G1": plays},
		mpRows: map[string][]MPRow{"G1": mpRows},
	}
	return newCacheWithClock(src, clk)
}

func TestCacheGetPBPCacheMiss(t *testing.T) {
	plays := []models.Play{
		{TypeDescKey: "faceoff", PeriodDescriptor: models.PeriodDescriptor{Number: 1}, TimeInPeriod: "00:00", TimeRemaining: "20:00"},
	}
	c := newTestCache(plays, nil, time.Now)
	got, err := c.GetPBP(context.Background(), "G1", GamePosition{Period: 1, GameSecs: 60})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].TypeDescKey != "faceoff" {
		t.Errorf("unexpected plays: %v", got)
	}
}

func TestCacheGetPBPFetchError(t *testing.T) {
	src := &fakeSource{err: errTest}
	c := newCacheWithClock(src, time.Now)
	_, err := c.GetPBP(context.Background(), "G1", GamePosition{Period: 1, GameSecs: 60})
	if err == nil {
		t.Error("expected fetch error, got nil")
	}
}

var errTest = fmt.Errorf("upstream down")

func TestCacheGameEndTransitionsToEnded(t *testing.T) {
	now := time.Now()
	clk := func() time.Time { return now }

	plays := []models.Play{
		{TypeDescKey: "faceoff", PeriodDescriptor: models.PeriodDescriptor{Number: 1}, TimeInPeriod: "00:00", TimeRemaining: "20:00"},
		gameEndPlay,
	}
	c := newTestCache(plays, []MPRow{{GameSecs: 3600, HomeGoals: 2, AwayGoals: 1, HomeExpectedGoals: 2.0, AwayExpectedGoals: 1.5}}, clk)

	// Request that includes game-end play.
	endPos := GamePosition{Period: 3, GameSecs: 3600}
	got, err := c.GetPBP(context.Background(), "G1", endPos)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) == 0 || got[len(got)-1].TypeDescKey != "game-end" {
		t.Fatalf("expected game-end play, got: %v", got)
	}

	c.mu.Lock()
	state := c.entries["G1"].state
	c.mu.Unlock()
	if state != stateEnded {
		t.Errorf("state = %v, want stateEnded", state)
	}
}

func TestCacheMPAfterEndedEvictsAndInstallsTombstone(t *testing.T) {
	now := time.Now()
	clk := func() time.Time { return now }

	plays := []models.Play{gameEndPlay}
	mpRows := []MPRow{{GameSecs: 3600, HomeGoals: 3, AwayGoals: 2, HomeExpectedGoals: 3.0, AwayExpectedGoals: 2.0}}
	c := newTestCache(plays, mpRows, clk)

	// Drive to ENDED state via PBP.
	c.GetPBP(context.Background(), "G1", GamePosition{Period: 3, GameSecs: 3600}) //nolint:errcheck

	// First MP request after ENDED → serve final stats + evict + tombstone.
	csv, err := c.GetMP(context.Background(), "G1", GamePosition{Period: 3, GameSecs: 3600})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(csv, "3600,3,2") {
		t.Errorf("expected final stats in CSV, got: %s", csv)
	}

	// Entry should be tombstone now.
	c.mu.Lock()
	e, ok := c.entries["G1"]
	c.mu.Unlock()
	if !ok || e.state != stateTombstone {
		t.Errorf("expected tombstone entry, ok=%v state=%v", ok, e)
	}
}

func TestCacheTombstoneServesFinalWithoutRefetch(t *testing.T) {
	fetchCount := 0
	src := &countingSource{
		plays:  map[string][]models.Play{"G1": {gameEndPlay}},
		mpRows: map[string][]MPRow{"G1": {{GameSecs: 3600, HomeGoals: 1}}},
		onFetch: func() { fetchCount++ },
	}
	c := newCacheWithClock(src, time.Now)

	// Drive to ENDED then evict.
	c.GetPBP(context.Background(), "G1", GamePosition{Period: 3, GameSecs: 3600}) //nolint:errcheck
	c.GetMP(context.Background(), "G1", GamePosition{Period: 3, GameSecs: 3600})  //nolint:errcheck

	beforeCount := fetchCount

	// Re-poll during tombstone TTL should NOT re-fetch.
	c.GetMP(context.Background(), "G1", GamePosition{Period: 3, GameSecs: 3600}) //nolint:errcheck
	if fetchCount != beforeCount {
		t.Errorf("re-poll during tombstone triggered a re-fetch (count %d→%d)", beforeCount, fetchCount)
	}
}

func TestCacheLazySweepDropsAbandonedEntries(t *testing.T) {
	now := time.Now()
	clk := func() time.Time { return now }

	plays := []models.Play{gameEndPlay}
	c := newTestCache(plays, []MPRow{}, clk)

	// Populate and drive to ENDED.
	c.GetPBP(context.Background(), "G1", GamePosition{Period: 3, GameSecs: 3600}) //nolint:errcheck

	// Advance clock past sweepThreshold.
	now = now.Add(sweepThreshold + time.Minute)

	// Next access triggers the sweep.
	c.GetPBP(context.Background(), "G1", GamePosition{Period: 1, GameSecs: 0}) //nolint:errcheck

	// Re-fetch occurred (entry was swept and re-fetched as a miss).
	// The entry should be active again (fresh fetch).
	c.mu.Lock()
	e, ok := c.entries["G1"]
	c.mu.Unlock()
	if !ok {
		t.Error("expected entry after re-fetch following sweep")
	}
	if e.state != stateActive {
		t.Errorf("expected stateActive after fresh fetch, got %v", e.state)
	}
}

// countingSource wraps fakeSource and increments a counter on each fetch.
type countingSource struct {
	plays   map[string][]models.Play
	mpRows  map[string][]MPRow
	onFetch func()
}

func (cs *countingSource) FetchPlayByPlay(ctx context.Context, gameID string) ([]models.Play, error) {
	cs.onFetch()
	return cs.plays[gameID], nil
}
func (cs *countingSource) FetchMoneyPuck(ctx context.Context, gameID string) ([]MPRow, error) {
	cs.onFetch()
	return cs.mpRows[gameID], nil
}
