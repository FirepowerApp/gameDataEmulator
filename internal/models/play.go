package models

// PeriodDescriptor holds information about the period of a play
type PeriodDescriptor struct {
	Number               int    `json:"number"`
	PeriodType           string `json:"periodType"`
	MaxRegulationPeriods int    `json:"maxRegulationPeriods"`
}

// Play represents a single play in a hockey game
type Play struct {
	TypeDescKey      string           `json:"typeDescKey"`
	PeriodDescriptor PeriodDescriptor `json:"periodDescriptor"`
	TimeInPeriod     string           `json:"timeInPeriod"`
	TimeRemaining    string           `json:"timeRemaining"`
}

// PlayByPlayResponse represents the structure of the NHL play-by-play API response
type PlayByPlayResponse struct {
	MaxPeriods int    `json:"maxPeriods,omitempty"`
	Plays      []Play `json:"plays"`
}
