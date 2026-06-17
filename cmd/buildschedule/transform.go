package main

import (
	"fmt"
	"sort"
	"time"

	"testserver/internal/models"
)

// futGameState is the GameState value the backend scheduler requires in order
// to enqueue a game. The completed 2025-26 season returns "OFF" — the
// transform forces every output game to "FUT" (D1 / scheduler.go:74).
const futGameState = "FUT"

// regularSeasonGameType is the NHL API gameType for regular-season games.
// The weekly endpoint also returns preseason (1) and playoff (3) games, which
// the transform drops (D2).
const regularSeasonGameType = 2

// nyLocation is America/New_York, the NHL's primary scheduling timezone.
// Shifting dates in this location (rather than raw UTC) preserves each game's
// local wall-clock start time across the EST↔EDT DST boundary (D3 / Open Q4).
var nyLocation = func() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		panic(fmt.Sprintf("time.LoadLocation(America/New_York): %v", err))
	}
	return loc
}()

// ComputeOffsetDays returns the number of calendar days to add to every
// original date so that day1 maps to targetDay1 (both "YYYY-MM-DD").
func ComputeOffsetDays(day1, targetDay1 string) (int, error) {
	d1, err := time.Parse("2006-01-02", day1)
	if err != nil {
		return 0, fmt.Errorf("invalid day1 %q: %w", day1, err)
	}
	d2, err := time.Parse("2006-01-02", targetDay1)
	if err != nil {
		return 0, fmt.Errorf("invalid targetDay1 %q: %w", targetDay1, err)
	}
	// Both parsed as UTC midnight, so Sub is exact (no DST in UTC).
	return int(d2.Sub(d1).Hours() / 24), nil
}

// FilterRegularSeason keeps only regular-season games (gameType==2), dropping
// preseason and playoff entries that appear in the same weekly response (D2).
func FilterRegularSeason(games []models.ScheduleGame) []models.ScheduleGame {
	var out []models.ScheduleGame
	for _, g := range games {
		if g.GameType == regularSeasonGameType {
			out = append(out, g)
		}
	}
	return out
}

// ShiftDate shifts a "YYYY-MM-DD" date string by offsetDays calendar days.
func ShiftDate(date string, offsetDays int) (string, error) {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return "", fmt.Errorf("invalid date %q: %w", date, err)
	}
	return t.AddDate(0, 0, offsetDays).Format("2006-01-02"), nil
}

// ShiftStartTimeUTC shifts an RFC3339 UTC timestamp by offsetDays calendar
// days, preserving the America/New_York wall-clock time-of-day across DST
// transitions (D3 / Open Q4).
//
// Concretely: a 7 PM EST start in November (UTC-5) becomes a 7 PM EDT start
// after the shift (UTC-4) — the UTC timestamp changes by 23 h, not 24 h, but
// the local game time is unchanged from the fans' perspective.
func ShiftStartTimeUTC(startTimeUTC string, offsetDays int) (string, error) {
	t, err := time.Parse(time.RFC3339, startTimeUTC)
	if err != nil {
		return "", fmt.Errorf("invalid startTimeUTC %q: %w", startTimeUTC, err)
	}
	// Convert to NY local, add calendar days (Go's AddDate recomputes the DST
	// offset for the resulting date), then convert back to UTC.
	shifted := t.In(nyLocation).AddDate(0, 0, offsetDays)
	return shifted.UTC().Format(time.RFC3339), nil
}

// ShiftGame applies the full transform to a single game:
//   - sets GameDate from its source day's date (the live API omits per-game
//     gameDate, so we populate it from the enclosing GameWeekDay.Date)
//   - shifts GameDate and StartTimeUTC by offsetDays (DST-aware for StartTimeUTC)
//   - forces GameState to "FUT" (D1)
func ShiftGame(g models.ScheduleGame, dayDate string, offsetDays int) (models.ScheduleGame, error) {
	shiftedDate, err := ShiftDate(dayDate, offsetDays)
	if err != nil {
		return models.ScheduleGame{}, err
	}
	shiftedStart, err := ShiftStartTimeUTC(g.StartTimeUTC, offsetDays)
	if err != nil {
		return models.ScheduleGame{}, err
	}
	g.GameDate = shiftedDate
	g.StartTimeUTC = shiftedStart
	g.GameState = futGameState
	return g, nil
}

// BuildScheduleResponse groups shifted games by their GameDate into
// GameWeekDay entries, sorted by date (ascending) and by game ID within each
// day, for deterministic output.
func BuildScheduleResponse(games []models.ScheduleGame) models.ScheduleResponse {
	byDate := make(map[string][]models.ScheduleGame, 200)
	for _, g := range games {
		byDate[g.GameDate] = append(byDate[g.GameDate], g)
	}

	dates := make([]string, 0, len(byDate))
	for date := range byDate {
		dates = append(dates, date)
	}
	sort.Strings(dates)

	resp := models.ScheduleResponse{GameWeek: make([]models.GameWeekDay, 0, len(dates))}
	for _, date := range dates {
		dayGames := byDate[date]
		sort.Slice(dayGames, func(i, j int) bool { return dayGames[i].ID < dayGames[j].ID })
		resp.GameWeek = append(resp.GameWeek, models.GameWeekDay{
			Date:  date,
			Games: dayGames,
		})
	}
	return resp
}

// TransformSeason filters, shifts, and groups the raw GameWeekDay entries
// returned by FetchSeason into the final ScheduleResponse.
func TransformSeason(rawDays []models.GameWeekDay, offsetDays int) (models.ScheduleResponse, error) {
	var shifted []models.ScheduleGame
	for _, day := range rawDays {
		for _, g := range FilterRegularSeason(day.Games) {
			s, err := ShiftGame(g, day.Date, offsetDays)
			if err != nil {
				return models.ScheduleResponse{}, fmt.Errorf("shift game %d on %s: %w", g.ID, day.Date, err)
			}
			shifted = append(shifted, s)
		}
	}
	return BuildScheduleResponse(shifted), nil
}
