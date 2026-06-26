package services

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"testserver/internal/gamereplay"
	"testserver/internal/models"
)

// TestPlayByPlayServer serves GET /v1/gamecenter/{gameId}/play-by-play.
// It returns the subset of plays that would have occurred by the current
// wall-clock, anchored to the game's shifted startTimeUTC from the schedule.
type TestPlayByPlayServer struct {
	cache    *gamereplay.Cache
	provider gamereplay.StartTimeProvider
	clock    func() time.Time
}

// TestStatsServer serves GET /moneypuck/gameData/20252026/{gameId}.csv.
// It shares the same Cache as TestPlayByPlayServer so game-end eviction
// coordinates across both feeds (A2/D3).
type TestStatsServer struct {
	cache    *gamereplay.Cache
	provider gamereplay.StartTimeProvider
	clock    func() time.Time
}

// NewGameServers constructs the shared Cache and returns both servers.
// Both servers receive the same *Cache pointer so eviction coordinates
// across the PBP (port 8125) and stats (port 8124) handlers.
func NewGameServers(provider gamereplay.StartTimeProvider) (*TestPlayByPlayServer, *TestStatsServer) {
	src := gamereplay.NewSource()
	cache := gamereplay.NewCache(src)
	pbp := &TestPlayByPlayServer{cache: cache, provider: provider, clock: time.Now}
	stats := &TestStatsServer{cache: cache, provider: provider, clock: time.Now}
	return pbp, stats
}

// newGameServersWithClock creates servers with an injectable clock for testing.
func newGameServersWithClock(provider gamereplay.StartTimeProvider, src gamereplay.Source, clock func() time.Time) (*TestPlayByPlayServer, *TestStatsServer) {
	cache := gamereplay.NewCacheForTest(src, clock)
	pbp := &TestPlayByPlayServer{cache: cache, provider: provider, clock: clock}
	stats := &TestStatsServer{cache: cache, provider: provider, clock: clock}
	return pbp, stats
}

// HandlePlayByPlay handles GET /v1/gamecenter/{gameId}/play-by-play.
func (s *TestPlayByPlayServer) HandlePlayByPlay(w http.ResponseWriter, r *http.Request) {
	gameID := extractGameID(r.URL.Path)
	pos, ok := s.position(gameID)
	if !ok {
		// Game not in schedule — return empty plays (graceful, no panic).
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(models.PlayByPlayResponse{Plays: []models.Play{}})
		return
	}

	plays, err := s.cache.GetPBP(r.Context(), gameID, pos)
	if err != nil {
		log.Printf("PBP fetch error for game %s: %v", gameID, err)
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}

	if plays == nil {
		plays = []models.Play{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(models.PlayByPlayResponse{Plays: plays})
}

// HandleStats handles GET /moneypuck/gameData/20252026/{gameId}.csv.
func (s *TestStatsServer) HandleStats(w http.ResponseWriter, r *http.Request) {
	// Extract game ID from path: /moneypuck/gameData/20252026/{gameId}.csv
	path := strings.TrimPrefix(r.URL.Path, "/moneypuck/gameData/20252026/")
	gameID := strings.TrimSuffix(path, ".csv")

	pos, ok := s.position(gameID)
	if !ok {
		w.Header().Set("Content-Type", "text/csv")
		w.Write([]byte("time,homeTeamGoals,awayTeamGoals,homeTeamExpectedGoals,awayTeamExpectedGoals,homeTeamShootOutGoals,awayTeamShootOutGoals\n0,0,0,0.00,0.00,0,0\n"))
		return
	}

	csv, err := s.cache.GetMP(r.Context(), gameID, pos)
	if err != nil {
		log.Printf("stats fetch error for game %s: %v", gameID, err)
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "text/csv")
	w.Write([]byte(csv))
}

// position computes the current GamePosition for a game using its shifted start time.
func (s *TestPlayByPlayServer) position(gameID string) (gamereplay.GamePosition, bool) {
	start, ok := s.provider.StartTime(gameID)
	if !ok {
		return gamereplay.GamePosition{}, false
	}
	return gamereplay.Position(start, s.clock()), true
}

func (s *TestStatsServer) position(gameID string) (gamereplay.GamePosition, bool) {
	start, ok := s.provider.StartTime(gameID)
	if !ok {
		return gamereplay.GamePosition{}, false
	}
	return gamereplay.Position(start, s.clock()), true
}

// extractGameID extracts the game ID from /v1/gamecenter/{gameId}/play-by-play.
func extractGameID(path string) string {
	path = strings.TrimPrefix(path, "/v1/gamecenter/")
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		return path[:idx]
	}
	return path
}
