package dateshift

import (
	"testing"
	"time"
)

func TestDate(t *testing.T) {
	tests := []struct {
		date       string
		offsetDays int
		want       string
		wantErr    bool
	}{
		{"2025-10-07", 258, "2026-06-22", false},
		{"2026-04-18", 258, "2027-01-01", false}, // last regular-season day → New Year's Day
		{"2025-12-31", 1, "2026-01-01", false},   // year rollover
		{"bad", 1, "", true},
	}
	for _, tt := range tests {
		got, err := Date(tt.date, tt.offsetDays)
		if tt.wantErr {
			if err == nil {
				t.Errorf("Date(%q, %d): expected error", tt.date, tt.offsetDays)
			}
			continue
		}
		if err != nil {
			t.Errorf("Date(%q, %d): %v", tt.date, tt.offsetDays, err)
			continue
		}
		if got != tt.want {
			t.Errorf("Date(%q, %d) = %q, want %q", tt.date, tt.offsetDays, got, tt.want)
		}
	}
}

func TestDaysBetween(t *testing.T) {
	tests := []struct {
		from, to string
		want     int
		wantErr  bool
	}{
		{from: "2025-10-07", to: "2026-06-22", want: 258},
		{from: "2025-01-01", to: "2025-01-01", want: 0},
		{from: "2025-01-02", to: "2025-01-01", want: -1},
		{from: "bad-date", to: "2026-06-22", wantErr: true},
	}
	for _, tt := range tests {
		got, err := DaysBetween(tt.from, tt.to)
		if tt.wantErr {
			if err == nil {
				t.Errorf("DaysBetween(%q, %q): expected error, got nil", tt.from, tt.to)
			}
			continue
		}
		if err != nil {
			t.Errorf("DaysBetween(%q, %q): unexpected error: %v", tt.from, tt.to, err)
			continue
		}
		if got != tt.want {
			t.Errorf("DaysBetween(%q, %q) = %d, want %d", tt.from, tt.to, got, tt.want)
		}
	}
}

func TestShiftTime(t *testing.T) {
	// Same DST-crossing case as StartTimeUTC's "EST to EDT" case, but
	// exercised directly on a time.Time (the internal API schedule.go uses
	// for StartTime()'s per-game rebase) rather than round-tripping strings.
	in := time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC)
	got := ShiftTime(in, 258)
	want := time.Date(2026, 8, 15, 23, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("ShiftTime(%s, 258) = %s, want %s", in.Format(time.RFC3339), got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

func TestStartTimeUTC(t *testing.T) {
	tests := []struct {
		name         string
		startTimeUTC string
		offsetDays   int
		want         string
		wantErr      bool
	}{
		{
			// EDT → EDT: both sides of the shift are in Eastern Daylight Time
			// (Oct 7 2025 and Jun 22 2026 are both UTC-4). Wall clock preserved,
			// UTC instant shifts by exactly 258×24h.
			name:         "EDT to EDT (no DST change)",
			startTimeUTC: "2025-10-07T21:00:00Z",
			offsetDays:   258,
			want:         "2026-06-22T21:00:00Z",
		},
		{
			// EST → EDT: Nov 30 2025 is EST (UTC-5), so 19:00 local = 00:00 UTC Dec 1.
			// Aug 15 2026 is EDT (UTC-4), so 19:00 local = 23:00 UTC.
			// A naive UTC shift would give 2026-08-16T00:00:00Z (off by 1 h).
			name:         "EST to EDT (DST-aware: wall clock preserved, UTC shifts by 23h not 24h)",
			startTimeUTC: "2025-12-01T00:00:00Z",
			offsetDays:   258,
			want:         "2026-08-15T23:00:00Z",
		},
		{
			// Second EST-tail case: deep winter game.
			// 2026-01-15T02:00:00Z = Jan 14 2026 21:00 EST (UTC-5).
			// Jan 14 + 258 calendar days = Sep 29 2026.
			// Sep 29 2026 is EDT (UTC-4): 21:00 EDT = 2026-09-30T01:00:00Z.
			// A naive UTC shift would give 2026-09-30T02:00:00Z (off by 1 h).
			name:         "deep winter EST game (required EST-tail coverage)",
			startTimeUTC: "2026-01-15T02:00:00Z",
			offsetDays:   258,
			want:         "2026-09-30T01:00:00Z",
		},
		{
			name:         "bad timestamp",
			startTimeUTC: "not-a-time",
			offsetDays:   1,
			wantErr:      true,
		},
		{
			// Negative offset (rebasing backward), used by the emulator's
			// day-index rebase when today's game-day is earlier than the
			// saved base date.
			name:         "negative offset",
			startTimeUTC: "2026-06-22T21:00:00Z",
			offsetDays:   -258,
			want:         "2025-10-07T21:00:00Z",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := StartTimeUTC(tt.startTimeUTC, tt.offsetDays)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("StartTimeUTC(%q, %d) = %q, want %q",
					tt.startTimeUTC, tt.offsetDays, got, tt.want)
			}
		})
	}
}
