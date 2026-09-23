package main

import (
	"testing"

	"testserver/internal/models"
)

func TestFilterSeasonGames(t *testing.T) {
	input := []models.ScheduleGame{
		{ID: 1, GameType: 1}, // preseason (no MoneyPuck data) — drop
		{ID: 2, GameType: 2}, // regular — keep
		{ID: 3, GameType: 3}, // playoff — keep
		{ID: 4, GameType: 2}, // regular — keep
		{ID: 9, GameType: 9}, // Olympic break, not an NHL game — drop
	}
	got := FilterSeasonGames(input)
	if len(got) != 3 {
		t.Fatalf("FilterSeasonGames: want 3 games, got %d", len(got))
	}
	for i, wantID := range []int{2, 3, 4} {
		if got[i].ID != wantID {
			t.Errorf("FilterSeasonGames[%d].ID = %d, want %d", i, got[i].ID, wantID)
		}
	}
}

func TestFilterSeasonGamesNil(t *testing.T) {
	if got := FilterSeasonGames(nil); got != nil {
		t.Errorf("FilterSeasonGames(nil) = %v, want nil", got)
	}
}

func TestPrepareGame(t *testing.T) {
	g := models.ScheduleGame{
		ID:           2025020001,
		GameType:     2,
		GameState:    "OFF", // completed season returns OFF
		StartTimeUTC: "2025-10-07T21:00:00Z",
		HomeTeam:     models.Team{Abbrev: "FLA"},
		AwayTeam:     models.Team{Abbrev: "CHI"},
	}

	prepared := PrepareGame(g, "2025-10-07")

	// D1: GameState must be "FUT" — the backend skips non-FUT games.
	if prepared.GameState != "FUT" {
		t.Errorf("GameState = %q, want FUT", prepared.GameState)
	}
	// GameDate should be populated from the day's date. No shifting happens
	// here — that's the emulator's job at request time.
	if prepared.GameDate != "2025-10-07" {
		t.Errorf("GameDate = %q, want 2025-10-07", prepared.GameDate)
	}
	if prepared.StartTimeUTC != "2025-10-07T21:00:00Z" {
		t.Errorf("StartTimeUTC = %q, want unchanged 2025-10-07T21:00:00Z", prepared.StartTimeUTC)
	}
	// ID, GameType, teams must be unchanged.
	if prepared.ID != 2025020001 {
		t.Errorf("ID = %d, want 2025020001", prepared.ID)
	}
	if prepared.HomeTeam.Abbrev != "FLA" || prepared.AwayTeam.Abbrev != "CHI" {
		t.Errorf("teams changed: home=%q away=%q", prepared.HomeTeam.Abbrev, prepared.AwayTeam.Abbrev)
	}
}

func TestBuildScheduleResponse(t *testing.T) {
	games := []models.ScheduleGame{
		{ID: 3, GameDate: "2025-10-07", GameState: "FUT"},
		{ID: 1, GameDate: "2025-10-07", GameState: "FUT"},
		{ID: 5, GameDate: "2025-10-08", GameState: "FUT"},
		{ID: 2, GameDate: "2025-10-07", GameState: "FUT"},
	}
	resp := BuildScheduleResponse(games)

	if len(resp.GameWeek) != 2 {
		t.Fatalf("GameWeek len = %d, want 2", len(resp.GameWeek))
	}
	day0 := resp.GameWeek[0]
	if day0.Date != "2025-10-07" {
		t.Errorf("GameWeek[0].Date = %q, want 2025-10-07", day0.Date)
	}
	// Within a day, games should be sorted by ID ascending.
	if day0.Games[0].ID != 1 || day0.Games[1].ID != 2 || day0.Games[2].ID != 3 {
		t.Errorf("GameWeek[0] IDs = %d,%d,%d, want 1,2,3",
			day0.Games[0].ID, day0.Games[1].ID, day0.Games[2].ID)
	}
	if resp.GameWeek[1].Date != "2025-10-08" {
		t.Errorf("GameWeek[1].Date = %q, want 2025-10-08", resp.GameWeek[1].Date)
	}
}

func TestTransformSeason(t *testing.T) {
	rawDays := []models.GameWeekDay{
		{
			Date: "2025-10-07",
			Games: []models.ScheduleGame{
				{ID: 1, GameType: 2, GameState: "OFF", StartTimeUTC: "2025-10-07T21:00:00Z"},
				{ID: 2, GameType: 1, GameState: "OFF", StartTimeUTC: "2025-10-07T23:00:00Z"}, // preseason — drop
			},
		},
		{
			Date: "2026-04-18",
			Games: []models.ScheduleGame{
				{ID: 3, GameType: 2, GameState: "OFF", StartTimeUTC: "2026-04-18T22:00:00Z"},
				{ID: 4, GameType: 3, GameState: "OFF", StartTimeUTC: "2026-04-18T23:00:00Z"}, // playoff — keep
				{ID: 5, GameType: 9, GameState: "OFF", StartTimeUTC: "2026-04-18T23:30:00Z"}, // Olympic break — drop
			},
		},
	}

	resp := TransformSeason(rawDays)

	gameCount := 0
	for _, day := range resp.GameWeek {
		for _, g := range day.Games {
			gameCount++
			// D1: every output game must be FUT.
			if g.GameState != "FUT" {
				t.Errorf("game %d GameState = %q, want FUT", g.ID, g.GameState)
			}
			// D2: non-NHL games must not appear.
			if !isSeasonGame(g) {
				t.Errorf("game %d GameType = %d slipped through filter", g.ID, g.GameType)
			}
		}
	}
	if gameCount != 3 {
		t.Errorf("total games = %d, want 3 (preseason and Olympic-break dropped)", gameCount)
	}

	// Dates are NOT shifted — buildschedule outputs real calendar dates.
	if resp.GameWeek[0].Date != "2025-10-07" {
		t.Errorf("GameWeek[0].Date = %q, want unshifted 2025-10-07", resp.GameWeek[0].Date)
	}
	if resp.GameWeek[0].Games[0].StartTimeUTC != "2025-10-07T21:00:00Z" {
		t.Errorf("StartTimeUTC = %q, want unshifted", resp.GameWeek[0].Games[0].StartTimeUTC)
	}
}
