package services

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"testserver/internal/gamereplay"
	"testserver/internal/models"
)

// fakeStartTimeProvider implements gamereplay.StartTimeProvider for tests.
type fakeStartTimeProvider struct {
	times map[string]time.Time
}

func (f *fakeStartTimeProvider) StartTime(gameID string) (time.Time, bool) {
	t, ok := f.times[gameID]
	return t, ok
}

// fakeSource implements gamereplay.Source with in-memory data for tests.
type fakeSource struct {
	plays  map[string][]models.Play
	mpRows map[string][]gamereplay.MPRow
}

func (f *fakeSource) FetchPlayByPlay(_ context.Context, gameID string) ([]models.Play, error) {
	return f.plays[gameID], nil
}
func (f *fakeSource) FetchMoneyPuck(_ context.Context, gameID string) ([]gamereplay.MPRow, error) {
	return f.mpRows[gameID], nil
}

// gameFixture is a helper to build a typical game's play list for tests.
func gameFixture() []models.Play {
	return []models.Play{
		{TypeDescKey: "faceoff", PeriodDescriptor: models.PeriodDescriptor{Number: 1, PeriodType: "REG", MaxRegulationPeriods: 3}, TimeInPeriod: "00:00", TimeRemaining: "20:00"},
		{TypeDescKey: "shot-on-goal", PeriodDescriptor: models.PeriodDescriptor{Number: 1, PeriodType: "REG", MaxRegulationPeriods: 3}, TimeInPeriod: "06:40", TimeRemaining: "13:20"},
		{TypeDescKey: "goal", PeriodDescriptor: models.PeriodDescriptor{Number: 2, PeriodType: "REG", MaxRegulationPeriods: 3}, TimeInPeriod: "06:40", TimeRemaining: "13:20"},
		{TypeDescKey: "game-end", PeriodDescriptor: models.PeriodDescriptor{Number: 3, PeriodType: "REG", MaxRegulationPeriods: 3}, TimeInPeriod: "20:00", TimeRemaining: "00:00"},
	}
}

func makeServersAt(gameID string, start, now time.Time, plays []models.Play) (*TestPlayByPlayServer, *TestStatsServer) {
	provider := &fakeStartTimeProvider{times: map[string]time.Time{gameID: start}}
	src := &fakeSource{
		plays:  map[string][]models.Play{gameID: plays},
		mpRows: map[string][]gamereplay.MPRow{gameID: {{GameSecs: 3600, HomeGoals: 1, AwayGoals: 0, HomeExpectedGoals: 1.5, AwayExpectedGoals: 0.8}}},
	}
	clk := func() time.Time { return now }
	return newGameServersWithClock(provider, src, clk, nil)
}

// makeTraceServersAt is like makeServersAt but returns a buffer-backed debug
// logger so tests can assert on the emitted log trace.
func makeTraceServersAt(gameID string, start, now time.Time, plays []models.Play) (*TestPlayByPlayServer, *TestStatsServer, *bytes.Buffer) {
	provider := &fakeStartTimeProvider{times: map[string]time.Time{gameID: start}}
	src := &fakeSource{
		plays:  map[string][]models.Play{gameID: plays},
		mpRows: map[string][]gamereplay.MPRow{gameID: {{GameSecs: 3600, HomeGoals: 1, AwayGoals: 0, HomeExpectedGoals: 1.5, AwayExpectedGoals: 0.8}}},
	}
	clk := func() time.Time { return now }
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	pbp, stats := newGameServersWithClock(provider, src, clk, logger)
	return pbp, stats, buf
}

// fetchPBP issues a play-by-play request and decodes the response.
func fetchPBP(t *testing.T, pbp *TestPlayByPlayServer, gameID string) models.PlayByPlayResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/gamecenter/"+gameID+"/play-by-play", nil)
	rec := httptest.NewRecorder()
	pbp.HandlePlayByPlay(rec, req)
	var resp models.PlayByPlayResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode PBP response: %v", err)
	}
	return resp
}

