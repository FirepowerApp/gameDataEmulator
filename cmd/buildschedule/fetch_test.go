package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"testing"

	"testserver/internal/models"
)

// makeWeekResponse builds a synthetic weekResponse with n type-2 games for
// the given date, pointing nextStartDate at the following week.
func makeWeekResponse(date, nextDate string, type2Count int) weekResponse {
	var days []models.GameWeekDay
	if type2Count > 0 {
		games := make([]models.ScheduleGame, type2Count)
		for i := range games {
			games[i] = models.ScheduleGame{ID: i + 1, GameType: 2, GameState: "OFF", StartTimeUTC: date + "T21:00:00Z"}
		}
		days = []models.GameWeekDay{{Date: date, Games: games}}
	}
	return weekResponse{NextStartDate: nextDate, GameWeek: days}
}

// TestFetchSeasonCachesAndStopsAfterThreeConsecutiveEmptyWeeks verifies the
// three invariants of FetchSeason:
//  1. Raw responses are cached to rawDir.
//  2. The nextStartDate chain is followed.
//  3. Fetching stops after maxConsecutiveEmptyWeeks zero-type2 weeks.
func TestFetchSeasonCachesAndStopsAfterThreeConsecutiveEmptyWeeks(t *testing.T) {
	// Build a sequence: 2 regular weeks → 3 empty weeks (should stop here).
	// The server records which dates were requested to verify the chain.
	requested := []string{}
	weeks := map[string]weekResponse{
		"2025-10-07": makeWeekResponse("2025-10-07", "2025-10-14", 3),
		"2025-10-14": makeWeekResponse("2025-10-14", "2025-10-21", 2),
		"2025-10-21": makeWeekResponse("2025-10-21", "2025-10-28", 0), // empty 1
		"2025-10-28": makeWeekResponse("2025-10-28", "2025-11-04", 0), // empty 2
		"2025-11-04": makeWeekResponse("2025-11-04", "2025-11-11", 0), // empty 3 → stop
		"2025-11-11": makeWeekResponse("2025-11-11", "", 5),            // should NOT be fetched
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		date := path.Base(r.URL.Path)
		requested = append(requested, date)
		resp, ok := weeks[date]
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	rawDir := t.TempDir()
	ctx := context.Background()

	days, err := FetchSeason(ctx, srv.Client(), srv.URL, "2025-10-07", rawDir)
	if err != nil {
		t.Fatalf("FetchSeason: %v", err)
	}

	// Should have fetched weeks 1–5 (stopped before week 6).
	if len(requested) != 5 {
		t.Errorf("HTTP requests = %d, want 5 (stopped after 3 consecutive empty weeks): %v", len(requested), requested)
	}

	// Last request should be the third empty week, not the week after.
	if len(requested) > 0 && requested[len(requested)-1] != "2025-11-04" {
		t.Errorf("last requested date = %q, want 2025-11-04", requested[len(requested)-1])
	}

	// Days from the 2 regular weeks should be present.
	type2Games := 0
	for _, day := range days {
		for _, g := range day.Games {
			if g.GameType == 2 {
				type2Games++
			}
		}
	}
	if type2Games != 5 {
		t.Errorf("type2 games = %d, want 5", type2Games)
	}
}

// TestFetchSeasonReusesCache verifies that a second FetchSeason call with the
// same rawDir does not issue any HTTP requests (fully cached).
func TestFetchSeasonReusesCache(t *testing.T) {
	httpCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalls++
		// Single week, no next page.
		resp := makeWeekResponse("2025-10-07", "", 2)
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	rawDir := t.TempDir()
	ctx := context.Background()

	// First call — should hit the server once.
	_, err := FetchSeason(ctx, srv.Client(), srv.URL, "2025-10-07", rawDir)
	if err != nil {
		t.Fatalf("first FetchSeason: %v", err)
	}
	if httpCalls != 1 {
		t.Errorf("first run: HTTP calls = %d, want 1", httpCalls)
	}

	// Second call — must not hit the server.
	httpCalls = 0
	_, err = FetchSeason(ctx, srv.Client(), srv.URL, "2025-10-07", rawDir)
	if err != nil {
		t.Fatalf("second FetchSeason: %v", err)
	}
	if httpCalls != 0 {
		t.Errorf("second run (cache hit): HTTP calls = %d, want 0", httpCalls)
	}

	// Cache file must exist.
	entries, _ := os.ReadDir(rawDir)
	if len(entries) == 0 {
		t.Error("rawDir is empty — cache file was not written")
	}
}

// TestFetchSeasonPropagatesHTTPError verifies that a non-200 response is
// surfaced as an error rather than silently producing an empty result.
func TestFetchSeasonPropagatesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	_, err := FetchSeason(context.Background(), srv.Client(), srv.URL, "2025-10-07", t.TempDir())
	if err == nil {
		t.Error("expected error on HTTP 429, got nil")
	}
}
