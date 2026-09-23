package main

import (
	"testing"

	"testserver/internal/models"
)

func TestFilterRegularSeason(t *testing.T) {
	input := []models.ScheduleGame{
		{ID: 1, GameType: 1}, // preseason — must be dropped
		{ID: 2, GameType: 2}, // regular — keep
		{ID: 3, GameType: 3}, // playoff — must be dropped
		{ID: 4, GameType: 2}, // regular — keep
		{ID: 9, GameType: 9}, // Olympic break — drop
	}
	got := FilterRegularSeason(input)
	if len(got) != 2 {
		t.Fatalf("FilterRegularSeason: want 2 games, got %d", len(got))
	}
	for _, g := range got {
		if g.GameType != 2 {
			t.Errorf("FilterRegularSeason returned game with GameType=%d, want 2", g.GameType)
		}
	}
	if got[0].ID != 2 || got[1].ID != 4 {
		t.Errorf("FilterRegularSeason: IDs = %d,%d, want 2,4", got[0].ID, got[1].ID)
	}
}

func TestFilterRegularSeasonNil(t *testing.T) {
	got := FilterRegularSeason(nil)
	if got != nil {
		t.Errorf("FilterRegularSeason(nil) = %v, want nil", got)
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
				{ID: 4, GameType: 3, GameState: "OFF", StartTimeUTC: "2026-04-18T23:00:00Z"}, // playoff — drop
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
			// D2: only gameType==2 should appear.
			if g.GameType != 2 {
				t.Errorf("game %d GameType = %d slipped through filter", g.ID, g.GameType)
			}
		}
	}
	if gameCount != 2 {
		t.Errorf("total games = %d, want 2 (preseason and playoff dropped)", gameCount)
	}

	// Dates are NOT shifted — buildschedule outputs real calendar dates.
	if resp.GameWeek[0].Date != "2025-10-07" {
		t.Errorf("GameWeek[0].Date = %q, want unshifted 2025-10-07", resp.GameWeek[0].Date)
	}
	if resp.GameWeek[0].Games[0].StartTimeUTC != "2025-10-07T21:00:00Z" {
		t.Errorf("StartTimeUTC = %q, want unshifted", resp.GameWeek[0].Games[0].StartTimeUTC)
	}
}
