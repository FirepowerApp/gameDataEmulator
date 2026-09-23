package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"testserver/internal/models"
)

// weekResponse is the shape of the NHL /v1/schedule/{date} response that we
// care about — the pagination cursor and the per-day game arrays.
// Extra fields (venue, broadcasts, etc.) are intentionally ignored (D3).
type weekResponse struct {
	NextStartDate  string               `json:"nextStartDate"`
	PlayoffEndDate string               `json:"playoffEndDate"`
	GameWeek       []models.GameWeekDay `json:"gameWeek"`
}

// maxConsecutiveEmptyWeeks controls how many consecutive weeks with zero NHL
// games (see isSeasonGame) must appear before the fetch stops. The 2026 NHL
// Olympic break spans exactly two such weeks mid-season, so this value must be
// > 2 to fetch through to the actual end of the season.
const maxConsecutiveEmptyWeeks = 3

// maxWeeks is a hard safety cap on the total number of weekly fetches,
// regardless of other stopping conditions.
const maxWeeks = 40

// FetchSeason walks the NHL API's weekly schedule starting at day1, caching
// each raw weekly response under rawDir (resumable if the run is interrupted).
// It returns all GameWeekDay entries across the fetched weeks, unfiltered —
// the caller is responsible for filtering to NHL games.
func FetchSeason(ctx context.Context, client *http.Client, baseURL, day1, rawDir string) ([]models.GameWeekDay, error) {
	if err := os.MkdirAll(rawDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create raw cache dir %s: %w", rawDir, err)
	}

	var allDays []models.GameWeekDay
	date := day1
	consecutiveEmpty := 0
	// seasonEnd is the source season's playoffEndDate, read from the first
	// response. The API's nextStartDate jumps straight from the end of the
	// playoffs to the NEXT season's preseason (skipping the empty weeks), so
	// the empty-weeks stop below never fires; without this bound the next
	// season's games would leak into the saved season.
	seasonEnd := ""

	for week := 1; week <= maxWeeks; week++ {
		cachePath := filepath.Join(rawDir, fmt.Sprintf("2025-W%02d-%s.json", week, date))

		resp, err := fetchWeek(ctx, client, baseURL, date, cachePath)
		if err != nil {
			return nil, fmt.Errorf("week %d (%s): %w", week, date, err)
		}

		seasonGames := 0
		if week == 1 {
			seasonEnd = resp.PlayoffEndDate
		}

		for _, day := range resp.GameWeek {
			if seasonEnd != "" && day.Date > seasonEnd {
				continue
			}
			allDays = append(allDays, day)
			for _, g := range day.Games {
				if isSeasonGame(g) {
					seasonGames++
				}
			}
		}

		if seasonGames == 0 {
			consecutiveEmpty++
			if consecutiveEmpty >= maxConsecutiveEmptyWeeks {
				break
			}
		} else {
			consecutiveEmpty = 0
		}

		if resp.NextStartDate == "" || (seasonEnd != "" && resp.NextStartDate > seasonEnd) {
			break
		}
		date = resp.NextStartDate
	}

	return allDays, nil
}

// fetchWeek returns the cached response at cachePath if it already exists,
// otherwise fetches from the NHL API and writes the raw response to cachePath.
func fetchWeek(ctx context.Context, client *http.Client, baseURL, date, cachePath string) (*weekResponse, error) {
	if data, err := os.ReadFile(cachePath); err == nil {
		var resp weekResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			return nil, fmt.Errorf("corrupt cache %s: %w", cachePath, err)
		}
		return &resp, nil
	}

	url := fmt.Sprintf("%s/v1/schedule/%s", baseURL, date)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	httpResp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("NHL API returned %d for %s", httpResp.StatusCode, url)
	}

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	if err := os.WriteFile(cachePath, body, 0o644); err != nil {
		return nil, fmt.Errorf("write cache %s: %w", cachePath, err)
	}

	var resp weekResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse response from %s: %w", url, err)
	}
	return &resp, nil
}
