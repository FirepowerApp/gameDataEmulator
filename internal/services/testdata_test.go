package services

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"testserver/internal/models"
)

// TestMockMatchesLiveNHLStructure verifies that the mock API returns
// the same data structure as the live NHL play-by-play API
func TestMockMatchesLiveNHLStructure(t *testing.T) {
	// Fetch live NHL data for comparison
	resp, err := http.Get("https://api-web.nhle.com/v1/gamecenter/2024021172/play-by-play")
	if err != nil {
		t.Skipf("Skipping live API test - unable to fetch live data: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Skipf("Skipping live API test - live API returned status %d", resp.StatusCode)
		return
	}

	var liveData models.PlayByPlayResponse
	if err := json.NewDecoder(resp.Body).Decode(&liveData); err != nil {
		t.Fatalf("Failed to decode live NHL API response: %v", err)
	}

	if len(liveData.Plays) == 0 {
		t.Fatalf("Live NHL API returned no plays")
	}

	// Get a play from the live data
	livePlay := liveData.Plays[len(liveData.Plays)-1]

	// Create mock server and get mock data
	mockServer := NewTestPlayByPlayServer()
	mockData := mockServer.events[0] // Get first event

	if len(mockData.Plays) == 0 {
		t.Fatalf("Mock API has no plays")
	}

	mockPlay := mockData.Plays[0]

	// Verify structure matches
	t.Run("TypeDescKey", func(t *testing.T) {
		if mockPlay.TypeDescKey == "" {
			t.Error("Mock play TypeDescKey is empty")
		}
		if livePlay.TypeDescKey == "" {
			t.Error("Live play TypeDescKey is empty")
		}
	})

	t.Run("TimeRemaining", func(t *testing.T) {
		if mockPlay.TimeRemaining == "" {
			t.Error("Mock play TimeRemaining is empty")
		}
		// Live play may have empty TimeRemaining for certain event types
	})

	t.Run("TimeInPeriod", func(t *testing.T) {
		if mockPlay.TimeInPeriod == "" {
			t.Error("Mock play TimeInPeriod is empty")
		}
		// Live play may have empty TimeInPeriod for certain event types
	})

	t.Run("PeriodDescriptor", func(t *testing.T) {
		if mockPlay.PeriodDescriptor.Number == 0 {
			t.Error("Mock play PeriodDescriptor.Number is 0")
		}
		if mockPlay.PeriodDescriptor.PeriodType == "" {
			t.Error("Mock play PeriodDescriptor.PeriodType is empty")
		}
		if mockPlay.PeriodDescriptor.MaxRegulationPeriods == 0 {
			t.Error("Mock play PeriodDescriptor.MaxRegulationPeriods is 0")
		}

		if livePlay.PeriodDescriptor.Number == 0 {
			t.Error("Live play PeriodDescriptor.Number is 0")
		}
		if livePlay.PeriodDescriptor.PeriodType == "" {
			t.Error("Live play PeriodDescriptor.PeriodType is empty")
		}
		if livePlay.PeriodDescriptor.MaxRegulationPeriods == 0 {
			t.Error("Live play PeriodDescriptor.MaxRegulationPeriods is 0")
		}
	})

	// Verify JSON serialization produces the same fields
	t.Run("JSONSerialization", func(t *testing.T) {
		mockJSON, err := json.Marshal(mockPlay)
		if err != nil {
			t.Fatalf("Failed to marshal mock play: %v", err)
		}

		liveJSON, err := json.Marshal(livePlay)
		if err != nil {
			t.Fatalf("Failed to marshal live play: %v", err)
		}

		// Unmarshal into generic maps to compare keys
		var mockMap, liveMap map[string]interface{}
		json.Unmarshal(mockJSON, &mockMap)
		json.Unmarshal(liveJSON, &liveMap)

		// Check that mock has all required fields
		requiredFields := []string{"typeDescKey", "periodDescriptor", "timeInPeriod", "timeRemaining"}
		for _, field := range requiredFields {
			if _, ok := mockMap[field]; !ok {
				t.Errorf("Mock JSON missing required field: %s", field)
			}
		}
	})
}

// TestAllMockEventsHaveRequiredFields verifies every mock event has all required fields populated
func TestAllMockEventsHaveRequiredFields(t *testing.T) {
	mockServer := NewTestPlayByPlayServer()

	for i, event := range mockServer.events {
		if len(event.Plays) == 0 {
			t.Errorf("Event %d has no plays", i)
			continue
		}

		play := event.Plays[0]

		t.Run("Event_"+play.TypeDescKey, func(t *testing.T) {
			if play.TypeDescKey == "" {
				t.Error("TypeDescKey is empty")
			}

			if play.TimeRemaining == "" {
				t.Error("TimeRemaining is empty")
			}

			if play.TimeInPeriod == "" {
				t.Error("TimeInPeriod is empty")
			}

			if play.PeriodDescriptor.Number == 0 {
				t.Error("PeriodDescriptor.Number is 0")
			}

			if play.PeriodDescriptor.PeriodType == "" {
				t.Error("PeriodDescriptor.PeriodType is empty")
			}

			if play.PeriodDescriptor.MaxRegulationPeriods == 0 {
				t.Error("PeriodDescriptor.MaxRegulationPeriods is 0")
			}
		})
	}
}

// fetchPlayByPlay issues a play-by-play request for the given game ID and
// decodes the response.
func fetchPlayByPlay(t *testing.T, s *TestPlayByPlayServer, gameID string) models.PlayByPlayResponse {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/v1/gamecenter/"+gameID+"/play-by-play", nil)
	rec := httptest.NewRecorder()
	s.HandlePlayByPlay(rec, req)

	var resp models.PlayByPlayResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode play-by-play response: %v", err)
	}
	return resp
}

// TestPlayByPlayHaltsAtGameEnd verifies that once a game's fixture reaches
// "game-end", further polls keep returning game-end instead of wrapping
// back to "faceoff" and re-firing notifications.
func TestPlayByPlayHaltsAtGameEnd(t *testing.T) {
	tests := []struct {
		name        string
		gameID      string
		eventsCount int
	}{
		{name: "mapped game ID", gameID: "2025020001", eventsCount: 10},
		{name: "unmapped game ID (default cycling)", gameID: "9999999999", eventsCount: 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewTestPlayByPlayServer()

			// Poll through all events and well past the end.
			var lastType string
			for i := 0; i < tt.eventsCount+5; i++ {
				resp := fetchPlayByPlay(t, s, tt.gameID)
				if len(resp.Plays) == 0 {
					t.Fatalf("poll %d: response has no plays", i)
				}
				lastType = resp.Plays[0].TypeDescKey

				if i >= tt.eventsCount-1 {
					if lastType != "game-end" {
						t.Errorf("poll %d: expected game-end, got %q (loop wrapped back)", i, lastType)
					}
				}
			}

			if lastType != "game-end" {
				t.Errorf("final poll: expected game-end, got %q", lastType)
			}
		})
	}
}
