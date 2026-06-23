package main

import (
	"log"
	"net/http"
	"os"
	"testserver/internal/services"

	"github.com/gorilla/mux"
)

func main() {
	playByPlayPort := os.Getenv("PLAYBYPLAY_PORT")
	if playByPlayPort == "" {
		playByPlayPort = "8125"
	}

	statsPort := os.Getenv("STATS_PORT")
	if statsPort == "" {
		statsPort = "8124"
	}

	// Build the schedule server first — it implements StartTimeProvider so the
	// shared replay cache can resolve each game's shifted start time.
	scheduleServer := services.NewScheduleServer()

	// Both servers share a single Cache so eviction coordinates across ports (A2/D3).
	playByPlayServer, statsServer := services.NewGameServers(scheduleServer)

	// Port 8125: schedule + play-by-play
	go func() {
		router := mux.NewRouter()
		router.HandleFunc("/v1/schedule/{date}", scheduleServer.HandleSchedule)
		router.PathPrefix("/v1/gamecenter/").HandlerFunc(playByPlayServer.HandlePlayByPlay)

		log.Printf("Starting play-by-play server on port %s", playByPlayPort)
		if err := http.ListenAndServe(":"+playByPlayPort, router); err != nil {
			log.Fatalf("Play-by-play server error: %v", err)
		}
	}()

	// Port 8124: MoneyPuck stats
	router := mux.NewRouter()
	router.PathPrefix("/moneypuck/gameData/").HandlerFunc(statsServer.HandleStats)

	log.Printf("Starting stats server on port %s", statsPort)
	if err := http.ListenAndServe(":"+statsPort, router); err != nil {
		log.Fatalf("Stats server error: %v", err)
	}
}