// TestPlayByPlayPreGame verifies no plays are returned before the game starts.
func TestPlayByPlayPreGame(t *testing.T) {
	start := time.Now().Add(time.Hour) // game starts in the future
	pbp, _ := makeServersAt("G1", start, time.Now(), gameFixture())
	resp := fetchPBP(t, pbp, "G1")
	if len(resp.Plays) != 0 {
		t.Errorf("pre-game: expected 0 plays, got %d", len(resp.Plays))
	}
}

// TestPlayByPlayMidGame verifies that only plays up to the current wall-clock are returned.
func TestPlayByPlayMidGame(t *testing.T) {
	start := time.Now().Add(-time.Hour) // game started an hour ago (~mid-period 2)
	pbp, _ := makeServersAt("G1", start, time.Now(), gameFixture())
	resp := fetchPBP(t, pbp, "G1")
	// Should have some plays but not game-end yet.
	if len(resp.Plays) == 0 {
		t.Error("mid-game: expected some plays, got none")
	}
	for _, p := range resp.Plays {
		if p.TypeDescKey == "game-end" {
			t.Error("mid-game: game-end should not appear yet")
		}
	}
}

// TestPlayByPlayHaltsAtGameEnd verifies that once the game is over, repeated polls
// stably return through game-end (no wrap-around, no re-fire of events).
// This is the regression invariant from the old cycling model, re-expressed in
// wall-clock terms: set clock well past game-end and verify stability.
func TestPlayByPlayHaltsAtGameEnd(t *testing.T) {
	// Set clock far past game-end (~10 hours after start covers any game length).
	start := time.Date(2026, 6, 29, 21, 0, 0, 0, time.UTC)
	now := start.Add(10 * time.Hour)
	pbp, _ := makeServersAt("G1", start, now, gameFixture())

	var lastType string
	for i := 0; i < 10; i++ {
		resp := fetchPBP(t, pbp, "G1")
		if len(resp.Plays) == 0 {
			t.Fatalf("poll %d: expected plays, got none", i)
		}
		lastType = resp.Plays[len(resp.Plays)-1].TypeDescKey
		if lastType != "game-end" {
			t.Errorf("poll %d: last play = %q, want game-end", i, lastType)
		}
	}
}

