// Package gamereplay provides time-aware replay of completed NHL game data.
// Given a game's shifted start time and the current wall-clock, it computes
// which plays and statistics would have occurred "so far."
package gamereplay

import (
	"fmt"
	"time"
)

// Pacing constants for the realistic ~2.5h game model.
// Each 20-min period is stretched 1.9× to ~38 min wall-clock to account for
// stoppages, TV timeouts, and faceoffs. Two 18-min intermissions separate
// the periods. OT adds a short intermission then up to 5 min OT.
//
// Wall-clock layout:
//
//	Period 1 (0–38m) | Intermission (38–56m) | Period 2 (56–94m) |
//	Intermission (94–112m) | Period 3 (112–150m)
//	[for tied games]: OT intermission (150–151m) | OT (151–160.5m) | SO instant
const (
	periodGameSecs     = 20 * 60                               // 1200s game-clock per period
	periodWallSecs     = int(float64(periodGameSecs) * 1.9)    // 2280s (~38m) wall-clock per period
	intermissionSecs   = 18 * 60                               // 1080s between periods
	otInterSecs        = 1 * 60                                // 60s intermission before OT
	otGameSecs         = 5 * 60                                // 300s OT game-clock
	otWallSecs         = int(float64(otGameSecs) * 1.9)        // 570s OT wall-clock
	regulationWallSecs = 3*periodWallSecs + 2*intermissionSecs // 8520s total regulation
)

// GamePosition describes where in the game the current wall-clock falls.
//
//	+------------------+
//	|  GamePosition    |
//	+------------------+
//	  Period         int  // 1-3 regulation; 4 = OT; 5 = SO
//	  GameSecs       int  // elapsed play-clock seconds (frozen during intermissions)
//	  InIntermission bool // true when wall-clock is between periods
//	  Ended          bool // true when game is fully complete
type GamePosition struct {
	Period         int
	GameSecs       int // elapsed play-clock seconds, used to filter MoneyPuck rows
	InIntermission bool
	Ended          bool
}

// Position computes the GamePosition for a game that started at start,
// evaluated at wall-clock time now.
// If now < start, returns a pre-game position (Period=0, GameSecs=0).
func Position(start, now time.Time) GamePosition {
	elapsed := int(now.Sub(start).Seconds())
	if elapsed < 0 {
		return GamePosition{} // pre-game
	}

	// Walk through the wall-clock segments in order.
	//
	// Segment layout (seconds from start):
	//   [0, P)          Period 1 play
	//   [P, P+I)        Intermission 1
	//   [P+I, 2P+I)     Period 2 play
	//   [2P+I, 2P+2I)   Intermission 2
	//   [2P+2I, 3P+2I)  Period 3 play     ← regulation end = 3P+2I
	//   [3P+2I, 3P+2I+OI) OT intermission
	//   [3P+2I+OI, 3P+2I+OI+OW) OT play
	//   beyond           SO (instant) / ended
	P := periodWallSecs
	I := intermissionSecs

	type seg struct {
		start, end    int
		period        int
		gameSecsStart int // play-clock seconds at the beginning of this segment
		isInter       bool
	}
	segments := []seg{
		{0, P, 1, 0, false},
		{P, P + I, 1, periodGameSecs, true},
		{P + I, 2*P + I, 2, periodGameSecs, false},
		{2*P + I, 2*P + 2*I, 2, 2 * periodGameSecs, true},
		{2*P + 2*I, 3*P + 2*I, 3, 2 * periodGameSecs, false},
		// OT intermission (period still 3 until OT starts)
		{3*P + 2*I, 3*P + 2*I + otInterSecs, 3, 3 * periodGameSecs, true},
		// OT play (period 4)
		{3*P + 2*I + otInterSecs, 3*P + 2*I + otInterSecs + otWallSecs, 4, 3 * periodGameSecs, false},
	}

	for _, s := range segments {
		if elapsed < s.end {
			var gameSecs int
			if s.isInter {
				gameSecs = s.gameSecsStart // frozen during intermission
			} else {
				// Linear interpolation within the play segment.
				posInSeg := elapsed - s.start
				gameSecs = s.gameSecsStart + int(float64(posInSeg)/float64(P)*float64(periodGameSecs))
				if gameSecs > s.gameSecsStart+periodGameSecs {
					gameSecs = s.gameSecsStart + periodGameSecs
				}
			}
			return GamePosition{
				Period:         s.period,
				GameSecs:       gameSecs,
				InIntermission: s.isInter,
			}
		}
	}

	// Past all segments — SO or ended.
	return GamePosition{
		Period:   5,
		GameSecs: 3*periodGameSecs + otGameSecs, // max regulation+OT play-clock
		Ended:    true,
	}
}

// FormatClock renders the within-period game clock as MM:SS, for logging.
// Meaningful only for periods 1-4 (OT); returns "" for pregame (Period 0) and
// shootout (Period 5), where a period clock doesn't apply.
func FormatClock(pos GamePosition) string {
	switch {
	case pos.Period == 0 || pos.Period >= 5:
		return ""
	case pos.Period == 4:
		return fmtMMSS(pos.GameSecs - 3*periodGameSecs) // OT: 300s period, not 1200s
	default:
		return fmtMMSS(pos.GameSecs - (pos.Period-1)*periodGameSecs)
	}
}

func fmtMMSS(secs int) string {
	if secs < 0 {
		secs = 0
	}
	return fmt.Sprintf("%02d:%02d", secs/60, secs%60)
}

// StateLabel returns a coarse pregame/live/over label for pos, for logging.
func StateLabel(pos GamePosition) string {
	switch {
	case pos.Ended:
		return "over"
	case pos.Period == 0:
		return "pregame"
	default:
		return "live"
	}
}
