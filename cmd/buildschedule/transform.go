package main

import (
	"sort"

	"testserver/internal/models"
)

// futGameState is the GameState value the backend scheduler requires in order
// to enqueue a game. The completed 2025-26 season returns "OFF" — the
// transform forces every output game to "FUT" so the emulator can present
// these as upcoming games on whatever offseason day it rebases them onto
// (scheduler.go:74 skips non-FUT games).
const futGameState = "FUT"

// regularSeasonGameType is the NHL API gameType for regular-season games.
// The weekly endpoint also returns preseason (1) and playoff (3) games, which
// the transform drops (D2).
const regularSeasonGameType = 2

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

// PrepareGame sets GameDate from its source day's date (the live API omits
// per-game gameDate, so we populate it from the enclosing GameWeekDay.Date)
// and forces GameState to "FUT" (D1). Unlike the prior date-shifted design,
// this does NOT change any dates — the output file stays in real calendar
// dates; the emulator rebases at request time (see internal/services/schedule.go).
func PrepareGame(g models.ScheduleGame, dayDate string) models.ScheduleGame {
	g.GameDate = dayDate
	g.GameState = futGameState
	return g
}

// BuildScheduleResponse groups games by their GameDate into GameWeekDay
// entries, sorted by date (ascending) and by game ID within each day, for
// deterministic output.
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

// TransformSeason filters and groups the raw GameWeekDay entries returned by
// FetchSeason into the final ScheduleResponse — the canonical, unshifted
// season. Both the emulator and the app embed this file verbatim.
func TransformSeason(rawDays []models.GameWeekDay) models.ScheduleResponse {
	var prepared []models.ScheduleGame
	for _, day := range rawDays {
		for _, g := range FilterRegularSeason(day.Games) {
			prepared = append(prepared, PrepareGame(g, day.Date))
		}
	}
	return BuildScheduleResponse(prepared)
}
