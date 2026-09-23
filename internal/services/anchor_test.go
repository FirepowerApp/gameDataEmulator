package services

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeBoundaryFetcher implements nhlScheduleFetcher against an in-memory map,
// so state resolution is testable without a live NHL API call. It counts calls.
type fakeBoundaryFetcher struct {
	byDate map[string]seasonBoundaries
	err    error // if set, all calls return this error
	calls  atomic.Int32
}

func (f *fakeBoundaryFetcher) fetchBoundaries(_ context.Context, date string) (seasonBoundaries, error) {
	f.calls.Add(1)
	if f.err != nil {
		return seasonBoundaries{}, f.err
	}
	b, ok := f.byDate[date]
	if !ok {
		return seasonBoundaries{}, errors.New("no fake data for date " + date)
	}
	return b, nil
}

func mustParseDate(date string) time.Time {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		panic("invalid date constant " + date + ": " + err.Error())
	}
	return t
}

func day(date string) time.Time { return mustParseDate(date) }

// apiFixture mirrors the confirmed real API responses around the 2026 offseason.
// Once the playoffs end, every response for a later date describes the NEXT
// season (regularSeasonStartDate 2026-09-29, playoffEndDate 2027-06-10);
// responses for dates inside the season that just ended describe that season.
func apiFixture() map[string]seasonBoundaries {
	next := func(prev string) seasonBoundaries {
		return seasonBoundaries{RegularSeasonStartDate: "2026-09-29", PlayoffEndDate: "2027-06-10", PreviousStartDate: prev}
	}
	return map[string]seasonBoundaries{
		// Prior season (2025-26), playoffs ended 2026-06-15.
		"2026-06-14": {RegularSeasonStartDate: "2025-10-07", PlayoffEndDate: "2026-06-15", PreviousStartDate: "2026-06-07"},
		"2026-06-10": {RegularSeasonStartDate: "2025-10-07", PlayoffEndDate: "2026-06-15", PreviousStartDate: "2026-06-03"},
		// Offseason.
		"2026-07-15": next("2026-06-10"),
		// Preseason: previousStartDate walks back through next-season responses
		// before reaching the season that just ended.
		"2026-09-22": next("2026-09-15"),
		"2026-09-15": next("2026-09-08"),
		"2026-09-08": next("2026-06-14"),
		// Regular season and postseason of the following year.
		"2026-10-15": {RegularSeasonStartDate: "2026-09-29", PlayoffEndDate: "2027-06-10", PreviousStartDate: "2026-10-08"},
		"2027-05-01": {RegularSeasonStartDate: "2026-09-29", PlayoffEndDate: "2027-06-10", PreviousStartDate: "2027-04-24"},
	}
}

func fixtureFetcher() *fakeBoundaryFetcher { return &fakeBoundaryFetcher{byDate: apiFixture()} }

// TestResolveStateActive verifies both halves of the serving window resolve to
// the same anchor — the day after the previous season's playoffs ended.
func TestResolveStateActive(t *testing.T) {
	for _, tt := range []struct{ name, today string }{
		{"offseason", "2026-07-15"},
		{"preseason (walks back past next-season responses)", "2026-09-22"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveState(context.Background(), fixtureFetcher(), day(tt.today), nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !got.active {
				t.Fatal("want active (serve the stack)")
			}
			if want := day("2026-06-16"); !got.anchor.Equal(want) {
				t.Errorf("anchor = %s, want 2026-06-16", got.anchor.Format("2006-01-02"))
			}
			if want := day("2026-09-29"); !got.regularSeasonStart.Equal(want) {
				t.Errorf("regularSeasonStart = %s, want 2026-09-29", got.regularSeasonStart.Format("2006-01-02"))
			}
		})
	}
}

