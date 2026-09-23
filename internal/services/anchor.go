package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// anchorFetchTimeout bounds each NHL API call made while resolving the season
// state, so a slow upstream can't hang a request. Each call gets its own budget.
const anchorFetchTimeout = 3 * time.Second

// maxAnchorWalkBack bounds how many previousStartDate hops are followed while
// looking for the prior season's boundaries (the offseason is ~15 weeks).
const maxAnchorWalkBack = 30

// seasonBoundaries is the subset of the NHL schedule endpoint's top-level
// fields needed to decide what to serve. See resolveState.
type seasonBoundaries struct {
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

// seasonState is what the NHL API says about today, reduced to what the
// schedule handler needs.
type seasonState struct {
	// active is true from the first day of the offseason until the regular
	// season starts (offseason + preseason): the emulator serves the stack.
	// False once the regular season (and postseason) is under way: it serves
	// no games.
	active bool
	// anchor is day 0 of the stack: the day after the previous season's
	// playoffs ended. Zero when !active.
	anchor time.Time
	// regularSeasonStart is the upcoming (or current) regular season's first day.
	regularSeasonStart time.Time
}

// resolveState decides, from the NHL API alone, whether today is inside the
// serving window and where the stack starts.
//
//  1. GET /v1/schedule/{today}. The response's regularSeasonStartDate says
//     whether the regular season has started: if today >= it, we're in the
//     regular season or postseason -> inactive, serve nothing.
//  2. Otherwise we're in the offseason or preseason -> active. The response's
//     playoffEndDate is already the NEXT season's, so find the season that
//     just ended by walking previousStartDate back until a response whose
//     regularSeasonStartDate is on or before the date queried (i.e. a season
//     that had actually started). Its playoffEndDate + 1 day is the anchor.
//
// prev (may be nil) is the last successfully resolved state: the anchor only
// depends on the season that just ended, so it's reused while the upcoming
// regularSeasonStart is unchanged, skipping the walk.
//
// There is no guessed fallback: if the API can't answer, this returns an error.
func resolveState(ctx context.Context, fetcher nhlScheduleFetcher, now time.Time, prev *seasonState) (seasonState, error) {
	fetch := func(date string) (seasonBoundaries, error) {
		ctx, cancel := context.WithTimeout(ctx, anchorFetchTimeout)
		defer cancel()
		return fetcher.fetchBoundaries(ctx, date)
	}

	// UTC, not local wall-clock: anchor/regularSeasonStart are UTC-midnight
	// values, and a non-UTC host would otherwise misclassify "today" by up to
	// a day near midnight boundaries.
	today := now.UTC().Format("2006-01-02")

	resp, err := fetch(today)
	if err != nil {
		return seasonState{}, fmt.Errorf("fetch season boundaries for %s: %w", today, err)
	}
	regularSeasonStart, err := time.Parse("2006-01-02", resp.RegularSeasonStartDate)
	if err != nil {
		return seasonState{}, fmt.Errorf("invalid regularSeasonStartDate %q: %w", resp.RegularSeasonStartDate, err)
	}

	if today >= resp.RegularSeasonStartDate {
		return seasonState{regularSeasonStart: regularSeasonStart}, nil
	}

	if prev != nil && prev.active && prev.regularSeasonStart.Equal(regularSeasonStart) {
		return *prev, nil
	}

	date := resp.PreviousStartDate
	for i := 0; i < maxAnchorWalkBack; i++ {
		if date == "" {
			return seasonState{}, fmt.Errorf("no previousStartDate while looking for the previous season (from %s)", today)
		}
		b, err := fetch(date)
		if err != nil {
			return seasonState{}, fmt.Errorf("fetch season boundaries for %s: %w", date, err)
		}
		if b.RegularSeasonStartDate != "" && b.RegularSeasonStartDate <= date {
			playoffEnd, err := time.Parse("2006-01-02", b.PlayoffEndDate)
			if err != nil {
				return seasonState{}, fmt.Errorf("invalid playoffEndDate %q for the season containing %s: %w", b.PlayoffEndDate, date, err)
			}
			anchor := playoffEnd.AddDate(0, 0, 1)
			if anchor.UTC().Format("2006-01-02") > today {
				return seasonState{}, fmt.Errorf("previous season's playoffs end %s, which is not before today (%s)", b.PlayoffEndDate, today)
			}
			return seasonState{active: true, anchor: anchor, regularSeasonStart: regularSeasonStart}, nil
		}
		date = b.PreviousStartDate
	}
	return seasonState{}, fmt.Errorf("did not find the previous season within %d previousStartDate hops of %s", maxAnchorWalkBack, today)
}
