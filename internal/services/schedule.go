package services

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"testserver/internal/dateshift"
	"testserver/internal/models"
)

//go:embed data/season_2025-26.json
var seasonJSON []byte

// nhlAPIBaseURL is the default NHL API base URL used to resolve the
// offseason anchor.
const nhlAPIBaseURL = "https://api-web.nhle.com"

// gameDay is one saved day of the real season: its real calendar date and
// the games that were played on it, exactly as embedded (real dates,
// real start times).
type gameDay struct {
	baseDate string // "YYYY-MM-DD", the real saved date
	games    []models.ScheduleGame
}

// gameIndexEntry locates a game within gameDays and carries its saved start
// time, pre-parsed, so StartTime() doesn't re-parse on every replay poll.
type gameIndexEntry struct {
	dayIndex     int
	startTimeUTC time.Time
}

// ScheduleServer serves the stacked-season schedule via GET /v1/schedule/{date}.
// The embedded season is a dense list of real game-days (day 0 = the first
// saved game-day, day 1 = the second, etc. — no gaps, since off-days were
// never part of the real season's games). Offseason day N serves game-day N,
// rebased onto the requested date, starting from an anchor resolved from the
// live NHL API (the first day of the offseason).
//
//	Wall-clock:  anchor ── +1d ── +2d ── ... ── +(len(gameDays)-1)d
//	Served day:  gameDays[0]  gameDays[1]  gameDays[2]  ...  gameDays[last]
//
// It also implements gamereplay.StartTimeProvider: StartTime(gameID) returns
// the rebased start time for a game so the replay engine can compute game
// position.
type ScheduleServer struct {
	gameDays  []gameDay
	gameIndex map[string]gameIndexEntry

	// anchor is the first offseason day, resolved once at construction and
	// frozen for the process lifetime (D2) — re-resolved only on restart, so
	// StartTime() and HandleSchedule() never disagree about a game's rebased
	// start mid-replay.
	anchor time.Time
	// regularSeasonStart is the upcoming real season's first day, used by
	// seasonResumed (D5). Zero when unknown (anchor resolution fell back to
	// the constant) — that disables the "real season resumed" stop check,
	// leaving only the "past the end of saved days" check.
	regularSeasonStart time.Time

	now    func() time.Time // injectable for testing
	logger *slog.Logger
}

// NewScheduleServer parses the embedded season file, resolves the offseason
// anchor from the live NHL API (falling back to a constant on failure — the
// emulator must serve rather than crash, D1), and returns a ready ScheduleServer.
func NewScheduleServer(logger *slog.Logger) *ScheduleServer {
	if logger == nil {
		logger = slog.Default()
	}
	fetcher := &httpBoundaryFetcher{client: &http.Client{}, baseURL: nhlAPIBaseURL}
	return newScheduleServer(fetcher, time.Now, logger)
}

// NewScheduleServerForTest builds a ScheduleServer with an injectable
// boundary fetcher and clock, so tests exercise real anchor-resolution logic
// without a live network call. Mirrors gamereplay.NewCacheForTest.
func NewScheduleServerForTest(fetcher nhlScheduleFetcher, clock func() time.Time, logger *slog.Logger) *ScheduleServer {
	if logger == nil {
		logger = slog.Default()
	}
	return newScheduleServer(fetcher, clock, logger)
}

func newScheduleServer(fetcher nhlScheduleFetcher, clock func() time.Time, logger *slog.Logger) *ScheduleServer {
	gameDays, gameIndex := loadSeason(logger)
	resolved := resolveAnchor(context.Background(), fetcher, clock(), logger)
	logger.Info("season loaded", "game_days", len(gameDays), "anchor", resolved.anchor.Format("2006-01-02"))

	return &ScheduleServer{
		gameDays:           gameDays,
		gameIndex:          gameIndex,
		anchor:             resolved.anchor,
		regularSeasonStart: resolved.regularSeasonStart,
		now:                clock,
		logger:             logger,
	}
}

// newScheduleServerWithAnchor builds a ScheduleServer with a pre-resolved
// anchor, skipping anchor resolution (and its network dependency) entirely.
// Used by tests that exercise day-index/rebase behavior, not anchor
// resolution itself — see anchor_test.go for those.
func newScheduleServerWithAnchor(anchor, regularSeasonStart time.Time, clock func() time.Time, logger *slog.Logger) *ScheduleServer {
	if logger == nil {
		logger = slog.Default()
	}
	gameDays, gameIndex := loadSeason(logger)
	return &ScheduleServer{
		gameDays:           gameDays,
		gameIndex:          gameIndex,
		anchor:             anchor,
		regularSeasonStart: regularSeasonStart,
		now:                clock,
		logger:             logger,
	}
}

