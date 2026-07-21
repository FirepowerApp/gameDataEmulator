package gamereplay

import (
	"fmt"
	"strings"
	"testing"

	"testserver/internal/models"
)

func makePlay(typeKey string, period, minSecs int) models.Play {
	m := minSecs / 60
	s := minSecs % 60
	tp := fmt.Sprintf("%02d:%02d", m, s)
	rem := 1200 - minSecs
	rm := rem / 60
	rs := rem % 60
	tr := fmt.Sprintf("%02d:%02d", rm, rs)
	return models.Play{
		TypeDescKey: typeKey,
		PeriodDescriptor: models.PeriodDescriptor{
			Number:               period,
			PeriodType:           "REG",
			MaxRegulationPeriods: 3,
		},
		TimeInPeriod:  tp,
		TimeRemaining: tr,
	}
}

func TestSlicePBP(t *testing.T) {
	plays := []models.Play{
		makePlay("faceoff", 1, 0),
		makePlay("shot-on-goal", 1, 400),
		makePlay("goal", 1, 800),
		makePlay("period-end", 1, 1200),
		makePlay("faceoff", 2, 0),
		makePlay("goal", 2, 600),
		makePlay("game-end", 3, 1200),
	}

	cases := []struct {
		name      string
		pos       GamePosition
		wantCount int
		wantLast  string
	}{
		{"pre-game", GamePosition{Period: 0}, 0, ""},
		{"period1 before first play", GamePosition{Period: 1, GameSecs: 0}, 1, "faceoff"},
		{"period1 mid", GamePosition{Period: 1, GameSecs: 500}, 2, "shot-on-goal"},
		{"period1 intermission (all p1 plays)", GamePosition{Period: 1, GameSecs: 1200, InIntermission: true}, 4, "period-end"},
		{"period2 start", GamePosition{Period: 2, GameSecs: 1200}, 5, "faceoff"},
		{"period2 mid", GamePosition{Period: 2, GameSecs: 1800}, 6, "goal"},
		{"post-game ended", GamePosition{Period: 5, GameSecs: 3900, Ended: true}, 7, "game-end"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SlicePBP(plays, tc.pos)
			if len(got) != tc.wantCount {
				t.Errorf("len = %d, want %d", len(got), tc.wantCount)
			}
			if tc.wantLast != "" && len(got) > 0 {
				last := got[len(got)-1].TypeDescKey
				if last != tc.wantLast {
					t.Errorf("last play = %q, want %q", last, tc.wantLast)
				}
			}
		})
	}
}

// TestSlicePBPPreservesUpstreamOrder ensures plays come back in original order.
func TestSlicePBPPreservesUpstreamOrder(t *testing.T) {
	plays := []models.Play{
		makePlay("a", 1, 0),
		makePlay("b", 1, 300),
		makePlay("c", 1, 600),
	}
	pos := GamePosition{Period: 1, GameSecs: 700}
	got := SlicePBP(plays, pos)
	want := []string{"a", "b", "c"}
	for i, w := range want {
		if got[i].TypeDescKey != w {
			t.Errorf("play[%d] = %q, want %q", i, got[i].TypeDescKey, w)
		}
	}
}

func TestSliceMP(t *testing.T) {
	rows := []MPRow{
		{GameSecs: 0, HomeGoals: 0, AwayGoals: 0, HomeExpectedGoals: 0.1, AwayExpectedGoals: 0.1},
		{GameSecs: 600, HomeGoals: 1, AwayGoals: 0, HomeExpectedGoals: 1.2, AwayExpectedGoals: 0.8},
		{GameSecs: 3600, HomeGoals: 2, AwayGoals: 1, HomeExpectedGoals: 2.5, AwayExpectedGoals: 1.3},
	}

	t.Run("pre-game returns zeroed row", func(t *testing.T) {
		csv := SliceMP(nil, GamePosition{})
		if !strings.Contains(csv, "0,0,0,0.00,0.00,0,0") {
			t.Errorf("expected zeroed row, got: %s", csv)
		}
	})

	t.Run("mid-game returns last qualifying row", func(t *testing.T) {
		csv := SliceMP(rows, GamePosition{Period: 1, GameSecs: 700})
		if !strings.Contains(csv, "600,1,0") {
			t.Errorf("expected row at 600s, got: %s", csv)
		}
	})

	t.Run("post-game returns final row", func(t *testing.T) {
		csv := SliceMP(rows, GamePosition{Period: 5, GameSecs: 3900, Ended: true})
		if !strings.Contains(csv, "3600,2,1") {
			t.Errorf("expected final row, got: %s", csv)
		}
	})

	t.Run("goals in CSV agree with PBP-derived score at same position", func(t *testing.T) {
		// Position at 700 game-secs: PBP has 1 home goal at 600, MP row at 600 also shows 1-0.
		csv := SliceMP(rows, GamePosition{Period: 1, GameSecs: 700})
		if !strings.Contains(csv, "1,0") {
			t.Errorf("MP score should be 1-0 at position 700, got: %s", csv)
		}
	})
}

// TestSlicePBPShootout verifies that shootout plays (period 5) are included
// when the position is post-game ended. Previously, period-5 plays were
// excluded because the within-period arithmetic produced a negative posInPeriod
// (GameSecs=3900 - periodStart=4800 = -900), causing playSecs <= -900 → false.
func TestSlicePBPShootout(t *testing.T) {
	plays := []models.Play{
		makePlay("faceoff", 1, 0),
		makePlay("game-end", 3, 1200),
		makePlay("goal", 5, 0), // shootout-deciding goal — period 5
	}
	pos := GamePosition{Period: 5, GameSecs: 3900, Ended: true}
	got := SlicePBP(plays, pos)
	if len(got) != 3 {
		t.Fatalf("expected all 3 plays (including shootout goal), got %d", len(got))
	}
	if got[len(got)-1].TypeDescKey != "goal" {
		t.Errorf("last play = %q, want %q", got[len(got)-1].TypeDescKey, "goal")
	}
}

// TestLastMPRow verifies LastMPRow returns the same row SliceMP serializes,
// as structured data — used to log the current score without re-parsing CSV.
func TestLastMPRow(t *testing.T) {
	rows := []MPRow{
		{GameSecs: 0, HomeGoals: 0, AwayGoals: 0},
		{GameSecs: 600, HomeGoals: 1, AwayGoals: 0},
		{GameSecs: 3600, HomeGoals: 2, AwayGoals: 1},
	}

	t.Run("pre-game returns not-ok", func(t *testing.T) {
		_, ok := LastMPRow(nil, GamePosition{})
		if ok {
			t.Error("expected not-ok for empty rows")
		}
	})

	t.Run("mid-game matches SliceMP's chosen row", func(t *testing.T) {
		pos := GamePosition{Period: 1, GameSecs: 700}
		row, ok := LastMPRow(rows, pos)
		if !ok {
			t.Fatal("expected ok")
		}
		if row.GameSecs != 600 || row.HomeGoals != 1 || row.AwayGoals != 0 {
			t.Errorf("unexpected row: %+v", row)
		}
		csv := SliceMP(rows, pos)
		if !strings.Contains(csv, "600,1,0") {
			t.Errorf("LastMPRow and SliceMP disagree: csv=%s row=%+v", csv, row)
		}
	})
}
