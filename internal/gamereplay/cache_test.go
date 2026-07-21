package gamereplay

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
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
	return newCacheWithClock(src, clk, nil)
}

// newTraceCache is like newTestCache but returns a buffer-backed debug logger
// so tests can assert on the emitted log trace, not just behavior.
func newTraceCache(plays []models.Play, mpRows []MPRow, clk func() time.Time) (*Cache, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	src := &fakeSource{
		plays:  map[string][]models.Play{"G1": plays},
		mpRows: map[string][]MPRow{"G1": mpRows},
	}
	return newCacheWithClock(src, clk, logger), buf
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
	c := newCacheWithClock(src, time.Now, nil)
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
	csv, last, err := c.GetMP(context.Background(), "G1", GamePosition{Period: 3, GameSecs: 3600})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(csv, "3600,3,2") {
		t.Errorf("expected final stats in CSV, got: %s", csv)
	}
	if last.HomeGoals != 3 || last.AwayGoals != 2 {
		t.Errorf("expected last row to match final CSV, got HomeGoals=%d AwayGoals=%d", last.HomeGoals, last.AwayGoals)
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
		plays:   map[string][]models.Play{"G1": {gameEndPlay}},
		mpRows:  map[string][]MPRow{"G1": {{GameSecs: 3600, HomeGoals: 1}}},
		onFetch: func() { fetchCount++ },
	}
	c := newCacheWithClock(src, time.Now, nil)

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

// --- Decision-trace logging tests (one per cache state transition) ---

// TestTraceLivePollLogsMissThenHit verifies the level split: cache miss is
// info (a decision), cache hit and slice result are debug (steady-state).
func TestTraceLivePollLogsMissThenHit(t *testing.T) {
	now := time.Now()
	clk := func() time.Time { return now }
	plays := []models.Play{
		{TypeDescKey: "faceoff", PeriodDescriptor: models.PeriodDescriptor{Number: 1}, TimeInPeriod: "00:00", TimeRemaining: "20:00"},
	}
	c, buf := newTraceCache(plays, nil, clk)

	c.GetPBP(context.Background(), "G1", GamePosition{Period: 1, GameSecs: 60}) //nolint:errcheck
	first := buf.String()
	if !strings.Contains(first, "cache miss") {
		t.Errorf("expected 'cache miss' on first poll, got: %s", first)
	}
	if !strings.Contains(first, "game=G1") {
		t.Errorf("expected game=G1 attribute, got: %s", first)
	}

	buf.Reset()
	c.GetPBP(context.Background(), "G1", GamePosition{Period: 1, GameSecs: 120}) //nolint:errcheck
	second := buf.String()
	if !strings.Contains(second, "cache hit") {
		t.Errorf("expected 'cache hit' on second poll, got: %s", second)
	}
	if strings.Contains(second, "cache miss") {
		t.Errorf("did not expect 'cache miss' on second poll, got: %s", second)
	}
	if !strings.Contains(second, "slice result") {
		t.Errorf("expected 'slice result' event, got: %s", second)
	}
}

// TestTraceLevelSplitInfoIsQuietOnSteadyState verifies that at LOG_LEVEL=info
// (the default), a steady-state poll (cache hit, position, response) emits no
// lines — only decision events (miss, game-end, eviction) reach info. This is
// the core premise: filtering game=X at info should read as the game's story,
// not its heartbeat.
func TestTraceLevelSplitInfoIsQuietOnSteadyState(t *testing.T) {
	now := time.Now()
	clk := func() time.Time { return now }
	plays := []models.Play{
		{TypeDescKey: "faceoff", PeriodDescriptor: models.PeriodDescriptor{Number: 1}, TimeInPeriod: "00:00", TimeRemaining: "20:00"},
	}
	buf := &bytes.Buffer{}
	infoLogger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	src := &fakeSource{plays: map[string][]models.Play{"G1": plays}}
	c := newCacheWithClock(src, clk, infoLogger)

	// First poll is a genuine decision (cache miss → fetch): should log at info.
	c.GetPBP(context.Background(), "G1", GamePosition{Period: 1, GameSecs: 60}) //nolint:errcheck
	if !strings.Contains(buf.String(), "cache miss") {
		t.Errorf("expected cache miss to log at info, got: %s", buf.String())
	}

	// Second poll is steady-state (cache hit): should be silent at info level.
	buf.Reset()
	c.GetPBP(context.Background(), "G1", GamePosition{Period: 1, GameSecs: 120}) //nolint:errcheck
	if buf.Len() != 0 {
		t.Errorf("expected no output at info level for a steady-state cache-hit poll, got: %s", buf.String())
	}
}

// TestTraceGameEndedLogsEvent verifies the ended transition logs "game ended"
// at info, exactly once (not on subsequent polls).
func TestTraceGameEndedLogsEvent(t *testing.T) {
	now := time.Now()
	clk := func() time.Time { return now }
	plays := []models.Play{gameEndPlay}
	c, buf := newTraceCache(plays, nil, clk)

	c.GetPBP(context.Background(), "G1", GamePosition{Period: 3, GameSecs: 3600}) //nolint:errcheck
	if !strings.Contains(buf.String(), "game ended") {
		t.Errorf("expected 'game ended' event, got: %s", buf.String())
	}

	buf.Reset()
	c.GetPBP(context.Background(), "G1", GamePosition{Period: 3, GameSecs: 3600}) //nolint:errcheck
	if strings.Contains(buf.String(), "game ended") {
		t.Errorf("did not expect 'game ended' to re-fire on a repeat poll, got: %s", buf.String())
	}
}

// TestTraceEvictAndTombstoneLogsEvent verifies the GetMP ENDED→tombstone
// transition — the normal cache-exit path — logs "evict and tombstone" at
// info. No prior test exercised this transition at all before this change.
func TestTraceEvictAndTombstoneLogsEvent(t *testing.T) {
	now := time.Now()
	clk := func() time.Time { return now }
	plays := []models.Play{gameEndPlay}
	mpRows := []MPRow{{GameSecs: 3600, HomeGoals: 1, AwayGoals: 0}}
	c, buf := newTraceCache(plays, mpRows, clk)

	c.GetPBP(context.Background(), "G1", GamePosition{Period: 3, GameSecs: 3600}) //nolint:errcheck
	buf.Reset()

	c.GetMP(context.Background(), "G1", GamePosition{Period: 3, GameSecs: 3600}) //nolint:errcheck
	if !strings.Contains(buf.String(), "evict and tombstone") {
		t.Errorf("expected 'evict and tombstone' event, got: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "game=G1") {
		t.Errorf("expected game=G1 attribute, got: %s", buf.String())
	}
}

// TestTraceTombstoneServeLogsEvent verifies a re-poll during the tombstone
// TTL logs "tombstone serve" at info and does not re-fetch.
func TestTraceTombstoneServeLogsEvent(t *testing.T) {
	now := time.Now()
	clk := func() time.Time { return now }
	plays := []models.Play{gameEndPlay}
	mpRows := []MPRow{{GameSecs: 3600, HomeGoals: 1, AwayGoals: 0}}
	c, buf := newTraceCache(plays, mpRows, clk)

	c.GetPBP(context.Background(), "G1", GamePosition{Period: 3, GameSecs: 3600}) //nolint:errcheck
	c.GetMP(context.Background(), "G1", GamePosition{Period: 3, GameSecs: 3600})  //nolint:errcheck
	buf.Reset()

	c.GetMP(context.Background(), "G1", GamePosition{Period: 3, GameSecs: 3600}) //nolint:errcheck
	if !strings.Contains(buf.String(), "tombstone serve") {
		t.Errorf("expected 'tombstone serve' event, got: %s", buf.String())
	}
}

// TestTraceSweepLogsOnlyWhenEvicting verifies sweepLocked, which runs on
// every poll, only produces a log line on the poll where it actually evicts
// something — otherwise every poll would log a no-op sweep.
func TestTraceSweepLogsOnlyWhenEvicting(t *testing.T) {
	now := time.Now()
	clk := func() time.Time { return now }
	plays := []models.Play{gameEndPlay}
	c, buf := newTraceCache(plays, []MPRow{}, clk)

	// Drive to ENDED.
	c.GetPBP(context.Background(), "G1", GamePosition{Period: 3, GameSecs: 3600}) //nolint:errcheck
	buf.Reset()

	// A poll before the threshold: sweep runs but evicts nothing — no log line.
	c.GetPBP(context.Background(), "G1", GamePosition{Period: 3, GameSecs: 3600}) //nolint:errcheck
	if strings.Contains(buf.String(), "eviction sweep") {
		t.Errorf("did not expect an eviction sweep log when nothing was evicted, got: %s", buf.String())
	}

	// Advance clock past sweepThreshold and poll a second, unrelated game to
	// trigger the sweep on G1's abandoned entry.
	now = now.Add(sweepThreshold + time.Minute)
	buf.Reset()
	c.GetPBP(context.Background(), "G2", GamePosition{Period: 1, GameSecs: 0}) //nolint:errcheck
	if !strings.Contains(buf.String(), "eviction sweep") {
		t.Errorf("expected an eviction sweep log when G1 was evicted, got: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "G1") {
		t.Errorf("expected the evicted game ID G1 in the sweep log, got: %s", buf.String())
	}
}

// TestNewCacheNilLoggerDoesNotPanic asserts the nil→slog.Default() fallback:
// a nil logger passed to NewCache/NewCacheForTest must not panic on first use.
func TestNewCacheNilLoggerDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil logger caused a panic: %v", r)
		}
	}()
	src := &fakeSource{plays: map[string][]models.Play{"G1": {gameEndPlay}}}
	c := NewCacheForTest(src, time.Now, nil)
	c.GetPBP(context.Background(), "G1", GamePosition{Period: 3, GameSecs: 3600}) //nolint:errcheck
}

