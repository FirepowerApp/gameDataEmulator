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
	stats map[string][]string // gameID -> [homeGoals, awayGoals, homeExpectedGoals, awayExpectedGoals, homeShootOutGoals, awayShootOutGoals]
}

// NewTestPlayByPlayServer creates a new test play-by-play server with predefined data
func NewTestPlayByPlayServer() *TestPlayByPlayServer {
	return &TestPlayByPlayServer{
		currentEvent: 0,
		events: []models.PlayByPlayResponse{
			{Plays: []models.Play{{TypeDescKey: "faceoff", Time: 0, PeriodDescriptor: models.PeriodDescriptor{Number: 1, PeriodType: "REG", MaxRegulationPeriods: 3}, TimeInPeriod: "00:00", TimeRemaining: "20:00"}}},
			{Plays: []models.Play{{TypeDescKey: "shot-on-goal", Time: 400, PeriodDescriptor: models.PeriodDescriptor{Number: 1, PeriodType: "REG", MaxRegulationPeriods: 3}, TimeInPeriod: "06:40", TimeRemaining: "13:20"}}},
			{Plays: []models.Play{{TypeDescKey: "blocked-shot", Time: 800, PeriodDescriptor: models.PeriodDescriptor{Number: 1, PeriodType: "REG", MaxRegulationPeriods: 3}, TimeInPeriod: "13:20", TimeRemaining: "06:40"}}},
			{Plays: []models.Play{{TypeDescKey: "missed-shot", Time: 1200, PeriodDescriptor: models.PeriodDescriptor{Number: 2, PeriodType: "REG", MaxRegulationPeriods: 3}, TimeInPeriod: "00:00", TimeRemaining: "20:00"}}},
			{Plays: []models.Play{{TypeDescKey: "goal", Time: 1600, PeriodDescriptor: models.PeriodDescriptor{Number: 2, PeriodType: "REG", MaxRegulationPeriods: 3}, TimeInPeriod: "06:40", TimeRemaining: "13:20"}}},
			{Plays: []models.Play{{TypeDescKey: "hit", Time: 2000, PeriodDescriptor: models.PeriodDescriptor{Number: 2, PeriodType: "REG", MaxRegulationPeriods: 3}, TimeInPeriod: "13:20", TimeRemaining: "06:40"}}},
			{Plays: []models.Play{{TypeDescKey: "takeaway", Time: 2400, PeriodDescriptor: models.PeriodDescriptor{Number: 3, PeriodType: "REG", MaxRegulationPeriods: 3}, TimeInPeriod: "00:00", TimeRemaining: "20:00"}}},
			{Plays: []models.Play{{TypeDescKey: "giveaway", Time: 2800, PeriodDescriptor: models.PeriodDescriptor{Number: 3, PeriodType: "REG", MaxRegulationPeriods: 3}, TimeInPeriod: "06:40", TimeRemaining: "13:20"}}},
			{Plays: []models.Play{{TypeDescKey: "penalty", Time: 3200, PeriodDescriptor: models.PeriodDescriptor{Number: 3, PeriodType: "REG", MaxRegulationPeriods: 3}, TimeInPeriod: "13:20", TimeRemaining: "06:40"}}},
			{Plays: []models.Play{{TypeDescKey: "game-end", Time: 3600, PeriodDescriptor: models.PeriodDescriptor{Number: 3, PeriodType: "REG", MaxRegulationPeriods: 3}, TimeInPeriod: "20:00", TimeRemaining: "00:00"}}},
		},
	}
}

// NewTestStatsServer creates a new test stats server with predefined data
func NewTestStatsServer() *TestStatsServer {
	return &TestStatsServer{
		stats: map[string][]string{
			// Regular game - no shootout
			"2024030411": {"3", "2", "2.35", "1.87", "0", "0"},
			// Shootout game - home team wins in shootout
			"2024030412": {"2", "2", "3.12", "2.94", "2", "1"},
			// Additional game
			"2024030413": {"4", "3", "1.95", "2.68", "0", "0"},
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
		stats = []string{"3", "2", "2.50", "2.50", "0", "0"} // Default values
	}

	// Return CSV format as expected by the fetcher
	csvContent := "homeTeamGoals,awayTeamGoals,homeTeamExpectedGoals,awayTeamExpectedGoals,homeTeamShootOutGoals,awayTeamShootOutGoals\n" +
		stats[0] + "," + stats[1] + "," + stats[2] + "," + stats[3] + "," + stats[4] + "," + stats[5] + "\n"

	w.Header().Set("Content-Type", "text/csv")
	w.Write([]byte(csvContent))
}
