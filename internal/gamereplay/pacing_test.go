package gamereplay

import (
	"testing"
	"time"
)

func TestPosition(t *testing.T) {
	start := time.Date(2026, 6, 29, 21, 0, 0, 0, time.UTC)

	P := periodWallSecs
	I := intermissionSecs

	cases := []struct {
		name           string
		offsetSecs     int
		wantPeriod     int
		wantMaxSecs    int // GameSecs must be <= this
		wantMinSecs    int // GameSecs must be >= this
		wantInter      bool
		wantEnded      bool
	}{
		{"pre-game", -60, 0, 0, 0, false, false},
		{"period1 start", 0, 1, 0, 0, false, false},
		{"period1 mid", P / 2, 1, 600, 580, false, false},
		{"period1 end", P - 1, 1, periodGameSecs, 1100, false, false},
		{"intermission1 start", P, 1, periodGameSecs, periodGameSecs, true, false},
		{"intermission1 mid", P + I/2, 1, periodGameSecs, periodGameSecs, true, false},
		{"period2 start", P + I, 2, periodGameSecs, periodGameSecs, false, false},
		{"period2 mid", P + I + P/2, 2, 2*periodGameSecs - 1, periodGameSecs+500, false, false},
		{"intermission2", 2*P + I, 2, 2 * periodGameSecs, 2 * periodGameSecs, true, false},
		{"period3 start", 2*P + 2*I, 3, 2 * periodGameSecs, 2 * periodGameSecs, false, false},
		{"period3 end", 3*P + 2*I - 1, 3, 3 * periodGameSecs, 3*periodGameSecs - 100, false, false},
		{"ot intermission", 3*P + 2*I, 3, 3 * periodGameSecs, 3 * periodGameSecs, true, false},
		{"ot play start", 3*P + 2*I + otInterSecs, 4, 3 * periodGameSecs, 3 * periodGameSecs, false, false},
		{"ot play mid", 3*P + 2*I + otInterSecs + otWallSecs/2, 4, 3*periodGameSecs + otGameSecs/2 + 10, 3*periodGameSecs + otGameSecs/2 - 10, false, false},
		{"post-game (SO/ended)", 3*P + 2*I + otInterSecs + otWallSecs + 1, 5, 3*periodGameSecs + otGameSecs, 3*periodGameSecs + otGameSecs, false, true},
		{"well past", 10000, 5, 3*periodGameSecs + otGameSecs, 3*periodGameSecs + otGameSecs, false, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := start.Add(time.Duration(tc.offsetSecs) * time.Second)
			pos := Position(start, now)

			if pos.Period != tc.wantPeriod {
				t.Errorf("Period = %d, want %d", pos.Period, tc.wantPeriod)
			}
			if pos.GameSecs < tc.wantMinSecs || pos.GameSecs > tc.wantMaxSecs {
				t.Errorf("GameSecs = %d, want [%d, %d]", pos.GameSecs, tc.wantMinSecs, tc.wantMaxSecs)
			}
			if pos.InIntermission != tc.wantInter {
				t.Errorf("InIntermission = %v, want %v", pos.InIntermission, tc.wantInter)
			}
			if pos.Ended != tc.wantEnded {
				t.Errorf("Ended = %v, want %v", pos.Ended, tc.wantEnded)
			}
		})
	}
}

// TestPositionGameSecsMonotonicallyIncreases verifies that GameSecs never
// decreases as wall-clock advances (excluding intermissions which hold steady).
func TestPositionGameSecsMonotonicallyIncreases(t *testing.T) {
	start := time.Date(2026, 6, 29, 21, 0, 0, 0, time.UTC)
	prev := -1
	for i := 0; i < 10000; i += 30 {
		pos := Position(start, start.Add(time.Duration(i)*time.Second))
		if pos.GameSecs < prev {
			t.Errorf("at +%ds: GameSecs decreased from %d to %d", i, prev, pos.GameSecs)
		}
		prev = pos.GameSecs
	}
}
