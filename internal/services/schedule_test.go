package services

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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
// for a date outside the shifted season returns an empty gameWeek (not a 500
// or nil that would cause a panic in filterGamesByDate).
func TestScheduleHandlerUnknownDateReturnsEmptyGameWeek(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(NewScheduleServer().HandleSchedule))
	defer srv.Close()

	cases := []string{
		"2000-01-01", // before the shifted season
		"2030-12-31", // after the shifted season
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
	const wantGames = 1312
	if total != wantGames {
		t.Errorf("embedded schedule has %d games, want %d", total, wantGames)
	}
}