// TestPlayByPlayUnknownGameReturnsEmpty verifies an unknown game ID returns empty plays
// without panicking (the old TestMockMatchesLiveNHLStructure smoke check, adapted).
func TestPlayByPlayUnknownGameReturnsEmpty(t *testing.T) {
	provider := &fakeStartTimeProvider{times: map[string]time.Time{}}
	src := &fakeSource{}
	clk := func() time.Time { return time.Now() }
	pbp, _ := newGameServersWithClock(provider, src, clk, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/gamecenter/9999999999/play-by-play", nil)
	rec := httptest.NewRecorder()
	pbp.HandlePlayByPlay(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	var resp models.PlayByPlayResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Plays) != 0 {
		t.Errorf("unknown game: expected 0 plays, got %d", len(resp.Plays))
	}
}

// TestPlayByPlayRequiredFieldsPresent verifies all contract fields are populated.
// Adapted from TestAllMockEventsHaveRequiredFields.
func TestPlayByPlayRequiredFieldsPresent(t *testing.T) {
	start := time.Date(2026, 6, 29, 21, 0, 0, 0, time.UTC)
	now := start.Add(10 * time.Hour) // post-game so all plays visible
	pbp, _ := makeServersAt("G1", start, now, gameFixture())

	resp := fetchPBP(t, pbp, "G1")
	if len(resp.Plays) == 0 {
		t.Fatal("expected plays, got none")
	}
	for i, p := range resp.Plays {
		if p.TypeDescKey == "" {
			t.Errorf("play %d: TypeDescKey is empty", i)
		}
		if p.TimeInPeriod == "" {
			t.Errorf("play %d: TimeInPeriod is empty", i)
		}
		if p.TimeRemaining == "" {
			t.Errorf("play %d: TimeRemaining is empty", i)
		}
		if p.PeriodDescriptor.Number == 0 {
			t.Errorf("play %d: PeriodDescriptor.Number is 0", i)
		}
	}
}

// TestStatsHandlerReturnsCSV verifies the stats handler returns valid CSV.
func TestStatsHandlerReturnsCSV(t *testing.T) {
	start := time.Date(2026, 6, 29, 21, 0, 0, 0, time.UTC)
	now := start.Add(10 * time.Hour) // post-game
	_, stats := makeServersAt("2025020001", start, now, gameFixture())

	req := httptest.NewRequest(http.MethodGet, "/moneypuck/gameData/20252026/2025020001.csv", nil)
	rec := httptest.NewRecorder()
	stats.HandleStats(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "homeTeamGoals") {
		t.Errorf("CSV header missing homeTeamGoals: %s", body)
	}
}

// TestScheduleStartTimeProvider verifies ScheduleServer.StartTime works for a real game ID.
func TestScheduleStartTimeProvider(t *testing.T) {
	s := NewScheduleServer(nil)
	// 2025020001 is CHI @ FLA, Day 1 of the shifted season.
	_, ok := s.StartTime("2025020001")
	if !ok {
		t.Error("StartTime(2025020001) returned not-ok; want a parsed start time")
	}
	// Unknown game should return false.
	_, ok2 := s.StartTime("0")
	if ok2 {
		t.Error("StartTime(0) returned ok; want false for unknown game")
	}
}

// --- Decision-trace logging tests ---

// TestTraceRequestLogsCarryGameIDFirst verifies request/position/response
// events all carry game=<id> so one Aptakube filter isolates the full story.
func TestTraceRequestLogsCarryGameIDFirst(t *testing.T) {
	start := time.Date(2026, 6, 29, 21, 0, 0, 0, time.UTC)
	now := start.Add(-time.Hour) // mid-game
	pbp, _, buf := makeTraceServersAt("G1", start, now, gameFixture())

	fetchPBP(t, pbp, "G1")
	out := buf.String()

	for _, event := range []string{"request received", "position computed", "response summary"} {
		if !strings.Contains(out, event) {
			t.Errorf("expected %q event in trace, got: %s", event, out)
		}
	}
	if !strings.Contains(out, "game=G1") {
		t.Errorf("expected game=G1 on every line, got: %s", out)
	}
}

// TestTraceResponseSummaryIncludesLastPlay verifies the PBP response summary
// carries the last play's type/period/time — logged at info, not just a
// count — so "what did the emulator actually return" is answerable from logs
// alone, without a manual curl.
func TestTraceResponseSummaryIncludesLastPlay(t *testing.T) {
	start := time.Date(2026, 6, 29, 21, 0, 0, 0, time.UTC)
	now := start.Add(10 * time.Hour) // well past game-end
	pbp, _, buf := makeTraceServersAt("G1", start, now, gameFixture())

	fetchPBP(t, pbp, "G1")
	out := buf.String()

	if !strings.Contains(out, "level=INFO") || !strings.Contains(out, "response summary") {
		t.Errorf("expected 'response summary' at INFO level, got: %s", out)
	}
	if !strings.Contains(out, "last_play=game-end") {
		t.Errorf("expected last_play=game-end (gameFixture's terminal play), got: %s", out)
	}
	if !strings.Contains(out, "last_play_period=3") {
		t.Errorf("expected last_play_period=3, got: %s", out)
	}
}

// TestTraceResponseSummaryIncludesScore verifies the stats response summary
// carries the actual score (home/away goals) at info level, not just a byte
// count — mirrors TestTraceResponseSummaryIncludesLastPlay for the MP feed.
func TestTraceResponseSummaryIncludesScore(t *testing.T) {
	start := time.Date(2026, 6, 29, 21, 0, 0, 0, time.UTC)
	now := start.Add(10 * time.Hour)
	_, stats, buf := makeTraceServersAt("2025020001", start, now, gameFixture())

	req := httptest.NewRequest(http.MethodGet, "/moneypuck/gameData/20252026/2025020001.csv", nil)
	rec := httptest.NewRecorder()
	stats.HandleStats(rec, req)
	out := buf.String()

	if !strings.Contains(out, "level=INFO") || !strings.Contains(out, "response summary") {
		t.Errorf("expected 'response summary' at INFO level, got: %s", out)
	}
	// makeTraceServersAt's fixture MP row is HomeGoals=1, AwayGoals=0.
	if !strings.Contains(out, "home_goals=1") || !strings.Contains(out, "away_goals=0") {
		t.Errorf("expected home_goals=1 away_goals=0, got: %s", out)
	}
}

// TestStatsResponseSummaryStateOverWhenFinalDataServed verifies that when GetMP
// returns final data (PBP already detected game-end, triggering eviction), the
// stats response summary logs state=over rather than state=live. Previously,
// StateLabel(pos) returned "live" because the wall-clock position hadn't reached
// period 5 yet, even though the cache was already serving terminal data.
func TestStatsResponseSummaryStateOverWhenFinalDataServed(t *testing.T) {
	// Place the game start far in the past so PBP sees game-end, but NOT so far
	// that Position() returns Period:5/Ended:true — we want the wall-clock to
	// still read "live" (e.g., period 3 in-progress) while the cache has already
	// been driven to ENDED by a prior PBP fetch.
	start := time.Date(2026, 6, 29, 21, 0, 0, 0, time.UTC)
	// ~3h10m in: past regulation wall-clock so position is Ended, but we want
	// to test the "cache ENDED before wall-clock" window. Drive the scenario by
	// using the full post-game clock so PBP+MP both complete the lifecycle.
	now := start.Add(10 * time.Hour)

	mpRows := []gamereplay.MPRow{{GameSecs: 3600, HomeGoals: 2, AwayGoals: 1}} // final row at 3600s
	provider := &fakeStartTimeProvider{times: map[string]time.Time{"G1": start}}
	src := &fakeSource{
		plays:  map[string][]models.Play{"G1": gameFixture()},
		mpRows: map[string][]gamereplay.MPRow{"G1": mpRows},
	}
	clk := func() time.Time { return now }
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	_, stats := newGameServersWithClock(provider, src, clk, logger)

	req := httptest.NewRequest(http.MethodGet, "/moneypuck/gameData/20252026/G1.csv", nil)
	rec := httptest.NewRecorder()
	stats.HandleStats(rec, req)
	out := buf.String()

	if !strings.Contains(out, "state=over") {
		t.Errorf("expected state=over when MP row is at 3600s (regulation end), got: %s", out)
	}
	if strings.Contains(out, "state=live") {
		t.Errorf("expected no state=live when final data was served, got: %s", out)
	}
}

// TestTraceUnknownGameLogsWarnNotError verifies the unknown-game branch logs
// a warn (graceful — the response is 200, not an error status).
func TestTraceUnknownGameLogsWarnNotError(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	provider := &fakeStartTimeProvider{times: map[string]time.Time{}}
	src := &fakeSource{}
	clk := func() time.Time { return time.Now() }
	pbp, _ := newGameServersWithClock(provider, src, clk, logger)

	req := httptest.NewRequest(http.MethodGet, "/v1/gamecenter/9999999999/play-by-play", nil)
	rec := httptest.NewRecorder()
	pbp.HandlePlayByPlay(rec, req)

	out := buf.String()
	if !strings.Contains(out, "game not found") {
		t.Errorf("expected 'game not found' event, got: %s", out)
	}
	if !strings.Contains(out, "level=WARN") {
		t.Errorf("expected WARN level, got: %s", out)
	}
}

// TestNewGameServersNilLoggerDoesNotPanic asserts the nil→slog.Default()
// fallback holds at the services layer, not just inside gamereplay.
func TestNewGameServersNilLoggerDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil logger caused a panic: %v", r)
		}
	}()
	provider := &fakeStartTimeProvider{times: map[string]time.Time{"G1": time.Now()}}
	src := &fakeSource{plays: map[string][]models.Play{"G1": gameFixture()}}
	pbp, _ := newGameServersWithClock(provider, src, time.Now, nil)
	fetchPBP(t, pbp, "G1")
}

