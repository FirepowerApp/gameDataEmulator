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
	"sync"
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

// stateTTL is how long a resolved seasonState is trusted before the NHL API is
// asked again. It bounds how late the emulator notices the offseason starting
// or the regular season resuming.
const stateTTL = time.Hour

// ScheduleServer serves the stacked-season schedule via GET /v1/schedule/{date}.
// The embedded season is a dense list of every game-day of the sample season
// (day 0 = the first saved game-day, day 1 = the second, etc. — no gaps).
// From the first day of the offseason through the preseason, offseason day N
// serves game-day N rebased onto the requested date; once the regular season
// starts it serves no games. Both decisions come from the NHL API alone (see
// resolveState).
//
//	Wall-clock:  anchor ── +1d ── +2d ── ... ── regular season starts (stop)
//	Served day:  gameDays[0]  gameDays[1]  gameDays[2]  ...
//
// It also implements gamereplay.StartTimeProvider: StartTime(gameID) returns
// the rebased start time for a game so the replay engine can compute game
// position.
type ScheduleServer struct {
	gameDays  []gameDay
	gameIndex map[string]gameIndexEntry

	fetcher nhlScheduleFetcher
	clock   func() time.Time
	logger  *slog.Logger

	// mu guards the cached season state. The state is refreshed from the API at
	// most once per stateTTL (a single hourly call — the anchor is reused while
	// the season is unchanged). Within one offseason the anchor is the same on
	// every refresh, so StartTime() and HandleSchedule() always agree about a
	// game's rebased start. ponytail: the API call happens under the lock, so
	// requests queue behind a refresh (<=~3s/hour); use singleflight if that
	// ever matters.
	mu       sync.Mutex
	state    seasonState
	stateAt  time.Time
	hasState bool
}

// NewScheduleServer parses the embedded season file and returns a
// ScheduleServer that consults the live NHL API for what to serve. It makes no
// network call itself, so it starts at any time of year.
func NewScheduleServer(logger *slog.Logger) *ScheduleServer {
	if logger == nil {
		logger = slog.Default()
	}
	fetcher := &httpBoundaryFetcher{client: &http.Client{}, baseURL: nhlAPIBaseURL}
	return newScheduleServer(fetcher, time.Now, logger)
}

// NewScheduleServerForTest builds a ScheduleServer with an injectable
// boundary fetcher and clock, so tests exercise the real state-resolution
// logic without a live network call. Mirrors gamereplay.NewCacheForTest.
func NewScheduleServerForTest(fetcher nhlScheduleFetcher, clock func() time.Time, logger *slog.Logger) *ScheduleServer {
	if logger == nil {
		logger = slog.Default()
	}
	return newScheduleServer(fetcher, clock, logger)
}

func newScheduleServer(fetcher nhlScheduleFetcher, clock func() time.Time, logger *slog.Logger) *ScheduleServer {
	gameDays, gameIndex := loadSeason(logger)
	logger.Info("season loaded", "game_days", len(gameDays))
	return &ScheduleServer{
		gameDays:  gameDays,
		gameIndex: gameIndex,
		fetcher:   fetcher,
		clock:     clock,
		logger:    logger,
	}
}

// newScheduleServerWithState builds a ScheduleServer with an already-resolved
// state that never expires, skipping the NHL API entirely. Used by tests that
// exercise day-index/rebase behavior, not state resolution itself — see
// anchor_test.go for those.
func newScheduleServerWithState(st seasonState, logger *slog.Logger) *ScheduleServer {
	if logger == nil {
		logger = slog.Default()
	}
	s := newScheduleServer(nil, func() time.Time { return time.Time{} }, logger)
	s.state, s.hasState = st, true // clock is frozen at the zero time, so stateAt (zero) never ages
	return s
}

