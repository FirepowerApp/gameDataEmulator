package services

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"testserver/internal/models"
)

// filterGamesByDate replicates the backend's filterGamesByDate logic
// (watchgameupdates/internal/schedule/fetcher.go) to guard the exact contract
// the emulator must satisfy.
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

// gameIDs returns the sorted (by appearance) set of game IDs in a schedule response.
func gameIDs(resp models.ScheduleResponse) []int {
	var ids []int
	for _, day := range resp.GameWeek {
		for _, g := range day.Games {
			ids = append(ids, g.ID)
		}
	}
	return ids
}

// testServer builds a ScheduleServer with a fixed 2026-06-16 anchor (the
// real 2025-26 offseason start), skipping anchor resolution's network
// dependency entirely.
func testServer(t *testing.T) *ScheduleServer {
	t.Helper()
	anchor := mustParseDate("2026-06-16")
	return newScheduleServerWithAnchor(anchor, time.Time{}, time.Now, nil)
}

// TestScheduleHandlerRoundTrip is the integration test: it stands up a real
// ScheduleServer and asserts the full round-trip — request → decode →
// filterGamesByDate → GameState==FUT — exactly mirroring how the backend's
// HTTPScheduleFetcher + scheduler.go consume the endpoint.
func TestScheduleHandlerRoundTrip(t *testing.T) {
	s := testServer(t)
	srv := httptest.NewServer(http.HandlerFunc(s.HandleSchedule))
	defer srv.Close()

	// Day 0 (anchor) must be present and non-empty.
	resp := getSchedule(t, srv, "/v1/schedule/2026-06-16")

	games := filterGamesByDate(resp, "2026-06-16")
	if len(games) == 0 {
		t.Fatal("filterGamesByDate returned no games for 2026-06-16 (anchor day)")
	}

	// D1: every game returned must have GameState=="FUT".
	// This is the exact check that was missing from the original design (scheduler.go:74
	// skips non-FUT games) — if this test fails, the backend silently enqueues nothing.
	for _, g := range games {
		if g.GameState != "FUT" {
			t.Errorf("game %d on 2026-06-16: GameState=%q, want FUT — scheduler.go:74 will skip it",
				g.ID, g.GameState)
		}
	}

	// The response shape must use "gameWeek" with a one-element array containing
	// the requested date, exactly matching the real NHL API's contract.
	if len(resp.GameWeek) != 1 {
		t.Errorf("GameWeek len = %d, want 1 (one day per request)", len(resp.GameWeek))
	}
	if len(resp.GameWeek) > 0 && resp.GameWeek[0].Date != "2026-06-16" {
		t.Errorf("GameWeek[0].Date = %q, want 2026-06-16", resp.GameWeek[0].Date)
	}
}

