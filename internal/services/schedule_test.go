package services

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"testserver/internal/models"
)

// filterGamesByDate replicates the backend's filterGamesByDate logic
// (watchgameupdates/internal/schedule/fetcher.go) to guard the exact contract
// the emulator must satisfy (D4).
func filterGamesByDate(resp models.ScheduleResponse, date string) []models.ScheduleGame {
	for _, day := range resp.GameWeek {
		if day.Date == date {
			return day.Games
		}
	}
	return nil
}

// getSchedule issues a GET request to the given server URL and path, decoding
// the response body into a ScheduleResponse.
func getSchedule(t *testing.T, srv *httptest.Server, path string) models.ScheduleResponse {
	t.Helper()
	resp, err := srv.Client().Get(srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", path, resp.StatusCode)
	}
	var schedResp models.ScheduleResponse
	if err := json.NewDecoder(resp.Body).Decode(&schedResp); err != nil {
		t.Fatalf("decode response from %s: %v", path, err)
	}
	return schedResp
}

// TestScheduleHandlerRoundTrip is the D4 integration test: it stands up a
// real ScheduleServer backed by the embedded shifted schedule and asserts the
// full round-trip — request → decode → filterGamesByDate → GameState==FUT —
// exactly mirroring how the backend's HTTPScheduleFetcher + scheduler.go
// consume the endpoint.
func TestScheduleHandlerRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(NewScheduleServer().HandleSchedule))
	defer srv.Close()

	// Day 1 of the shifted season must be present and non-empty.
	resp := getSchedule(t, srv, "/v1/schedule/2026-06-29")

	games := filterGamesByDate(resp, "2026-06-29")
	if len(games) == 0 {
		t.Fatal("filterGamesByDate returned no games for 2026-06-29 (Day 1 of shifted season)")
	}

	// D1: every game returned must have GameState=="FUT".
	// This is the exact check that was missing from the original design (scheduler.go:74
	// skips non-FUT games) — if this test fails, the backend silently enqueues nothing.
	for _, g := range games {
		if g.GameState != "FUT" {
			t.Errorf("game %d on 2026-06-29: GameState=%q, want FUT — scheduler.go:74 will skip it",
				g.ID, g.GameState)
		}
	}

	// The response shape must use "gameWeek" with a one-element array containing
	// the requested date, exactly matching the real NHL API's contract.
	if len(resp.GameWeek) != 1 {
		t.Errorf("GameWeek len = %d, want 1 (one day per request)", len(resp.GameWeek))
	}
	if len(resp.GameWeek) > 0 && resp.GameWeek[0].Date != "2026-06-29" {
		t.Errorf("GameWeek[0].Date = %q, want 2026-06-29", resp.GameWeek[0].Date)
	}
}

// TestScheduleHandlerUnknownDateReturnsEmptyGameWeek verifies that a request
// for a date outside any season (the off-season gap, roughly February-May, which
// the shifted season never spans) returns an empty gameWeek — not a 500 or a nil
// that would panic filterGamesByDate. Because replay is year-agnostic, dates like
// 2000-01-01 or 2030-12-31 now DO map to a season instance; only the off-season
// gap is genuinely empty.
func TestScheduleHandlerUnknownDateReturnsEmptyGameWeek(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(NewScheduleServer().HandleSchedule))
	defer srv.Close()

	cases := []string{
		"2026-03-15", // off-season gap (between January tail and June start)
		"2027-05-20", // off-season gap, different year
		"",           // empty date segment
	}
	for _, date := range cases {
		t.Run(date, func(t *testing.T) {
			resp := getSchedule(t, srv, "/v1/schedule/"+date)
			// filterGamesByDate must return nil (no games), not panic.
			games := filterGamesByDate(resp, date)
			if games != nil {
				t.Errorf("filterGamesByDate(%q) = %v, want nil", date, games)
			}
			// GameWeek must be non-nil (an empty slice, not absent) so the
			// JSON serialises as [] not null.
			if resp.GameWeek == nil {
				t.Errorf("GameWeek is nil for date %q, want empty slice", date)
			}
		})
	}
}

// TestScheduleHandlerMidSeasonDate spot-checks a date in the middle of the
// shifted season. Season starts 2026-06-29; mid-season is ~late July 2026.
func TestScheduleHandlerMidSeasonDate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(NewScheduleServer().HandleSchedule))
	defer srv.Close()

	// 2026-07-22 is 23 days into the shifted season (~mid-November 2025 cadence).
	// A busy period with ~6-10 games a night. If it's empty something went wrong.
	resp := getSchedule(t, srv, "/v1/schedule/2026-07-22")
	games := filterGamesByDate(resp, "2026-07-22")

	if len(games) == 0 {
		t.Error("no games found for 2026-07-22 (mid-shifted-season); expected mid-November cadence")
	}
	for _, g := range games {
		if g.GameState != "FUT" {
			t.Errorf("game %d on 2026-07-22: GameState=%q, want FUT", g.ID, g.GameState)
		}
	}
}

// TestScheduleServerTotalGameCount sanity-checks the embedded season has the
// expected number of regular-season games (1312 for the 2025-26 season).
func TestScheduleServerTotalGameCount(t *testing.T) {
	s := NewScheduleServer()
	total := 0
	for _, games := range s.index {
		total += len(games)
	}
	// 1312 real 2025-26 regular-season games, plus 12 synthetic test duplicates
	// (the Day 1 slate of 3 games copied onto 2026-06-25..28 for replay/slicing
	// verification — see upstreamAliases in internal/gamereplay/source.go).
	const wantRealGames = 1312
	const wantDuplicateGames = 12
	const wantGames = wantRealGames + wantDuplicateGames
	if total != wantGames {
		t.Errorf("embedded schedule has %d games, want %d (%d real + %d duplicates)",
			total, wantGames, wantRealGames, wantDuplicateGames)
	}
}

