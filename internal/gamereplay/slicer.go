package gamereplay

import (
	"fmt"
	"strconv"
	"strings"

	"testserver/internal/models"
)

// SlicePBP filters plays to those that would have occurred by pos.
// Plays are returned in their original upstream order (no re-sort).
// On pre-game (Period==0) or empty plays, returns nil.
//
// A play is "before or at" pos when:
//   - its period is earlier than pos.Period, OR
//   - its period equals pos.Period AND its game-clock seconds ≤ pos.GameSecs
//     (except during intermission: include all plays from the completed period)
func SlicePBP(plays []models.Play, pos GamePosition) []models.Play {
	if pos.Period == 0 {
		return nil
	}
	var out []models.Play
	for _, p := range plays {
		if includePlay(p, pos) {
			out = append(out, p)
		}
	}
	return out
}

func includePlay(p models.Play, pos GamePosition) bool {
	// Post-game: include everything, including shootout plays (period 5).
	// Without this guard, period-5 plays get negative posInPeriod and are
	// always excluded (GameSecs=3900 < periodStart=4800).
	if pos.Ended {
		return true
	}
	if p.PeriodDescriptor.Number < pos.Period {
		return true
	}
	if p.PeriodDescriptor.Number > pos.Period {
		return false
	}
	// Same period. During intermission the current period is finished —
	// include all plays from it.
	if pos.InIntermission {
		return true
	}
	playSecs := parseGameSecs(p.TimeInPeriod)
	// gameSecs counts from start of game; convert to within-period secs.
	periodStart := (pos.Period - 1) * periodGameSecs
	posInPeriod := pos.GameSecs - periodStart
	return playSecs <= posInPeriod
}

// parseGameSecs converts "MM:SS" to total seconds.
func parseGameSecs(t string) int {
	parts := strings.SplitN(t, ":", 2)
	if len(parts) != 2 {
		return 0
	}
	m, _ := strconv.Atoi(parts[0])
	s, _ := strconv.Atoi(parts[1])
	return m*60 + s
}

// LastMPRow returns the same row SliceMP would serialize — the last row whose
// GameSecs ≤ pos.GameSecs — or false if none qualify yet (pre-game/empty).
// Exposed so callers can log the current score without re-parsing SliceMP's
// CSV output.
func LastMPRow(rows []MPRow, pos GamePosition) (MPRow, bool) {
	var best *MPRow
	for i := range rows {
		if rows[i].GameSecs <= pos.GameSecs {
			best = &rows[i]
		}
	}
	if best == nil {
		return MPRow{}, false
	}
	return *best, true
}

// SliceMP returns the last MoneyPuck row whose GameSecs ≤ pos.GameSecs.
// If no rows qualify (pre-game or empty), returns a zeroed row as CSV header+row.
// Always returns header + exactly one data row to match the existing handler contract.
func SliceMP(rows []MPRow, pos GamePosition) string {
	header := "time,homeTeamGoals,awayTeamGoals,homeTeamExpectedGoals,awayTeamExpectedGoals,homeTeamShootOutGoals,awayTeamShootOutGoals"
	best, ok := LastMPRow(rows, pos)
	if !ok {
		return header + "\n0,0,0,0.00,0.00,0,0\n"
	}
	return header + "\n" + fmt.Sprintf("%d,%d,%d,%.2f,%.2f,%d,%d\n",
		best.GameSecs,
		best.HomeGoals, best.AwayGoals,
		best.HomeExpectedGoals, best.AwayExpectedGoals,
		best.HomeShootOutGoals, best.AwayShootOutGoals,
	)
}
