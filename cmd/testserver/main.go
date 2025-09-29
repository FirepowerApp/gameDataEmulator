package main

import (
	"log"
	"net/http"
	"os"
	"testserver/internal/services"

	"github.com/gorilla/mux"
)

func main() {
	// Get ports from environment variables or use defaults
	playByPlayPort := os.Getenv("PLAYBYPLAY_PORT")
	if playByPlayPort == "" {
		playByPlayPort = "8125"
	}

	statsPort := os.Getenv("STATS_PORT")
	if statsPort == "" {
		statsPort = "8124"
	}

	// Create test servers
	playByPlayServer := services.NewTestPlayByPlayServer()
	statsServer := services.NewTestStatsServer()

	// Start play-by-play server
	go func() {
		router := mux.NewRouter()
		router.PathPrefix("/v1/gamecenter/").HandlerFunc(playByPlayServer.HandlePlayByPlay)

		log.Printf("Starting test play-by-play server on port %s", playByPlayPort)
		if err := http.ListenAndServe(":"+playByPlayPort, router); err != nil {
			log.Fatalf("Play-by-play test server error: %v", err)
		}
	}()

	// Start stats server
	router := mux.NewRouter()
	router.PathPrefix("/moneypuck/gameData/").HandlerFunc(statsServer.HandleStats)

	log.Printf("Starting test stats server on port %s", statsPort)
	if err := http.ListenAndServe(":"+statsPort, router); err != nil {
		log.Fatalf("Stats test server error: %v", err)
	}
}
