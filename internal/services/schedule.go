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

// ScheduleServer serves the shifted-season schedule via GET /v1/schedule/{date},
// mimicking the real NHL API's response shape so the backend's
// HTTPScheduleFetcher works against the emulator without modification.
//
// It also implements gamereplay.StartTimeProvider: StartTime(gameID) returns the
// shifted start time for a game so the replay engine can compute game position.
type ScheduleServer struct {
	// index maps shifted calendar date ("YYYY-MM-DD") to that day's games.
	index map[string][]models.ScheduleGame
	// startTimes maps string game ID to shifted startTimeUTC.
	startTimes map[string]time.Time
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

	return &ScheduleServer{index: index, startTimes: startTimes}
}

// StartTime returns the shifted startTimeUTC for the given string game ID.
// Satisfies gamereplay.StartTimeProvider so the replay cache can compute position
// without importing services (avoids import cycle per A1).
func (s *ScheduleServer) StartTime(gameID string) (time.Time, bool) {
	t, ok := s.startTimes[gameID]
	return t, ok
}

// HandleSchedule serves GET /v1/schedule/{date}.
//
// For a date within the shifted season it returns a one-element gameWeek
// containing that day's games (mirroring what the real NHL API returns for
// the same date). For any other date it returns an empty gameWeek, which
// filterGamesByDate in the backend handles as "no games today".
func (s *ScheduleServer) HandleSchedule(w http.ResponseWriter, r *http.Request) {
	// Extract the date from the trailing path segment ("/v1/schedule/YYYY-MM-DD").
	date := strings.TrimPrefix(r.URL.Path, "/v1/schedule/")

	var resp models.ScheduleResponse
	if games, ok := s.index[date]; ok {
		resp.GameWeek = []models.GameWeekDay{{Date: date, Games: games}}
	} else {
		resp.GameWeek = []models.GameWeekDay{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
