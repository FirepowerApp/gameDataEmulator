package services

import (
	_ "embed"
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"testserver/internal/models"
)

//go:embed data/season_2025-26_shifted.json
var shiftedScheduleJSON []byte

// ScheduleServer serves the shifted-season schedule via GET /v1/schedule/{date},
// mimicking the real NHL API's response shape so the backend's
// HTTPScheduleFetcher works against the emulator without modification.
type ScheduleServer struct {
	// index maps shifted calendar date ("YYYY-MM-DD") to that day's games.
	// Built once at startup from the embedded schedule file.
	index map[string][]models.ScheduleGame
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
	for _, day := range resp.GameWeek {
		index[day.Date] = day.Games
	}

	totalGames := 0
	for _, games := range index {
		totalGames += len(games)
	}
	log.Printf("Shifted schedule loaded: %d days, %d games", len(index), totalGames)

	return &ScheduleServer{index: index}
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
