package models

// Play represents a single play in a hockey game
type Play struct {
	TypeDescKey string `json:"typeDescKey"`
}

// PlayByPlayResponse represents the structure of the NHL play-by-play API response
type PlayByPlayResponse struct {
	Plays []Play `json:"plays"`
}
