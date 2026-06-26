package gamereplay

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"testserver/internal/models"
)

// Source fetches full final game data from upstream APIs.
// Both feeds use real 2025-26 season IDs that resolve to completed games.
type Source interface {
	FetchPlayByPlay(ctx context.Context, gameID string) ([]models.Play, error)
	FetchMoneyPuck(ctx context.Context, gameID string) ([]MPRow, error)
}

// MPRow holds the MoneyPuck columns the backend reads, keyed to a game-elapsed
// seconds timestamp (the `time` column). One row per game event.
type MPRow struct {
	GameSecs              int
	HomeGoals             int
	AwayGoals             int
	HomeShootOutGoals     int
	AwayShootOutGoals     int
	HomeExpectedGoals     float64
	AwayExpectedGoals     float64
}

// upstreamAliases maps synthetic test-duplicate game IDs to the real NHL game ID
// whose data they replay. This lets the same real game appear "live" on multiple
// shifted dates: each synthetic ID carries its own start time in the schedule, but
// all fetch the same underlying upstream game.
//
// Populated for the June 25-28 duplicates of the June 29 (Day 1) slate. Remove
// these entries (and the corresponding schedule entries) to drop the duplicates.
var upstreamAliases = map[string]string{
	"20250292251": "2025020001", "20250292252": "2025020002", "20250292253": "2025020003", // 2026-06-25
	"20250292261": "2025020001", "20250292262": "2025020002", "20250292263": "2025020003", // 2026-06-26
	"20250292271": "2025020001", "20250292272": "2025020002", "20250292273": "2025020003", // 2026-06-27
	"20250292281": "2025020001", "20250292282": "2025020002", "20250292283": "2025020003", // 2026-06-28
}

// resolveUpstreamID returns the real upstream game ID for a (possibly synthetic)
// game ID. Non-aliased IDs pass through unchanged.
func resolveUpstreamID(gameID string) string {
	if real, ok := upstreamAliases[gameID]; ok {
		return real
	}
	return gameID
}

// httpSource is the real implementation that fetches from nhle.com and moneypuck.com.
// BaseURLNHL and BaseURLMP are injectable for testing (default to real upstream).
type httpSource struct {
	client     *http.Client
	baseURLNHL string
	baseURLMP  string
}

// NewSource returns an httpSource ready for production use.
func NewSource() Source {
	return &httpSource{
		client:     &http.Client{Timeout: 10 * time.Second},
		baseURLNHL: "https://api-web.nhle.com",
		baseURLMP:  "https://moneypuck.com",
	}
}

// NewSourceWithBaseURLs returns an httpSource with overridden base URLs (for testing).
func NewSourceWithBaseURLs(nhlBase, mpBase string) Source {
	return &httpSource{
		client:     &http.Client{Timeout: 10 * time.Second},
		baseURLNHL: strings.TrimRight(nhlBase, "/"),
		baseURLMP:  strings.TrimRight(mpBase, "/"),
	}
}

// fetch GETs url, sets a non-blank User-Agent (required by moneypuck.com Cloudflare
// gate), and returns the body bytes. Non-2xx → error.
func (s *httpSource) fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "gameDataEmulator/1.0")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// FetchPlayByPlay fetches the full final play-by-play for a completed game and
// returns the plays array. The response also carries top-level startTimeUTC but
// we source that from the shifted schedule; only plays are needed here.
func (s *httpSource) FetchPlayByPlay(ctx context.Context, gameID string) ([]models.Play, error) {
	gameID = resolveUpstreamID(gameID)
	url := fmt.Sprintf("%s/v1/gamecenter/%s/play-by-play", s.baseURLNHL, gameID)
	body, err := s.fetch(ctx, url)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Plays []models.Play `json:"plays"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse PBP %s: %w", gameID, err)
	}
	return resp.Plays, nil
}

// FetchMoneyPuck fetches the per-event MoneyPuck CSV for a 2025-26 game and
// returns the rows. Columns are looked up by header name (not position) so
// upstream reordering does not corrupt values. A missing required column → error.
func (s *httpSource) FetchMoneyPuck(ctx context.Context, gameID string) ([]MPRow, error) {
	gameID = resolveUpstreamID(gameID)
	url := fmt.Sprintf("%s/moneypuck/gameData/20252026/%s.csv", s.baseURLMP, gameID)
	body, err := s.fetch(ctx, url)
	if err != nil {
		return nil, err
	}
	return parseMoneyPuckCSV(body)
}

// parseMoneyPuckCSV reads the MoneyPuck per-event CSV and extracts the 7 fields
// the backend consumes, keyed by header name.
func parseMoneyPuckCSV(data []byte) ([]MPRow, error) {
	r := csv.NewReader(strings.NewReader(string(data)))
	headers, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("read MoneyPuck header: %w", err)
	}

	// Build a name→index map. Look up only the columns we need.
	idx := make(map[string]int, len(headers))
	for i, h := range headers {
		idx[h] = i
	}

	required := []string{
		"time",
		"homeTeamGoals", "awayTeamGoals",
		"homeTeamShootOutGoals", "awayTeamShootOutGoals",
		"homeTeamExpectedGoals", "awayTeamExpectedGoals",
	}
	for _, col := range required {
		if _, ok := idx[col]; !ok {
			return nil, fmt.Errorf("MoneyPuck CSV missing required column %q", col)
		}
	}

	var rows []MPRow
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read MoneyPuck row: %w", err)
		}
		row := MPRow{}
		fmt.Sscanf(rec[idx["time"]], "%d", &row.GameSecs)
		fmt.Sscanf(rec[idx["homeTeamGoals"]], "%d", &row.HomeGoals)
		fmt.Sscanf(rec[idx["awayTeamGoals"]], "%d", &row.AwayGoals)
		fmt.Sscanf(rec[idx["homeTeamShootOutGoals"]], "%d", &row.HomeShootOutGoals)
		fmt.Sscanf(rec[idx["awayTeamShootOutGoals"]], "%d", &row.AwayShootOutGoals)
		fmt.Sscanf(rec[idx["homeTeamExpectedGoals"]], "%f", &row.HomeExpectedGoals)
		fmt.Sscanf(rec[idx["awayTeamExpectedGoals"]], "%f", &row.AwayExpectedGoals)
		rows = append(rows, row)
	}
	return rows, nil
}
