package models

// Team represents a team in the NHL schedule response.
//
// Mirrors watchgameupdates/internal/models/payload.go in the backend repo —
// HTTPScheduleFetcher decodes this shape directly.
type Team struct {
	ID                       int               `json:"id"`
	CommonName               map[string]string `json:"commonName"`
	PlaceName                map[string]string `json:"placeName"`
	PlaceNameWithPreposition map[string]string `json:"placeNameWithPreposition"`
	Abbrev                   string            `json:"abbrev"`
}

// ScheduleGame represents a single game in the NHL schedule response.
//
// Mirrors watchgameupdates/internal/schedule/types.go in the backend repo.
type ScheduleGame struct {
	ID           int    `json:"id"`
	GameDate     string `json:"gameDate"`
	StartTimeUTC string `json:"startTimeUTC"`
	GameState    string `json:"gameState"`
	GameType     int    `json:"gameType"`
	HomeTeam     Team   `json:"homeTeam"`
	AwayTeam     Team   `json:"awayTeam"`
}

// GameWeekDay represents a single day within the gameWeek array.
type GameWeekDay struct {
	Date  string         `json:"date"`
	Games []ScheduleGame `json:"games"`
}

// ScheduleResponse represents the NHL API response from /v1/schedule/{date}.
type ScheduleResponse struct {
	GameWeek []GameWeekDay `json:"gameWeek"`
}