// TestResolveStateInactiveOnceRegularSeasonStarts verifies the regular season
// and postseason resolve to "serve nothing" without error or any walk-back.
func TestResolveStateInactiveOnceRegularSeasonStarts(t *testing.T) {
	for _, today := range []string{"2026-09-29", "2026-10-15", "2027-05-01"} {
		t.Run(today, func(t *testing.T) {
			f := fixtureFetcher()
			f.byDate[today] = seasonBoundaries{RegularSeasonStartDate: "2026-09-29", PlayoffEndDate: "2027-06-10"}
			got, err := resolveState(context.Background(), f, day(today), nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.active {
				t.Error("want inactive once the regular season has started")
			}
			if f.calls.Load() != 1 {
				t.Errorf("API calls = %d, want 1 (no walk-back when inactive)", f.calls.Load())
			}
		})
	}
}

// TestResolveStateReusesAnchor verifies a previously resolved anchor is reused
// (one API call, no walk) while the upcoming regular season is unchanged.
func TestResolveStateReusesAnchor(t *testing.T) {
	f := fixtureFetcher()
	prev := seasonState{active: true, anchor: day("2026-06-16"), regularSeasonStart: day("2026-09-29")}
	got, err := resolveState(context.Background(), f, day("2026-09-22"), &prev)
	if err != nil || got != prev {
		t.Fatalf("got (%+v, %v), want the previous state", got, err)
	}
	if f.calls.Load() != 1 {
		t.Errorf("API calls = %d, want 1", f.calls.Load())
	}
}

// TestResolveStateUsesUTCDate verifies "today" is the UTC date, not the local
// one: 2026-07-15 23:30 in UTC-5 is already 2026-07-16 in UTC.
func TestResolveStateUsesUTCDate(t *testing.T) {
	f := fixtureFetcher()
	f.byDate["2026-07-16"] = f.byDate["2026-07-15"]
	delete(f.byDate, "2026-07-15") // only the UTC date is served
	local := time.Date(2026, 7, 15, 23, 30, 0, 0, time.FixedZone("UTC-5", -5*3600))
	if _, err := resolveState(context.Background(), f, local, nil); err != nil {
		t.Fatalf("expected the UTC date to be used, got: %v", err)
	}
}

// TestResolveStateErrors verifies every way the API can fail to answer returns
// an error — there is no guessed fallback.
func TestResolveStateErrors(t *testing.T) {
	with := func(mutate func(map[string]seasonBoundaries)) *fakeBoundaryFetcher {
		f := fixtureFetcher()
		mutate(f.byDate)
		return f
	}
	tests := []struct {
		name    string
		fetcher *fakeBoundaryFetcher
		today   string
	}{
		{"API unreachable", &fakeBoundaryFetcher{err: errors.New("connection refused")}, "2026-07-15"},
		{"invalid regularSeasonStartDate", with(func(m map[string]seasonBoundaries) {
			m["2026-07-15"] = seasonBoundaries{RegularSeasonStartDate: "not-a-date", PreviousStartDate: "2026-06-10"}
		}), "2026-07-15"},
		{"missing regularSeasonStartDate", with(func(m map[string]seasonBoundaries) {
			m["2026-07-15"] = seasonBoundaries{PreviousStartDate: "2026-06-10"}
		}), "2026-07-15"},
		{"missing previousStartDate", with(func(m map[string]seasonBoundaries) {
			m["2026-07-15"] = seasonBoundaries{RegularSeasonStartDate: "2026-09-29"}
		}), "2026-07-15"},
		{"previous-season fetch fails", with(func(m map[string]seasonBoundaries) { delete(m, "2026-06-10") }), "2026-07-15"},
		{"invalid previous playoffEndDate", with(func(m map[string]seasonBoundaries) {
			m["2026-06-10"] = seasonBoundaries{RegularSeasonStartDate: "2025-10-07", PlayoffEndDate: "not-a-date"}
		}), "2026-07-15"},
		{"previous season's playoffs end after today", with(func(m map[string]seasonBoundaries) {
			m["2026-06-10"] = seasonBoundaries{RegularSeasonStartDate: "2025-10-07", PlayoffEndDate: "2026-07-20"}
		}), "2026-07-15"},
		{"previous season never found", with(func(m map[string]seasonBoundaries) {
			// A cycle of next-season responses that never reaches a started season.
			m["2026-07-15"] = seasonBoundaries{RegularSeasonStartDate: "2026-09-29", PreviousStartDate: "2026-07-08"}
			m["2026-07-08"] = seasonBoundaries{RegularSeasonStartDate: "2026-09-29", PreviousStartDate: "2026-07-08"}
		}), "2026-07-15"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := resolveState(context.Background(), tt.fetcher, day(tt.today), nil); err == nil {
				t.Error("expected an error (no fallback), got nil")
			}
		})
	}
}

// slowFetcher blocks until its context is done, like a hung upstream.
type slowFetcher struct{}

func (slowFetcher) fetchBoundaries(ctx context.Context, _ string) (seasonBoundaries, error) {
	<-ctx.Done()
	return seasonBoundaries{}, ctx.Err()
}

// TestResolveStateTimeout verifies a hung upstream can't hang a request.
func TestResolveStateTimeout(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		_, err := resolveState(context.Background(), slowFetcher{}, day("2026-07-15"), nil)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("expected a timeout error, got nil")
		}
	case <-time.After(anchorFetchTimeout + 2*time.Second):
		t.Fatal("resolveState did not return after the per-call timeout")
	}
}

// testClock is a manually advanced clock.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *testClock) advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func schedStatus(s *ScheduleServer, date string) (int, string) {
	rec := httptest.NewRecorder()
	s.HandleSchedule(rec, httptest.NewRequest(http.MethodGet, "/v1/schedule/"+date, nil))
	return rec.Code, rec.Body.String()
}

// TestNewScheduleServerMakesNoNetworkCall verifies the emulator starts at any
// time of year: construction never touches the NHL API.
func TestNewScheduleServerMakesNoNetworkCall(t *testing.T) {
	f := &fakeBoundaryFetcher{err: errors.New("no network")}
	NewScheduleServerForTest(f, time.Now, nil)
	if f.calls.Load() != 0 {
		t.Errorf("constructor made %d API calls, want 0", f.calls.Load())
	}
}