// gameIDs returns the sorted set of game IDs in a schedule response.
func gameIDs(resp models.ScheduleResponse) []int {
	var ids []int
	for _, day := range resp.GameWeek {
		for _, g := range day.Games {
			ids = append(ids, g.ID)
		}
	}
	return ids
}

// TestSeasonStartYear checks the June-boundary season classification.
func TestSeasonStartYear(t *testing.T) {
	cases := []struct {
		date string
		want int
	}{
		{"2026-06-29", 2026}, // Day 1
		{"2026-12-31", 2026}, // December still belongs to the June-start season
		{"2027-01-05", 2026}, // January tail belongs to the previous June's season
		{"2027-06-29", 2027}, // next year's Day 1
		{"2028-01-05", 2027}, // next year's January tail
	}
	for _, c := range cases {
		tm, _ := time.Parse("2006-01-02", c.date)
		if got := seasonStartYear(tm); got != c.want {
			t.Errorf("seasonStartYear(%s) = %d, want %d", c.date, got, c.want)
		}
	}
}

// TestScheduleYearAgnostic verifies the same Day 1 games come back for the
// embedded year and for future years, with only the dates relabeled.
func TestScheduleYearAgnostic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(NewScheduleServer().HandleSchedule))
	defer srv.Close()

	base := getSchedule(t, srv, "/v1/schedule/2026-06-29")
	baseIDs := gameIDs(base)
	if len(baseIDs) == 0 {
		t.Fatal("no games for embedded Day 1 (2026-06-29)")
	}

	for _, year := range []string{"2027", "2028", "2031"} {
		resp := getSchedule(t, srv, "/v1/schedule/"+year+"-06-29")
		ids := gameIDs(resp)
		if len(ids) != len(baseIDs) {
			t.Errorf("%s-06-29: got %d games, want %d", year, len(ids), len(baseIDs))
			continue
		}
		for i := range ids {
			if ids[i] != baseIDs[i] {
				t.Errorf("%s-06-29: game IDs differ from base: %v vs %v", year, ids, baseIDs)
				break
			}
		}
		// Dates must be relabeled to the requested year, and start times shifted.
		if resp.GameWeek[0].Date != year+"-06-29" {
			t.Errorf("%s-06-29: response date = %q", year, resp.GameWeek[0].Date)
		}
		for _, g := range resp.GameWeek[0].Games {
			if g.GameDate != year+"-06-29" {
				t.Errorf("%s-06-29: game %d GameDate = %q, want %s-06-29", year, g.ID, g.GameDate, year)
			}
			if g.StartTimeUTC[:4] == "2026" {
				t.Errorf("%s-06-29: game %d StartTimeUTC not shifted: %s", year, g.ID, g.StartTimeUTC)
			}
		}
	}
}

// TestSeasonCutoff verifies the hard September-30 cutoff: dates on or before
// Sept 30 serve games (if the season has any that day), dates after it serve
// none — even though the embedded data physically continues into January.
func TestSeasonCutoff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(NewScheduleServer().HandleSchedule))
	defer srv.Close()

	// Sept 30 is the last served day and has games.
	last := getSchedule(t, srv, "/v1/schedule/2026-09-30")
	if len(gameIDs(last)) == 0 {
		t.Error("2026-09-30 (cutoff day) returned no games; expected the final slate")
	}

	// Everything after the cutoff is empty, across years, even where the embedded
	// data has games (October and the January tail).
	for _, d := range []string{"2026-10-01", "2026-12-31", "2027-01-05", "2031-10-15"} {
		resp := getSchedule(t, srv, "/v1/schedule/"+d)
		if len(gameIDs(resp)) != 0 {
			t.Errorf("%s is after the Sept 30 cutoff but returned %d games", d, len(gameIDs(resp)))
		}
	}
}

// TestAfterSeasonCutoff checks the boundary classification directly.
func TestAfterSeasonCutoff(t *testing.T) {
	cases := []struct {
		date string
		want bool
	}{
		{"2026-09-30", false}, // last day of September — served
		{"2026-10-01", true},  // first day past cutoff
		{"2026-06-29", false}, // Day 1
		{"2027-01-05", true},  // January tail of the 2026 season — cut off
		{"2027-09-30", false}, // next season's cutoff day — served
		{"2027-10-01", true},  // next season past cutoff
	}
	for _, c := range cases {
		tm, _ := time.Parse("2006-01-02", c.date)
		if got := afterSeasonCutoff(tm); got != c.want {
			t.Errorf("afterSeasonCutoff(%s) = %v, want %v", c.date, got, c.want)
		}
	}
}

// TestStartTimeShiftsWithClock verifies StartTime anchors to the running year.
func TestStartTimeShiftsWithClock(t *testing.T) {
	s := NewScheduleServer()
	// Real Day 1 game; embedded start is in 2026.
	const gameID = "2025020001"
	embedded, ok := s.startTimes[gameID]
	if !ok {
		t.Fatalf("game %s not in embedded start times", gameID)
	}

	// Pretend it is the 2028 season.
	s.now = func() time.Time { return time.Date(2028, 7, 1, 0, 0, 0, 0, time.UTC) }
	got, ok := s.StartTime(gameID)
	if !ok {
		t.Fatal("StartTime returned not-ok")
	}
	want := embedded.AddDate(2028-embeddedSeasonStartYear, 0, 0)
	if !got.Equal(want) {
		t.Errorf("StartTime in 2028 = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}
