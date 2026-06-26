package services

import (
	"context"
	"encoding/json"
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
	return newGameServersWithClock(provider, src, clk)
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
	pbp, _ := newGameServersWithClock(provider, src, clk)

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
	s := NewScheduleServer()
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
