package main

import (
	"testing"

	"testserver/internal/models"
)

func TestComputeOffsetDays(t *testing.T) {
	tests := []struct {
		day1, target string
		want         int
		wantErr      bool
	}{
		{day1: "2025-10-07", target: "2026-06-22", want: 258},
		{day1: "2025-01-01", target: "2025-01-01", want: 0},
		{day1: "2025-01-01", target: "2025-01-02", want: 1},
		{day1: "bad-date", target: "2026-06-22", wantErr: true},
	}
	for _, tt := range tests {
		got, err := ComputeOffsetDays(tt.day1, tt.target)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ComputeOffsetDays(%q, %q): expected error, got nil", tt.day1, tt.target)
			}
			continue
		}
		if err != nil {
			t.Errorf("ComputeOffsetDays(%q, %q): unexpected error: %v", tt.day1, tt.target, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ComputeOffsetDays(%q, %q) = %d, want %d", tt.day1, tt.target, got, tt.want)
		}
	}
}

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

func TestShiftDate(t *testing.T) {
	tests := []struct {
		date       string
		offsetDays int
		want       string
		wantErr    bool
	}{
		{"2025-10-07", 258, "2026-06-22", false},
		{"2026-04-18", 258, "2027-01-01", false}, // last regular-season day → New Year's Day
		{"2025-12-31", 1, "2026-01-01", false},   // year rollover
		{"bad", 1, "", true},
	}
	for _, tt := range tests {
		got, err := ShiftDate(tt.date, tt.offsetDays)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ShiftDate(%q, %d): expected error", tt.date, tt.offsetDays)
			}
			continue
		}
		if err != nil {
			t.Errorf("ShiftDate(%q, %d): %v", tt.date, tt.offsetDays, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ShiftDate(%q, %d) = %q, want %q", tt.date, tt.offsetDays, got, tt.want)
		}
	}
}

func TestShiftStartTimeUTC(t *testing.T) {
	tests := []struct {
		name         string
		startTimeUTC string
		offsetDays   int
		want         string
		wantErr      bool
	}{
		{
			// EDT → EDT: both sides of the shift are in Eastern Daylight Time
			// (Oct 7 2025 and Jun 22 2026 are both UTC-4). Wall clock preserved,
			// UTC instant shifts by exactly 258×24h.
			name:         "EDT to EDT (no DST change)",
			startTimeUTC: "2025-10-07T21:00:00Z",
			offsetDays:   258,
			want:         "2026-06-22T21:00:00Z",
		},
		{
			// EST → EDT: Nov 30 2025 is EST (UTC-5), so 19:00 local = 00:00 UTC Dec 1.
			// Aug 15 2026 is EDT (UTC-4), so 19:00 local = 23:00 UTC.
			// A naive UTC shift would give 2026-08-16T00:00:00Z (off by 1 h).
			name:         "EST to EDT (DST-aware: wall clock preserved, UTC shifts by 23h not 24h)",
			startTimeUTC: "2025-12-01T00:00:00Z",
			offsetDays:   258,
			want:         "2026-08-15T23:00:00Z",
		},
		{
			// Second EST-tail case: deep winter game.
			// 2026-01-15T02:00:00Z = Jan 14 2026 21:00 EST (UTC-5).
			// Jan 14 + 258 calendar days = Sep 29 2026.
			// Sep 29 2026 is EDT (UTC-4): 21:00 EDT = 2026-09-30T01:00:00Z.
			// A naive UTC shift would give 2026-09-30T02:00:00Z (off by 1 h).
			name:         "deep winter EST game (D4 required EST-tail coverage)",
			startTimeUTC: "2026-01-15T02:00:00Z",
			offsetDays:   258,
			want:         "2026-09-30T01:00:00Z",
		},
		{
			name:         "bad timestamp",
			startTimeUTC: "not-a-time",
			offsetDays:   1,
			wantErr:      true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ShiftStartTimeUTC(tt.startTimeUTC, tt.offsetDays)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("ShiftStartTimeUTC(%q, %d) = %q, want %q",
					tt.startTimeUTC, tt.offsetDays, got, tt.want)
			}
		})
	}
}

func TestShiftGame(t *testing.T) {
	g := models.ScheduleGame{
		ID:           2025020001,
		GameType:     2,
		GameState:    "OFF",    // completed season returns OFF
		StartTimeUTC: "2025-10-07T21:00:00Z",
		HomeTeam:     models.Team{Abbrev: "FLA"},
		AwayTeam:     models.Team{Abbrev: "CHI"},
	}

	shifted, err := ShiftGame(g, "2025-10-07", 258)
	if err != nil {
		t.Fatalf("ShiftGame: %v", err)
	}

	// D1: GameState must be "FUT" — the backend skips non-FUT games.
	if shifted.GameState != "FUT" {
		t.Errorf("GameState = %q, want FUT", shifted.GameState)
	}
	// GameDate should be populated from the day's date, shifted.
	if shifted.GameDate != "2026-06-22" {
		t.Errorf("GameDate = %q, want 2026-06-22", shifted.GameDate)
	}
	// StartTimeUTC should be shifted (EDT→EDT, same UTC time, just +258 days).
	if shifted.StartTimeUTC != "2026-06-22T21:00:00Z" {
		t.Errorf("StartTimeUTC = %q, want 2026-06-22T21:00:00Z", shifted.StartTimeUTC)
	}
	// ID, GameType, teams must be unchanged.
	if shifted.ID != 2025020001 {
		t.Errorf("ID = %d, want 2025020001", shifted.ID)
	}
	if shifted.HomeTeam.Abbrev != "FLA" || shifted.AwayTeam.Abbrev != "CHI" {
		t.Errorf("teams changed: home=%q away=%q", shifted.HomeTeam.Abbrev, shifted.AwayTeam.Abbrev)
	}
}

func TestBuildScheduleResponse(t *testing.T) {
	games := []models.ScheduleGame{
		{ID: 3, GameDate: "2026-06-22", GameState: "FUT"},
		{ID: 1, GameDate: "2026-06-22", GameState: "FUT"},
		{ID: 5, GameDate: "2026-06-23", GameState: "FUT"},
		{ID: 2, GameDate: "2026-06-22", GameState: "FUT"},
	}
	resp := BuildScheduleResponse(games)

	if len(resp.GameWeek) != 2 {
		t.Fatalf("GameWeek len = %d, want 2", len(resp.GameWeek))
	}
	day0 := resp.GameWeek[0]
	if day0.Date != "2026-06-22" {
		t.Errorf("GameWeek[0].Date = %q, want 2026-06-22", day0.Date)
	}
	// Within a day, games should be sorted by ID ascending.
	if day0.Games[0].ID != 1 || day0.Games[1].ID != 2 || day0.Games[2].ID != 3 {
		t.Errorf("GameWeek[0] IDs = %d,%d,%d, want 1,2,3",
			day0.Games[0].ID, day0.Games[1].ID, day0.Games[2].ID)
	}
	if resp.GameWeek[1].Date != "2026-06-23" {
		t.Errorf("GameWeek[1].Date = %q, want 2026-06-23", resp.GameWeek[1].Date)
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

	resp, err := TransformSeason(rawDays, 258)
	if err != nil {
		t.Fatalf("TransformSeason: %v", err)
	}

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
}
