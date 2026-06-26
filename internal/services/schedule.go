package services

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"testserver/internal/models"
)

//go:embed data/season_2025-26_shifted.json
var shiftedScheduleJSON []byte

// embeddedSeasonStartYear is the calendar year of Day 1 (late June) in the
// embedded schedule data. The embedded season runs from June of this year into
// January of the next, so the data physically spans 2026 and 2027.
const embeddedSeasonStartYear = 2026

// seasonBoundaryMonth splits the calendar year into "this season" and "the
// previous season's January tail". Any date in June or later belongs to that
// year's season; January-May belongs to the prior year's season. June is safe
// because the embedded content runs late-June -> early-January, with nothing in
// between (no month-day ambiguity around the boundary).
const seasonBoundaryMonth = time.June

// seasonStartYear returns the calendar year of the June season-start for a date.
// This is what makes replay year-agnostic: it identifies which season instance a
// date belongs to regardless of the absolute year.
func seasonStartYear(t time.Time) int {
	if t.Month() >= seasonBoundaryMonth {
		return t.Year()
	}
	return t.Year() - 1
}

// afterSeasonCutoff reports whether a date falls after the season's hard cutoff:
// the last day of September of that season's start year. Pacing of games on or
// before the cutoff is unchanged; dates after it are served as "no games", even
// though the embedded data physically continues into January.
func afterSeasonCutoff(t time.Time) bool {
	cutoff := time.Date(seasonStartYear(t), time.September, 30, 0, 0, 0, 0, time.UTC)
	return t.After(cutoff)
}

// ScheduleServer serves the shifted-season schedule via GET /v1/schedule/{date},
// mimicking the real NHL API's response shape so the backend's
// HTTPScheduleFetcher works against the emulator without modification.
//
// It also implements gamereplay.StartTimeProvider: StartTime(gameID) returns the
// shifted start time for a game so the replay engine can compute game position.
type ScheduleServer struct {
	// index maps the embedded calendar date ("YYYY-MM-DD", in the 2026/2027
	// base season) to that day's games.
	index map[string][]models.ScheduleGame
	// startTimes maps string game ID to its embedded (base-year) startTimeUTC.
	startTimes map[string]time.Time
	// now returns the current time; injectable for testing the year shift.
	now func() time.Time
}

// NewScheduleServer parses the embedded shifted-schedule file and builds the
// date index. It calls log.Fatalf on a malformed embed so the server fails
// loudly at startup rather than silently serving empty responses.
func NewScheduleServer() *ScheduleServer {
	var resp models.ScheduleResponse
	if err := json.Unmarshal(shiftedScheduleJSON, &resp); err != nil {
		log.Fatalf("embedded schedule data is malformed — rebuild with cmd/buildschedule: %v", err)
	}

	index := make(map[string][]models.ScheduleGame, len(resp.GameWeek))
	startTimes := make(map[string]time.Time)
	for _, day := range resp.GameWeek {
		index[day.Date] = day.Games
		for _, g := range day.Games {
			t, err := time.Parse(time.RFC3339, g.StartTimeUTC)
			if err != nil {
				log.Printf("warning: could not parse startTimeUTC %q for game %d: %v", g.StartTimeUTC, g.ID, err)
				continue
			}
			startTimes[fmt.Sprintf("%d", g.ID)] = t
		}
	}

	totalGames := 0
	for _, games := range index {
		totalGames += len(games)
	}
	log.Printf("Shifted schedule loaded: %d days, %d games", len(index), totalGames)

	return &ScheduleServer{index: index, startTimes: startTimes, now: time.Now}
}

// StartTime returns the startTimeUTC for the given game ID, shifted into the
// season instance the emulator is currently running. The play-by-play and stats
// requests carry no date, so "which year" is derived from the current clock —
// this is what lets slicing work in any year without rebuilding the data.
//
// Satisfies gamereplay.StartTimeProvider so the replay cache can compute position
// without importing services (avoids import cycle per A1).
func (s *ScheduleServer) StartTime(gameID string) (time.Time, bool) {
	t, ok := s.startTimes[gameID]
	if !ok {
		return time.Time{}, false
	}
	shift := seasonStartYear(s.now()) - embeddedSeasonStartYear
	return t.AddDate(shift, 0, 0), true
}

// HandleSchedule serves GET /v1/schedule/{date}.
//
// Replay is year-agnostic: the requested date's season (see seasonStartYear) is
// mapped back to the embedded base season, the matching day's games are looked
// up, then relabeled to the requested year before returning. So 2027-06-29,
// 2028-06-29, etc. all return the same Day 1 slate as the embedded 2026-06-29.
//
// A hard cutoff applies: dates after the last day of September (see
// afterSeasonCutoff) return an empty gameWeek even though the embedded data runs
// into January. Games on or before the cutoff keep their original pacing.
//
// For a date outside the season it returns an empty gameWeek, which
// filterGamesByDate in the backend handles as "no games today".
func (s *ScheduleServer) HandleSchedule(w http.ResponseWriter, r *http.Request) {
	// Extract the date from the trailing path segment ("/v1/schedule/YYYY-MM-DD").
	date := strings.TrimPrefix(r.URL.Path, "/v1/schedule/")

	resp := models.ScheduleResponse{GameWeek: []models.GameWeekDay{}}
	if reqT, err := time.Parse("2006-01-02", date); err == nil && !afterSeasonCutoff(reqT) {
		shift := seasonStartYear(reqT) - embeddedSeasonStartYear
		embeddedDate := reqT.AddDate(-shift, 0, 0).Format("2006-01-02")
		if games, ok := s.index[embeddedDate]; ok {
			resp.GameWeek = []models.GameWeekDay{{Date: date, Games: shiftGames(games, shift, date)}}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// shiftGames relabels embedded games into the requested season instance: each
// game's GameDate becomes the requested date and its StartTimeUTC is moved
// forward by shift whole years (preserving month-day and time-of-day). Game IDs
// are unchanged so play-by-play/stats lookups still resolve.
func shiftGames(games []models.ScheduleGame, shift int, reqDate string) []models.ScheduleGame {
	out := make([]models.ScheduleGame, len(games))
	for i, g := range games {
		g.GameDate = reqDate
		if st, err := time.Parse(time.RFC3339, g.StartTimeUTC); err == nil {
			g.StartTimeUTC = st.AddDate(shift, 0, 0).Format(time.RFC3339)
		}
		out[i] = g
	}
	return out
}