// TestScheduleServerServesStackInOffseasonAndPreseasonThenNothing walks the
// clock through the whole year and checks the decision follows the API alone.
func TestScheduleServerServesStackInOffseasonAndPreseasonThenNothing(t *testing.T) {
	f := fixtureFetcher()
	clock := &testClock{t: day("2026-07-15")}
	s := NewScheduleServerForTest(f, clock.now, nil)

	games := func(date string) int {
		t.Helper()
		code, body := schedStatus(s, date)
		if code != http.StatusOK {
			t.Fatalf("%s: status %d", date, code)
		}
		return len(gameIDs(decodeSchedule(t, body)))
	}

	// Offseason: games.
	if games("2026-07-15") == 0 {
		t.Error("offseason: want games")
	}
	// Preseason (past the TTL, state re-resolved): still games.
	clock.advance(70 * 24 * time.Hour)
	clock.t = day("2026-09-22")
	if games("2026-09-22") == 0 {
		t.Error("preseason: want games")
	}
	// Regular season starts: nothing, even for a date that would map into the stack.
	clock.t = day("2026-10-15")
	if n := games("2026-10-15"); n != 0 {
		t.Errorf("regular season: got %d games, want none", n)
	}
	if _, ok := s.StartTime("2025020001"); ok {
		t.Error("StartTime should report false while no games are served")
	}
	// Playoffs still over, next offseason begins for a new season (API flips).
	f.byDate["2027-07-15"] = seasonBoundaries{RegularSeasonStartDate: "2027-09-28", PlayoffEndDate: "2028-06-10", PreviousStartDate: "2027-06-10"}
	f.byDate["2027-06-10"] = seasonBoundaries{RegularSeasonStartDate: "2026-09-29", PlayoffEndDate: "2027-06-12", PreviousStartDate: "2027-06-03"}
	clock.t = day("2027-07-15")
	if games("2027-07-15") == 0 {
		t.Error("next offseason: want games again")
	}
}

// TestScheduleServerRefreshesOnlyAfterTTL verifies the API is asked once per
// TTL, not per request.
func TestScheduleServerRefreshesOnlyAfterTTL(t *testing.T) {
	f := fixtureFetcher()
	clock := &testClock{t: day("2026-07-15")}
	s := NewScheduleServerForTest(f, clock.now, nil)

	schedStatus(s, "2026-07-15") // resolves: today + one walk-back hop
	after := f.calls.Load()
	for i := 0; i < 5; i++ {
		schedStatus(s, "2026-07-15")
	}
	if f.calls.Load() != after {
		t.Errorf("API calls grew from %d to %d within the TTL", after, f.calls.Load())
	}
	clock.advance(stateTTL + time.Minute)
	schedStatus(s, "2026-07-15")
	if got := f.calls.Load(); got != after+1 {
		t.Errorf("after TTL: API calls = %d, want %d (one refresh, anchor reused)", got, after+1)
	}
}

// TestScheduleServerReusesLastStateWhenAPIFails verifies a stale API-derived
// state beats a guess, and that no state at all yields a 503.
func TestScheduleServerReusesLastStateWhenAPIFails(t *testing.T) {
	f := fixtureFetcher()
	clock := &testClock{t: day("2026-07-15")}
	s := NewScheduleServerForTest(f, clock.now, nil)
	if _, body := schedStatus(s, "2026-07-15"); len(gameIDs(decodeSchedule(t, body))) == 0 {
		t.Fatal("setup: want games")
	}

	f.err = errors.New("NHL API down")
	clock.advance(stateTTL + time.Minute)
	code, body := schedStatus(s, "2026-07-15")
	if code != http.StatusOK || len(gameIDs(decodeSchedule(t, body))) == 0 {
		t.Errorf("with the API down and a cached state: status %d, want 200 with games", code)
	}
}

func TestScheduleServerReturns503WithoutAnyState(t *testing.T) {
	s := NewScheduleServerForTest(&fakeBoundaryFetcher{err: errors.New("NHL API down")}, time.Now, nil)
	if code, _ := schedStatus(s, "2026-07-15"); code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 (nothing to go on, no guessing)", code)
	}
	if _, ok := s.StartTime("2025020001"); ok {
		t.Error("StartTime should report false with no season state")
	}
}

// TestScheduleServerConcurrentRequestsShareOneResolution runs under -race.
func TestScheduleServerConcurrentRequestsShareOneResolution(t *testing.T) {
	f := fixtureFetcher()
	s := NewScheduleServerForTest(f, func() time.Time { return day("2026-07-15") }, nil)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); schedStatus(s, "2026-07-15"); s.StartTime("2025020001") }()
	}
	wg.Wait()
	if got := f.calls.Load(); got != 2 {
		t.Errorf("API calls = %d, want 2 (today + one walk-back hop, shared by all requests)", got)
	}
}