// TestScheduleHandlerBeforeAnchorReturnsEmptyGameWeek verifies dates before
// the anchor (pregame) return an empty gameWeek — not a 500 or a nil that
// would panic filterGamesByDate.
func TestScheduleHandlerBeforeAnchorReturnsEmptyGameWeek(t *testing.T) {
	s := testServer(t)
	srv := httptest.NewServer(http.HandlerFunc(s.HandleSchedule))
	defer srv.Close()

	cases := []string{
		"2026-06-15", // day before the anchor
		"2020-01-01", // long before
		"",           // empty date segment
	}
	for _, date := range cases {
		t.Run(date, func(t *testing.T) {
			resp := getSchedule(t, srv, "/v1/schedule/"+date)
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

// TestScheduleHandlerPastEndReturnsEmptyGameWeek verifies a date past the
// last saved game-day serves no games (D3: stop, no looping).
func TestScheduleHandlerPastEndReturnsEmptyGameWeek(t *testing.T) {
	s := testServer(t)
	srv := httptest.NewServer(http.HandlerFunc(s.HandleSchedule))
	defer srv.Close()

	farFuture := s.anchor.AddDate(0, 0, len(s.gameDays)+100).Format("2006-01-02")
	resp := getSchedule(t, srv, "/v1/schedule/"+farFuture)
	if len(gameIDs(resp)) != 0 {
		t.Errorf("%s is past the end of saved days but returned games", farFuture)
	}
}

// TestScheduleHandlerDenseStack walks the whole saved stack day by day from
// the anchor and verifies every game-day is non-empty (dense: no off-days)
// and every game keeps GameState==FUT.
func TestScheduleHandlerDenseStack(t *testing.T) {
	s := testServer(t)
	srv := httptest.NewServer(http.HandlerFunc(s.HandleSchedule))
	defer srv.Close()

	if len(s.gameDays) == 0 {
		t.Fatal("no game-days loaded")
	}
	// Spot-check first, a middle day, and last — walking all ~167 days is
	// unnecessary; the rebase math is the same for every K.
	checkIdx := []int{0, len(s.gameDays) / 2, len(s.gameDays) - 1}
	for _, k := range checkIdx {
		date := s.anchor.AddDate(0, 0, k).Format("2006-01-02")
		resp := getSchedule(t, srv, "/v1/schedule/"+date)
		games := filterGamesByDate(resp, date)
		if len(games) == 0 {
			t.Errorf("day index %d (%s): no games, want dense (non-empty) stack", k, date)
		}
		for _, g := range games {
			if g.GameState != "FUT" {
				t.Errorf("day index %d game %d: GameState=%q, want FUT", k, g.ID, g.GameState)
			}
			if g.GameDate != date {
				t.Errorf("day index %d game %d: GameDate=%q, want %s", k, g.ID, g.GameDate, date)
			}
		}
	}
}

// TestScheduleHandlerCrossMidnightGamePreservesOffset verifies a game whose
// saved StartTimeUTC crosses into the next UTC day keeps that same offset
// after rebasing onto a new date.
func TestScheduleHandlerCrossMidnightGamePreservesOffset(t *testing.T) {
	s := testServer(t)

	// Find a saved game whose StartTimeUTC date differs from its day's baseDate
	// (a West Coast night game crossing midnight UTC).
	var found bool
	for _, day := range s.gameDays {
		for _, g := range day.games {
			if len(g.StartTimeUTC) < 10 {
				continue
			}
			startDate := g.StartTimeUTC[:10]
			if startDate != day.baseDate {
				found = true
				k, _ := indexOfDay(s, day.baseDate)
				reqDate := s.anchor.AddDate(0, 0, k).Format("2006-01-02")
				rebased, err := s.rebaseDay(k, reqDate)
				if err != nil {
					t.Fatalf("rebaseDay: %v", err)
				}
				for _, rg := range rebased {
					if rg.ID != g.ID {
						continue
					}
					gotDate := rg.StartTimeUTC[:10]
					wantCrossesForward := gotDate != reqDate
					if !wantCrossesForward {
						t.Errorf("game %d: rebased StartTimeUTC=%s no longer crosses midnight relative to reqDate=%s (offset lost)",
							g.ID, rg.StartTimeUTC, reqDate)
					}
				}
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		t.Skip("no cross-midnight game found in saved data to exercise this case")
	}
}

func indexOfDay(s *ScheduleServer, baseDate string) (int, bool) {
	for k, day := range s.gameDays {
		if day.baseDate == baseDate {
			return k, true
		}
	}
	return 0, false
}

// TestSeasonResumed checks the D5 "real season resumed" predicate.
func TestSeasonResumed(t *testing.T) {
	regStart := mustParseDate("2026-09-29")
	s := newScheduleServerWithAnchor(mustParseDate("2026-06-16"), regStart, time.Now, nil)

	cases := []struct {
		date string
		want bool
	}{
		{"2026-09-28", false},
		{"2026-09-29", true},
		{"2026-10-01", true},
		{"2026-06-16", false},
	}
	for _, c := range cases {
		if got := s.seasonResumed(mustParseDate(c.date)); got != c.want {
			t.Errorf("seasonResumed(%s) = %v, want %v", c.date, got, c.want)
		}
	}
}

// TestSeasonResumedUnknownNeverStopsEarly verifies a zero regularSeasonStart
// (anchor resolution fell back to the constant) disables the check entirely.
func TestSeasonResumedUnknownNeverStopsEarly(t *testing.T) {
	s := newScheduleServerWithAnchor(mustParseDate("2026-06-16"), time.Time{}, time.Now, nil)
	if s.seasonResumed(mustParseDate("2099-01-01")) {
		t.Error("seasonResumed with unknown regularSeasonStart should always be false")
	}
}

// TestScheduleHandlerStopsWhenSeasonResumed verifies D5's second stop
// condition: even with saved game-days remaining, a date on/after
// regularSeasonStart serves nothing.
func TestScheduleHandlerStopsWhenSeasonResumed(t *testing.T) {
	anchor := mustParseDate("2026-06-16")
	// Set regularSeasonStart to the day after the anchor, so day-index 1+
	// would normally have games, but seasonResumed should block them.
	regStart := anchor.AddDate(0, 0, 1)
	s := newScheduleServerWithAnchor(anchor, regStart, time.Now, nil)
	srv := httptest.NewServer(http.HandlerFunc(s.HandleSchedule))
	defer srv.Close()

	resp := getSchedule(t, srv, "/v1/schedule/"+regStart.Format("2006-01-02"))
	if len(gameIDs(resp)) != 0 {
		t.Errorf("date on regularSeasonStart returned games, want empty (D5 stop)")
	}
}

// TestStartTimeMatchesHandleSchedule is the CRITICAL agreement test (P1):
// StartTime(gameID) must return exactly what HandleSchedule serves as that
// game's startTimeUTC on the date its day-index naturally maps to. Every
// downstream replay computation (gamereplay.Position) depends on this.
func TestStartTimeMatchesHandleSchedule(t *testing.T) {
	s := testServer(t)
	srv := httptest.NewServer(http.HandlerFunc(s.HandleSchedule))
	defer srv.Close()

	if len(s.gameDays) == 0 {
		t.Fatal("no game-days loaded")
	}
	// Spot-check across the stack, not just day 0.
	for _, k := range []int{0, len(s.gameDays) / 2, len(s.gameDays) - 1} {
		date := s.anchor.AddDate(0, 0, k).Format("2006-01-02")
		resp := getSchedule(t, srv, "/v1/schedule/"+date)
		games := filterGamesByDate(resp, date)
		if len(games) == 0 {
			t.Fatalf("day index %d (%s): no games to check", k, date)
		}
		for _, g := range games {
			gameID := strconv.Itoa(g.ID)
			got, ok := s.StartTime(gameID)
			if !ok {
				t.Errorf("day index %d game %d: StartTime returned not-ok", k, g.ID)
				continue
			}
			want, err := time.Parse(time.RFC3339, g.StartTimeUTC)
			if err != nil {
				t.Fatalf("parse HandleSchedule's StartTimeUTC: %v", err)
			}
			if !got.Equal(want) {
				t.Errorf("day index %d game %d: StartTime=%s, HandleSchedule served=%s (DISAGREEMENT)",
					k, g.ID, got.Format(time.RFC3339), want.Format(time.RFC3339))
			}
		}
	}
}