// loadSeason parses the embedded season file into a dense, date-sorted list
// of game-days plus a gameID → position index. A malformed embed logs at
// error and exits(1) — slog has no Fatal, so the log-then-exit is explicit —
// so the server fails loudly at startup rather than silently serving empty
// responses.
func loadSeason(logger *slog.Logger) ([]gameDay, map[string]gameIndexEntry) {
	var resp models.ScheduleResponse
	if err := json.Unmarshal(seasonJSON, &resp); err != nil {
		logger.Error("embedded schedule data is malformed — rebuild with cmd/buildschedule", "err", err)
		os.Exit(1)
	}

	days := make([]gameDay, len(resp.GameWeek))
	for i, d := range resp.GameWeek {
		days[i] = gameDay{baseDate: d.Date, games: d.Games}
	}
	sort.Slice(days, func(i, j int) bool { return days[i].baseDate < days[j].baseDate })

	index := make(map[string]gameIndexEntry)
	for k, day := range days {
		for _, g := range day.games {
			t, err := time.Parse(time.RFC3339, g.StartTimeUTC)
			if err != nil {
				logger.Warn("could not parse startTimeUTC for game", "start_time_utc", g.StartTimeUTC, "game_id", g.ID, "err", err)
				continue
			}
			index[fmt.Sprintf("%d", g.ID)] = gameIndexEntry{dayIndex: k, startTimeUTC: t}
		}
	}
	return days, index
}

// dayIndex returns the position in gameDays that date maps to, counting
// whole calendar days from the anchor. May be negative (before the anchor)
// or >= len(gameDays) (past the end of saved days) — callers check range.
func (s *ScheduleServer) dayIndex(date time.Time) int {
	return int(date.Sub(s.anchor).Hours() / 24)
}

// seasonResumed reports whether the real NHL season has resumed by date
// (D5): once true, the emulator stops replaying even if saved game-days
// remain, so it doesn't serve stale offseason games once real hockey is
// back. Always false when regularSeasonStart is unknown (anchor resolution
// fell back to the constant, so there's nothing reliable to compare against).
func (s *ScheduleServer) seasonResumed(date time.Time) bool {
	return !s.regularSeasonStart.IsZero() && !date.Before(s.regularSeasonStart)
}

// StartTime returns the rebased startTimeUTC for gameID, matching what
// HandleSchedule would return for that game on the date this game's
// day-index naturally serves (anchor + dayIndex days). This is the invariant
// the whole replay stack depends on — see TestStartTimeMatchesHandleSchedule.
//
// Satisfies gamereplay.StartTimeProvider so the replay cache can compute
// position without importing services (avoids import cycle).
func (s *ScheduleServer) StartTime(gameID string) (time.Time, bool) {
	entry, ok := s.gameIndex[gameID]
	if !ok {
		return time.Time{}, false
	}
	servedBaseDate := s.anchor.AddDate(0, 0, entry.dayIndex)
	day := s.gameDays[entry.dayIndex]
	shiftDays, err := dateshift.DaysBetween(day.baseDate, servedBaseDate.Format("2006-01-02"))
	if err != nil {
		s.logger.Error("StartTime: shift computation failed", "game", gameID, "err", err)
		return time.Time{}, false
	}
	return dateshift.ShiftTime(entry.startTimeUTC, shiftDays), true
}

// HandleSchedule serves GET /v1/schedule/{date}.
//
// The requested date maps to a position in the dense game-day stack via
// dayIndex; that saved day's games are rebased onto the requested date and
// returned. Dates before the anchor, past the end of the saved season, or on
// or after the real season's resumption (D5) all serve an empty gameWeek,
// which filterGamesByDate in the backend handles as "no games today".
func (s *ScheduleServer) HandleSchedule(w http.ResponseWriter, r *http.Request) {
	// Extract the date from the trailing path segment ("/v1/schedule/YYYY-MM-DD").
	date := strings.TrimPrefix(r.URL.Path, "/v1/schedule/")

	resp := models.ScheduleResponse{GameWeek: []models.GameWeekDay{}}
	k := -1
	gamesCount := 0
	if reqT, err := time.Parse("2006-01-02", date); err == nil && !s.seasonResumed(reqT) {
		k = s.dayIndex(reqT)
		if k >= 0 && k < len(s.gameDays) {
			games, err := s.rebaseDay(k, date)
			if err != nil {
				s.logger.Error("rebase failed", "date", date, "day_index", k, "err", err)
			} else {
				resp.GameWeek = []models.GameWeekDay{{Date: date, Games: games}}
				gamesCount = len(games)
			}
		}
	}

	s.logger.Debug("schedule request", "date", date, "day_index", k, "games", gamesCount)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// rebaseDay returns gameDays[k]'s games relabeled onto reqDate: each game's
// GameDate becomes reqDate and its StartTimeUTC is shifted by the same
// number of calendar days (DST-aware, via dateshift), preserving
// cross-midnight games' offset from the day.
func (s *ScheduleServer) rebaseDay(k int, reqDate string) ([]models.ScheduleGame, error) {
	day := s.gameDays[k]
	shiftDays, err := dateshift.DaysBetween(day.baseDate, reqDate)
	if err != nil {
		return nil, err
	}
	out := make([]models.ScheduleGame, len(day.games))
	for i, g := range day.games {
		shiftedStart, err := dateshift.StartTimeUTC(g.StartTimeUTC, shiftDays)
		if err != nil {
			return nil, fmt.Errorf("rebase game %d: %w", g.ID, err)
		}
		g.GameDate = reqDate
		g.StartTimeUTC = shiftedStart
		out[i] = g
	}
	return out, nil
}
