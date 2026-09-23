package services

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"
)

// fakeBoundaryFetcher implements nhlScheduleFetcher against an in-memory map,
// so anchor resolution is testable without a live NHL API call.
type fakeBoundaryFetcher struct {
	byDate map[string]seasonBoundaries
	err    error // if set, all calls return this error
}

func (f *fakeBoundaryFetcher) fetchBoundaries(_ context.Context, date string) (seasonBoundaries, error) {
	if f.err != nil {
		return seasonBoundaries{}, f.err
	}
	b, ok := f.byDate[date]
	if !ok {
		return seasonBoundaries{}, errors.New("no fake data for date " + date)
	}
	return b, nil
}

// silentLogger discards output so test runs stay quiet; the fallback/warning
// paths are expected to fire in several of these cases.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, nil))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// TestResolveAnchorOffseason verifies the full recipe: offseason response ->
// fetch previousStartDate -> anchor = prior playoffEndDate + 1 day. Mirrors
// the confirmed real API responses in the design doc.
func TestResolveAnchorOffseason(t *testing.T) {
	fetcher := &fakeBoundaryFetcher{byDate: map[string]seasonBoundaries{
		"2026-07-15": {
			PreSeasonStartDate:     "2026-09-19",
			RegularSeasonStartDate: "2026-09-29",
			PlayoffEndDate:         "2027-06-10", // next season's — must NOT be used directly
			PreviousStartDate:      "2026-06-10",
		},
		"2026-06-10": {
			PlayoffEndDate: "2026-06-15", // the just-ended season's real value
		},
	}}
	now := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)

	got := resolveAnchor(context.Background(), fetcher, now, silentLogger())

	want := time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC)
	if !got.anchor.Equal(want) {
		t.Errorf("anchor = %s, want %s", got.anchor.Format("2006-01-02"), want.Format("2006-01-02"))
	}
	wantRegStart := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	if !got.regularSeasonStart.Equal(wantRegStart) {
		t.Errorf("regularSeasonStart = %s, want %s", got.regularSeasonStart.Format("2006-01-02"), wantRegStart.Format("2006-01-02"))
	}
}

// TestResolveAnchorInSeasonRejected verifies a response where the regular
// season has already started is treated as "not offseason" -> fallback.
func TestResolveAnchorInSeasonRejected(t *testing.T) {
	fetcher := &fakeBoundaryFetcher{byDate: map[string]seasonBoundaries{
		"2026-09-20": {
			PreSeasonStartDate:     "2026-09-19", // already started
			RegularSeasonStartDate: "2026-09-29",
			PlayoffEndDate:         "2027-06-10",
			PreviousStartDate:      "2026-09-13",
		},
	}}
	now := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)

	got := resolveAnchor(context.Background(), fetcher, now, silentLogger())

	want := mustParseDate(fallbackAnchorDate)
	if !got.anchor.Equal(want) {
		t.Errorf("in-season date should fall back to constant, got anchor=%s", got.anchor.Format("2006-01-02"))
	}
	if !got.regularSeasonStart.IsZero() {
		t.Error("fallback path should leave regularSeasonStart zero (unknown)")
	}
}

// TestResolveAnchorPreseasonRejected verifies a preseason response (regular
// season still future, but preseason already started) also falls back.
func TestResolveAnchorPreseasonRejected(t *testing.T) {
	fetcher := &fakeBoundaryFetcher{byDate: map[string]seasonBoundaries{
		"2026-09-22": {
			PreSeasonStartDate:     "2026-09-19",
			RegularSeasonStartDate: "2026-09-29", // still future
			PlayoffEndDate:         "2027-06-10",
			PreviousStartDate:      "2026-09-13",
		},
	}}
	now := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)

	got := resolveAnchor(context.Background(), fetcher, now, silentLogger())

	want := mustParseDate(fallbackAnchorDate)
	if !got.anchor.Equal(want) {
		t.Errorf("preseason date should fall back to constant, got anchor=%s", got.anchor.Format("2006-01-02"))
	}
}

// TestResolveAnchorAPIUnreachable verifies a network error falls back to the
// constant without panicking or blocking (D1).
func TestResolveAnchorAPIUnreachable(t *testing.T) {
	fetcher := &fakeBoundaryFetcher{err: errors.New("connection refused")}
	now := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)

	got := resolveAnchor(context.Background(), fetcher, now, silentLogger())

	want := mustParseDate(fallbackAnchorDate)
	if !got.anchor.Equal(want) {
		t.Errorf("unreachable API should fall back to constant, got anchor=%s", got.anchor.Format("2006-01-02"))
	}
}

// TestResolveAnchorMissingPreviousStartDate verifies a malformed offseason
// response (missing previousStartDate) falls back rather than panicking on
// a second fetch to an empty date.
func TestResolveAnchorMissingPreviousStartDate(t *testing.T) {
	fetcher := &fakeBoundaryFetcher{byDate: map[string]seasonBoundaries{
		"2026-07-15": {
			PreSeasonStartDate:     "2026-09-19",
			RegularSeasonStartDate: "2026-09-29",
			PlayoffEndDate:         "2027-06-10",
			// PreviousStartDate intentionally omitted.
		},
	}}
	now := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)

	got := resolveAnchor(context.Background(), fetcher, now, silentLogger())

	want := mustParseDate(fallbackAnchorDate)
	if !got.anchor.Equal(want) {
		t.Errorf("missing previousStartDate should fall back to constant, got anchor=%s", got.anchor.Format("2006-01-02"))
	}
}

// TestResolveAnchorTimeout verifies resolveAnchor respects the shared
// deadline rather than hanging indefinitely (D1: startup must not block).
func TestResolveAnchorTimeout(t *testing.T) {
	fetcher := &fakeBoundaryFetcher{err: context.DeadlineExceeded}
	now := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)

	done := make(chan struct{})
	go func() {
		resolveAnchor(context.Background(), fetcher, now, silentLogger())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("resolveAnchor did not return promptly on a failing fetcher")
	}
}
