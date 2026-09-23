// Package dateshift provides DST-aware calendar-day arithmetic for rebasing
// NHL schedule dates and start times onto a different day.
//
// Shifting must go through America/New_York local time, not raw UTC: a naive
// UTC add drifts by an hour whenever the shift crosses a DST boundary,
// because a fixed number of calendar days is not a fixed number of hours
// once EST/EDT changes underneath it.
package dateshift

import (
	"fmt"
	"time"
)

// NYLocation is America/New_York, the NHL's primary scheduling timezone.
// Shifting dates in this location (rather than raw UTC) preserves each
// game's local wall-clock start time across the EST↔EDT DST boundary.
var NYLocation = func() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		panic(fmt.Sprintf("time.LoadLocation(America/New_York): %v", err))
	}
	return loc
}()

// DaysBetween returns the number of calendar days from `from` to `to` (both
// "YYYY-MM-DD"), as `to` minus `from`. Negative if `to` precedes `from`.
func DaysBetween(from, to string) (int, error) {
	f, err := time.Parse("2006-01-02", from)
	if err != nil {
		return 0, fmt.Errorf("invalid date %q: %w", from, err)
	}
	t, err := time.Parse("2006-01-02", to)
	if err != nil {
		return 0, fmt.Errorf("invalid date %q: %w", to, err)
	}
	// Both parsed as UTC midnight, so Sub is an exact multiple of 24h (no DST
	// in UTC) — safe to divide and truncate.
	return int(t.Sub(f).Hours() / 24), nil
}

// ShiftTime shifts t by offsetDays calendar days, preserving its
// America/New_York wall-clock time-of-day across DST transitions.
//
// Concretely: a 7 PM EST start in November (UTC-5) becomes a 7 PM EDT start
// after the shift (UTC-4) — the UTC instant changes by 23 h, not 24 h, but
// the local game time is unchanged from the fans' perspective.
func ShiftTime(t time.Time, offsetDays int) time.Time {
	// Convert to NY local, add calendar days (Go's AddDate recomputes the DST
	// offset for the resulting date), then convert back to UTC.
	return t.In(NYLocation).AddDate(0, 0, offsetDays).UTC()
}

// StartTimeUTC shifts an RFC3339 UTC timestamp by offsetDays calendar days.
// See ShiftTime for the DST-aware behavior.
func StartTimeUTC(startTimeUTC string, offsetDays int) (string, error) {
	t, err := time.Parse(time.RFC3339, startTimeUTC)
	if err != nil {
		return "", fmt.Errorf("invalid startTimeUTC %q: %w", startTimeUTC, err)
	}
	return ShiftTime(t, offsetDays).Format(time.RFC3339), nil
}
