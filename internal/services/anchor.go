package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// anchorResolveTimeout bounds how long startup waits on the NHL API before
// falling back to fallbackAnchorDate. Startup must never hang on a third
// party (D1).
const anchorResolveTimeout = 3 * time.Second

// fallbackAnchorDate is the constant anchor used when the NHL API is
// unreachable, or when resolveAnchor is invoked outside the offseason
// window it expects. This is the 2025-26 season's real offseason start
// (playoffEndDate 2026-06-15 + 1 day) — a safety net, not the primary path.
// Update it if this fallback is ever actually hit in production.
const fallbackAnchorDate = "2026-06-16"

// seasonBoundaries is the subset of the NHL schedule endpoint's top-level
// fields needed to resolve the offseason window. See resolveAnchor.
type seasonBoundaries struct {
	PreSeasonStartDate     string `json:"preSeasonStartDate"`
	RegularSeasonStartDate string `json:"regularSeasonStartDate"`
	PlayoffEndDate         string `json:"playoffEndDate"`
	PreviousStartDate      string `json:"previousStartDate"`
}

// nhlScheduleFetcher fetches the season-boundary fields from a schedule
// endpoint. An interface so anchor resolution is unit-testable without a
// live network call — see NewScheduleServerForTest.
type nhlScheduleFetcher interface {
	fetchBoundaries(ctx context.Context, date string) (seasonBoundaries, error)
}

// httpBoundaryFetcher implements nhlScheduleFetcher against the real NHL API.
type httpBoundaryFetcher struct {
	client  *http.Client
	baseURL string
}

func (f *httpBoundaryFetcher) fetchBoundaries(ctx context.Context, date string) (seasonBoundaries, error) {
	url := fmt.Sprintf("%s/v1/schedule/%s", f.baseURL, date)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return seasonBoundaries{}, err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return seasonBoundaries{}, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return seasonBoundaries{}, fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return seasonBoundaries{}, fmt.Errorf("read response body from %s: %w", url, err)
	}
	var b seasonBoundaries
	if err := json.Unmarshal(body, &b); err != nil {
		return seasonBoundaries{}, fmt.Errorf("parse schedule boundaries from %s: %w", url, err)
	}
	return b, nil
}

// resolvedAnchor is the result of resolveAnchor: the first offseason day, plus
// the upcoming real season's start date (for the D5 "stop once the real
// season resumes" check). regularSeasonStart is the zero time when it's
// unknown (fallback path was taken) — dayIndex/seasonResumed treat a zero
// regularSeasonStart as "unknown, don't stop early on this check".
type resolvedAnchor struct {
	anchor             time.Time
	regularSeasonStart time.Time
}

// resolveAnchor determines the first day of the offseason: the prior
// season's playoffEndDate + 1 day, using the NHL schedule endpoint's
// explicit season-boundary fields (no week-by-week walking needed — see the
// design doc's "Resolving the anchor" section for the full recipe and the
// confirmed API responses backing it).
//
// Recipe:
//  1. GET /v1/schedule/{today}.
//  2. Offseason iff regularSeasonStartDate is in the future AND today is
//     before preSeasonStartDate (this distinguishes offseason from both
//     in-season and preseason, which also carry a future
//     regularSeasonStartDate).
//  3. In offseason, playoffEndDate in that response is already NEXT
//     season's — so read the just-ended value from the prior season via
//     GET /v1/schedule/{previousStartDate}.
//  4. anchor = that response's playoffEndDate + 1 day.
//
// On any failure (network, parse, missing field, or "today is not actually
// in the offseason") this returns the fallback constant instead of failing
// startup — the emulator must serve rather than crash (D1).
func resolveAnchor(ctx context.Context, fetcher nhlScheduleFetcher, now time.Time, logger *slog.Logger) resolvedAnchor {
	ctx, cancel := context.WithTimeout(ctx, anchorResolveTimeout)
	defer cancel()

	fallback := resolvedAnchor{anchor: mustParseDate(fallbackAnchorDate)}
	today := now.Format("2006-01-02")

	resp, err := fetcher.fetchBoundaries(ctx, today)
	if err != nil {
		logger.Error("anchor resolution failed, using fallback constant",
			"err", err, "fallback", fallbackAnchorDate)
		return fallback
	}

	inSeason := resp.RegularSeasonStartDate == "" || today >= resp.RegularSeasonStartDate
	inPreseason := resp.PreSeasonStartDate != "" && today >= resp.PreSeasonStartDate
	if inSeason || inPreseason {
		logger.Warn("resolveAnchor called outside the offseason window, using fallback constant",
			"today", today, "regular_season_start", resp.RegularSeasonStartDate,
			"preseason_start", resp.PreSeasonStartDate, "fallback", fallbackAnchorDate)
		return fallback
	}

	regularSeasonStart, err := time.Parse("2006-01-02", resp.RegularSeasonStartDate)
	if err != nil {
		logger.Error("anchor resolution: invalid regularSeasonStartDate, using fallback constant",
			"value", resp.RegularSeasonStartDate, "err", err, "fallback", fallbackAnchorDate)
		return fallback
	}

	if resp.PreviousStartDate == "" {
		logger.Error("anchor resolution: missing previousStartDate, using fallback constant",
			"fallback", fallbackAnchorDate)
		return fallback
	}
	prev, err := fetcher.fetchBoundaries(ctx, resp.PreviousStartDate)
	if err != nil {
		logger.Error("anchor resolution: previous-season fetch failed, using fallback constant",
			"err", err, "fallback", fallbackAnchorDate)
		return fallback
	}
	if prev.PlayoffEndDate == "" {
		logger.Error("anchor resolution: previous season has no playoffEndDate, using fallback constant",
			"fallback", fallbackAnchorDate)
		return fallback
	}
	playoffEnd, err := time.Parse("2006-01-02", prev.PlayoffEndDate)
	if err != nil {
		logger.Error("anchor resolution: invalid playoffEndDate, using fallback constant",
			"value", prev.PlayoffEndDate, "err", err, "fallback", fallbackAnchorDate)
		return fallback
	}

	anchor := playoffEnd.AddDate(0, 0, 1)
	logger.Info("resolved offseason anchor",
		"anchor", anchor.Format("2006-01-02"), "source_playoff_end", prev.PlayoffEndDate,
		"regular_season_start", resp.RegularSeasonStartDate)
	return resolvedAnchor{anchor: anchor, regularSeasonStart: regularSeasonStart}
}

func mustParseDate(date string) time.Time {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		panic(fmt.Sprintf("invalid date constant %q: %v", date, err))
	}
	return t
}
