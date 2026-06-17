// Command buildschedule fetches the 2025-26 NHL regular-season schedule and
// produces a date-shifted version for use by the gameDataEmulator's
// /v1/schedule/{date} endpoint.
//
// Usage:
//
//	go run ./cmd/buildschedule [flags]
//
// Flags:
//
//	-day1        First regular-season date of the source season (default: 2025-10-07)
//	-target-day1 Date that day1 maps to in the shifted schedule (default: 2026-06-22)
//	-base-url    NHL API base URL (default: https://api-web.nhle.com)
//	-raw-dir     Directory for cached raw weekly responses (default: data/raw)
//	-out         Output path for the shifted season JSON (default: internal/services/data/season_2025-26_shifted.json)
//
// The fetch step is idempotent: already-cached weekly responses under -raw-dir
// are reused, so a failed run can be resumed without re-hitting the NHL API.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
)

func main() {
	day1 := flag.String("day1", "2025-10-07", "first regular-season date of the source season (YYYY-MM-DD)")
	targetDay1 := flag.String("target-day1", "2026-06-22", "date day1 maps to in the shifted schedule (YYYY-MM-DD)")
	baseURL := flag.String("base-url", "https://api-web.nhle.com", "NHL API base URL")
	rawDir := flag.String("raw-dir", "data/raw", "directory for cached raw weekly responses")
	out := flag.String("out", filepath.Join("internal", "services", "data", "season_2025-26_shifted.json"), "output path for the shifted season JSON")
	flag.Parse()

	ctx := context.Background()

	offsetDays, err := ComputeOffsetDays(*day1, *targetDay1)
	if err != nil {
		log.Fatalf("compute offset: %v", err)
	}
	log.Printf("Shifting %s → %s (%d calendar days)", *day1, *targetDay1, offsetDays)

	log.Printf("Fetching season schedule from %s starting %s (raw cache: %s)", *baseURL, *day1, *rawDir)
	rawDays, err := FetchSeason(ctx, http.DefaultClient, *baseURL, *day1, *rawDir)
	if err != nil {
		log.Fatalf("fetch season: %v", err)
	}
	log.Printf("Fetched %d day-entries across all weeks", len(rawDays))

	resp, err := TransformSeason(rawDays, offsetDays)
	if err != nil {
		log.Fatalf("transform season: %v", err)
	}

	gameCount := 0
	for _, day := range resp.GameWeek {
		gameCount += len(day.Games)
	}
	log.Printf("Produced %d regular-season games across %d days", gameCount, len(resp.GameWeek))

	data, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		log.Fatalf("marshal output: %v", err)
	}

	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		log.Fatalf("create output dir: %v", err)
	}
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		log.Fatalf("write %s: %v", *out, err)
	}
	log.Printf("Wrote %s (%d bytes)", *out, len(data))
}