// TestGetPBPTombstoneServesPlays verifies that after GetMP installs a tombstone,
// a subsequent GetPBP call returns the complete play history (not empty plays).
// Previously the tombstone entry had nil plays, making GetPBP return an empty
// response indistinguishable from a pre-game state.
func TestGetPBPTombstoneServesPlays(t *testing.T) {
	now := time.Now()
	clk := func() time.Time { return now }
	plays := []models.Play{gameEndPlay}
	mpRows := []MPRow{{GameSecs: 3600, HomeGoals: 1, AwayGoals: 0}}
	c, _ := newTraceCache(plays, mpRows, clk)

	// Drive to ENDED via PBP, then to tombstone via MP.
	c.GetPBP(context.Background(), "G1", GamePosition{Period: 3, GameSecs: 3600}) //nolint:errcheck
	c.GetMP(context.Background(), "G1", GamePosition{Period: 3, GameSecs: 3600})  //nolint:errcheck

	// PBP poll after tombstone: must return the complete play list, not empty.
	got, err := c.GetPBP(context.Background(), "G1", GamePosition{Period: 5, GameSecs: 3900, Ended: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("GetPBP on tombstone returned empty plays — expected complete play history")
	}
	if got[len(got)-1].TypeDescKey != "game-end" {
		t.Errorf("last play = %q, want game-end", got[len(got)-1].TypeDescKey)
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
