package services

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"testserver/internal/models"
)

// TestPlayByPlayServer simulates the NHL play-by-play API
type TestPlayByPlayServer struct {
	mu           sync.Mutex
	currentEvent int
	events       []models.PlayByPlayResponse
}

// TestStatsServer simulates the MoneyPuck statistics API
type TestStatsServer struct {
	mu    sync.Mutex
	stats map[string][]string // gameID -> [homeExpectedGoals, awayExpectedGoals]
}

// NewTestPlayByPlayServer creates a new test play-by-play server with predefined data
func NewTestPlayByPlayServer() *TestPlayByPlayServer {
	return &TestPlayByPlayServer{
		currentEvent: 0,
		events: []models.PlayByPlayResponse{
			{Plays: []models.Play{{TypeDescKey: "faceoff"}}},
			{Plays: []models.Play{{TypeDescKey: "shot-on-goal"}}},
			{Plays: []models.Play{{TypeDescKey: "blocked-shot"}}},
			{Plays: []models.Play{{TypeDescKey: "missed-shot"}}},
			{Plays: []models.Play{{TypeDescKey: "goal"}}},
			{Plays: []models.Play{{TypeDescKey: "hit"}}},
			{Plays: []models.Play{{TypeDescKey: "takeaway"}}},
			{Plays: []models.Play{{TypeDescKey: "giveaway"}}},
			{Plays: []models.Play{{TypeDescKey: "penalty"}}},
			{Plays: []models.Play{{TypeDescKey: "game-end"}}},
		},
	}
}

// NewTestStatsServer creates a new test stats server with predefined data
func NewTestStatsServer() *TestStatsServer {
	return &TestStatsServer{
		stats: map[string][]string{
			"2024030411": {"2.35", "1.87"},
			"2024030412": {"3.12", "2.94"},
			"2024030413": {"1.95", "2.68"},
		},
	}
}

// HandlePlayByPlay simulates the NHL play-by-play API endpoint
func (s *TestPlayByPlayServer) HandlePlayByPlay(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Extract game ID from URL path
	gameID := r.URL.Path[len("/v1/gamecenter/"):]
	if idx := len(gameID) - len("/play-by-play"); idx > 0 && gameID[idx:] == "/play-by-play" {
		gameID = gameID[:idx]
	}

	log.Printf("Test play-by-play server: serving event %d/%d for game %s",
		s.currentEvent+1, len(s.events), gameID)

	// Get current event and advance to next (cycling)
	response := s.events[s.currentEvent]
	s.currentEvent = (s.currentEvent + 1) % len(s.events)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// HandleStats simulates the MoneyPuck statistics API endpoint
func (s *TestStatsServer) HandleStats(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Extract game ID from URL path
	path := r.URL.Path[len("/moneypuck/gameData/20242025/"):]
	gameID := path[:len(path)-4] // Remove .csv extension

	log.Printf("Test stats server: serving stats for game %s", gameID)

	// Get predefined stats or use defaults
	stats, exists := s.stats[gameID]
	if !exists {
		stats = []string{"2.50", "2.50"} // Default values
	}

	// Return CSV format as expected by the fetcher
	csvContent := "homeTeamExpectedGoals,awayTeamExpectedGoals\n" +
		stats[0] + "," + stats[1] + "\n"

	w.Header().Set("Content-Type", "text/csv")
	w.Write([]byte(csvContent))
}
