package services

import (
	"encoding/json"
	"log/slog"
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
	logger   *slog.Logger
}

// TestStatsServer serves GET /moneypuck/gameData/20252026/{gameId}.csv.
// It shares the same Cache as TestPlayByPlayServer so game-end eviction
// coordinates across both feeds (A2/D3).
type TestStatsServer struct {
	cache    *gamereplay.Cache
	provider gamereplay.StartTimeProvider
	clock    func() time.Time
	logger   *slog.Logger
}

// NewGameServers constructs the shared Cache and returns both servers.
// Both servers receive the same *Cache pointer so eviction coordinates
// across the PBP (port 8125) and stats (port 8124) handlers. A nil logger
// falls back to slog.Default().
func NewGameServers(provider gamereplay.StartTimeProvider, logger *slog.Logger) (*TestPlayByPlayServer, *TestStatsServer) {
	if logger == nil {
		logger = slog.Default()
	}
	src := gamereplay.NewSource(logger)
	cache := gamereplay.NewCache(src, logger)
	pbp := &TestPlayByPlayServer{cache: cache, provider: provider, clock: time.Now, logger: logger}
	stats := &TestStatsServer{cache: cache, provider: provider, clock: time.Now, logger: logger}
	return pbp, stats
}

// newGameServersWithClock creates servers with an injectable clock for testing.
func newGameServersWithClock(provider gamereplay.StartTimeProvider, src gamereplay.Source, clock func() time.Time, logger *slog.Logger) (*TestPlayByPlayServer, *TestStatsServer) {
	if logger == nil {
		logger = slog.Default()
	}
	cache := gamereplay.NewCacheForTest(src, clock, logger)
	pbp := &TestPlayByPlayServer{cache: cache, provider: provider, clock: clock, logger: logger}
	stats := &TestStatsServer{cache: cache, provider: provider, clock: clock, logger: logger}
	return pbp, stats
}

// HandlePlayByPlay handles GET /v1/gamecenter/{gameId}/play-by-play.
func (s *TestPlayByPlayServer) HandlePlayByPlay(w http.ResponseWriter, r *http.Request) {
	gameID := extractGameID(r.URL.Path)
	logger := s.logger.With(gamereplay.LogKeyGame, gameID)
	logger.Debug("request received", gamereplay.LogKeyFeed, "pbp", "path", r.URL.Path, "remote", r.RemoteAddr)

	start, now, pos, ok := s.resolvePosition(gameID)
	if !ok {
		// Game not in schedule — return empty plays (graceful, no panic).
		logger.Warn("game not found", gamereplay.LogKeyFeed, "pbp", "path", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(models.PlayByPlayResponse{MaxPeriods: 3, Plays: []models.Play{}})
		return
	}
	logger.Debug("position computed",
		"start", start, "now", now,
		"period", pos.Period, "clock", gamereplay.FormatClock(pos),
		"intermission", pos.InIntermission, "state", gamereplay.StateLabel(pos))

	plays, err := s.cache.GetPBP(r.Context(), gameID, pos)
	if err != nil {
		logger.Error("pbp fetch error", gamereplay.LogKeyFeed, "pbp", "err", err)
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}

	if plays == nil {
		plays = []models.Play{}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(models.PlayByPlayResponse{MaxPeriods: 3, Plays: plays}); err != nil {
		logger.Warn("response encode error", gamereplay.LogKeyFeed, "pbp", "err", err)
		return
	}

	var lastPlayType, lastPlayTime string
	var lastPlayPeriod int
	if len(plays) > 0 {
		lp := plays[len(plays)-1]
		lastPlayType = lp.TypeDescKey
		lastPlayTime = lp.TimeInPeriod
		lastPlayPeriod = lp.PeriodDescriptor.Number
	}
	logger.Info("response summary", gamereplay.LogKeyFeed, "pbp", "plays", len(plays), "state", gamereplay.StateLabel(pos),
		"last_play", lastPlayType, "last_play_period", lastPlayPeriod, "last_play_time", lastPlayTime)
}

// HandleStats handles GET /moneypuck/gameData/20252026/{gameId}.csv.
func (s *TestStatsServer) HandleStats(w http.ResponseWriter, r *http.Request) {
	// Extract game ID from path: /moneypuck/gameData/20252026/{gameId}.csv
	path := strings.TrimPrefix(r.URL.Path, "/moneypuck/gameData/20252026/")
	gameID := strings.TrimSuffix(path, ".csv")
	logger := s.logger.With(gamereplay.LogKeyGame, gameID)
	logger.Debug("request received", gamereplay.LogKeyFeed, "stats", "path", r.URL.Path, "remote", r.RemoteAddr)

	start, now, pos, ok := s.resolvePosition(gameID)
	if !ok {
		logger.Warn("game not found", gamereplay.LogKeyFeed, "stats", "path", r.URL.Path)
		w.Header().Set("Content-Type", "text/csv")
		w.Write([]byte("time,homeTeamGoals,awayTeamGoals,homeTeamExpectedGoals,awayTeamExpectedGoals,homeTeamShootOutGoals,awayTeamShootOutGoals\n0,0,0,0.00,0.00,0,0\n"))
		return
	}
	logger.Debug("position computed",
		"start", start, "now", now,
		"period", pos.Period, "clock", gamereplay.FormatClock(pos),
		"intermission", pos.InIntermission, "state", gamereplay.StateLabel(pos))

	csv, last, err := s.cache.GetMP(r.Context(), gameID, pos)
	if err != nil {
		logger.Error("stats fetch error", gamereplay.LogKeyFeed, "stats", "err", err)
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "text/csv")
	if _, err := w.Write([]byte(csv)); err != nil {
		logger.Warn("response write error", gamereplay.LogKeyFeed, "stats", "err", err)
		return
	}
	// Use the MP row's timestamp rather than the wall-clock position to determine
	// game state: when PBP has triggered eviction, GetMP serves final data even
	// though pos.Ended is still false (wall-clock hasn't reached period 5 yet).
	// Regulation ends at 3*1200=3600s; any row at or past that means the game is over.
	statsState := gamereplay.StateLabel(pos)
	if last.GameSecs >= 3*1200 {
		statsState = "over"
	}
	logger.Info("response summary", gamereplay.LogKeyFeed, "stats", "bytes", len(csv), "state", statsState,
		"home_goals", last.HomeGoals, "away_goals", last.AwayGoals,
		"home_xg", last.HomeExpectedGoals, "away_xg", last.AwayExpectedGoals)
}

// resolvePosition computes the current GamePosition for a game using its
// shifted start time, also returning the raw start/now inputs for logging
// ("why is this game in period 2" is answered by the inputs, not the verdict).
func (s *TestPlayByPlayServer) resolvePosition(gameID string) (start, now time.Time, pos gamereplay.GamePosition, ok bool) {
	start, ok = s.provider.StartTime(gameID)
	if !ok {
		return
	}
	now = s.clock()
	pos = gamereplay.Position(start, now)
	return
}

func (s *TestStatsServer) resolvePosition(gameID string) (start, now time.Time, pos gamereplay.GamePosition, ok bool) {
	start, ok = s.provider.StartTime(gameID)
	if !ok {
		return
	}
	now = s.clock()
	pos = gamereplay.Position(start, now)
	return
}

// extractGameID extracts the game ID from /v1/gamecenter/{gameId}/play-by-play.
func extractGameID(path string) string {
	path = strings.TrimPrefix(path, "/v1/gamecenter/")
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		return path[:idx]
	}
	return path
}