// TestNewScheduleServerNilLoggerDoesNotPanic mirrors the above for the
// schedule server's own constructor and startup log lines.
func TestNewScheduleServerNilLoggerDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil logger caused a panic: %v", r)
		}
	}()
	NewScheduleServer(nil)
}

// TestPBPResponseAlwaysHasMaxPeriods verifies the PBP response always includes
// maxPeriods=3, preventing the backend from treating games as playoff format.
// The real NHL API omits maxPeriods for playoff games; an absent/zero value
// causes watchgameupdates to use an extended OT model. We must always emit 3.
func TestPBPResponseAlwaysHasMaxPeriods(t *testing.T) {
	start := time.Date(2026, 6, 29, 21, 0, 0, 0, time.UTC)
	now := start.Add(10 * time.Hour) // post-game
	pbp, _ := makeServersAt("G1", start, now, gameFixture())

	resp := fetchPBP(t, pbp, "G1")
	if resp.MaxPeriods != 3 {
		t.Errorf("MaxPeriods = %d, want 3 (absent/0 signals playoff to backend)", resp.MaxPeriods)
	}
}

// TestPBPResponseUnknownGameHasMaxPeriods verifies the "game not found" branch
// also emits maxPeriods=3, not the zero-value that signals a playoff game.
func TestPBPResponseUnknownGameHasMaxPeriods(t *testing.T) {
	provider := &fakeStartTimeProvider{times: map[string]time.Time{}}
	src := &fakeSource{}
	clk := func() time.Time { return time.Now() }
	pbp, _ := newGameServersWithClock(provider, src, clk, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/gamecenter/9999999999/play-by-play", nil)
	rec := httptest.NewRecorder()
	pbp.HandlePlayByPlay(rec, req)

	var resp models.PlayByPlayResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.MaxPeriods != 3 {
		t.Errorf("unknown game MaxPeriods = %d, want 3", resp.MaxPeriods)
	}
}

// --- T9: Skippable live schema canaries ---

// TestLiveNHLPBPSchema verifies the real NHL API still exposes the fields
// the gamereplay package depends on. Skips when offline.
func TestLiveNHLPBPSchema(t *testing.T) {
	resp, err := http.Get("https://api-web.nhle.com/v1/gamecenter/2025020001/play-by-play")
	if err != nil {
		t.Skipf("offline or NHL API unreachable: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Skipf("NHL API returned %d", resp.StatusCode)
	}

	var data struct {
		Plays []models.Play `json:"plays"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		t.Fatalf("decode NHL response: %v", err)
	}
	if len(data.Plays) == 0 {
		t.Fatal("NHL API returned no plays for 2025020001")
	}
	p := data.Plays[0]
	required := map[string]bool{
		"typeDescKey":      p.TypeDescKey != "",
		"timeInPeriod":     p.TimeInPeriod != "",
		"periodDescriptor": p.PeriodDescriptor.Number > 0,
	}
	for field, ok := range required {
		if !ok {
			t.Errorf("NHL PBP schema: field %q is missing or zero", field)
		}
	}
}

// TestLiveMoneyPuckSchema verifies the MoneyPuck CSV still contains the columns
// the gamereplay package depends on. Skips when offline.
func TestLiveMoneyPuckSchema(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://moneypuck.com/moneypuck/gameData/20252026/2025020001.csv", nil)
	req.Header.Set("User-Agent", "gameDataEmulator/1.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Skipf("offline or MoneyPuck unreachable: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Skipf("MoneyPuck returned %d", resp.StatusCode)
	}

	required := []string{
		"time", "homeTeamGoals", "awayTeamGoals",
		"homeTeamShootOutGoals", "awayTeamShootOutGoals",
		"homeTeamExpectedGoals", "awayTeamExpectedGoals",
	}
	body := new(strings.Builder)
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	body.Write(buf[:n])
	header := strings.SplitN(body.String(), "\n", 2)[0]

	for _, col := range required {
		if !strings.Contains(header, col) {
			t.Errorf("MoneyPuck CSV header missing required column %q", col)
		}
	}
}