// currentState returns the season state, asking the NHL API when the cached
// one is older than stateTTL. If the API can't be reached, the last state it
// gave is reused (still API-derived, just stale) and the error logged; with no
// prior state there is nothing to go on, so the error is returned rather than
// a guess.
func (s *ScheduleServer) currentState(ctx context.Context) (seasonState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.clock()
	if s.hasState && now.Sub(s.stateAt) < stateTTL {
		return s.state, nil
	}
	var prev *seasonState
	if s.hasState {
		prev = &s.state
	}
	st, err := resolveState(ctx, s.fetcher, now, prev)
	if err != nil {
		if !s.hasState {
			return seasonState{}, err
		}
		s.logger.Warn("could not refresh season state from the NHL API, reusing the last one", "err", err)
		s.stateAt = now // don't retry (and block) on every request
		return s.state, nil
	}
	if !s.hasState || st != s.state {
		s.logger.Info("season state resolved", "active", st.active,
			"anchor", st.anchor.Format("2006-01-02"), "regular_season_start", st.regularSeasonStart.Format("2006-01-02"))
	}
	s.state, s.stateAt, s.hasState = st, now, true
	return st, nil
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
func (st seasonState) dayIndex(date time.Time) int {
	return int(date.Sub(st.anchor).Hours() / 24)
}

// seasonResumed reports whether the regular season has started by date: from
// then on the emulator serves no games, even if saved game-days remain.
func (st seasonState) seasonResumed(date time.Time) bool {
	return !date.Before(st.regularSeasonStart)
}

// StartTime returns the rebased startTimeUTC for gameID, matching what
// HandleSchedule would return for that game on the date this game's
// day-index naturally serves (anchor + dayIndex days). This is the invariant
// the whole replay stack depends on — see TestStartTimeMatchesHandleSchedule.
// It reports false for unknown games and whenever the emulator isn't serving
// the stack (regular season under way, or the NHL API unreachable with nothing
// cached).
//
// Satisfies gamereplay.StartTimeProvider so the replay cache can compute
// position without importing services (avoids import cycle).
func (s *ScheduleServer) StartTime(gameID string) (time.Time, bool) {
	entry, ok := s.gameIndex[gameID]
	if !ok {
		return time.Time{}, false
	}
	st, err := s.currentState(context.Background())
	if err != nil {
		s.logger.Error("StartTime: cannot determine season state from the NHL API", "game", gameID, "err", err)
		return time.Time{}, false
	}
	if !st.active {
		return time.Time{}, false
	}
	servedBaseDate := st.anchor.AddDate(0, 0, entry.dayIndex)
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
// While the NHL API says we're in the offseason or preseason, the requested
// date maps to a position in the dense game-day stack via dayIndex; that saved
// day's games are rebased onto the requested date and returned. Dates before
// the anchor, past the end of the saved season, or on or after the regular
// season's start — and every date once the regular season is under way — serve
// an empty gameWeek, which filterGamesByDate in the backend handles as "no
// games today". If the NHL API can't be reached and nothing is cached, it
// answers 503 rather than guess.
func (s *ScheduleServer) HandleSchedule(w http.ResponseWriter, r *http.Request) {
	// Extract the date from the trailing path segment ("/v1/schedule/YYYY-MM-DD").
	date := strings.TrimPrefix(r.URL.Path, "/v1/schedule/")

	st, err := s.currentState(r.Context())
	if err != nil {
		s.logger.Error("cannot determine season state from the NHL API", "date", date, "err", err)
		http.Error(w, "cannot determine season state from the NHL API", http.StatusServiceUnavailable)
		return
	}

	resp := models.ScheduleResponse{GameWeek: []models.GameWeekDay{}}
	k := -1
	gamesCount := 0
	if reqT, err := time.Parse("2006-01-02", date); err == nil && st.active && !st.seasonResumed(reqT) {
		k = st.dayIndex(reqT)
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

	s.logger.Debug("schedule request", "date", date, "active", st.active, "day_index", k, "games", gamesCount)

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
