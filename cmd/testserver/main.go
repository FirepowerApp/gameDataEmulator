package main

import (
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testserver/internal/services"

	"github.com/gorilla/mux"
)

func main() {
	logger := newLogger(os.Getenv("LOG_LEVEL"), os.Stdout)

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
	scheduleServer := services.NewScheduleServer(logger)

	// Both servers share a single Cache so eviction coordinates across ports (A2/D3).
	playByPlayServer, statsServer := services.NewGameServers(scheduleServer, logger)

	// Port 8125: schedule + play-by-play
	go func() {
		router := mux.NewRouter()
		router.HandleFunc("/v1/schedule/{date}", scheduleServer.HandleSchedule)
		router.PathPrefix("/v1/gamecenter/").HandlerFunc(playByPlayServer.HandlePlayByPlay)

		logger.Info("starting play-by-play server", "port", playByPlayPort)
		if err := http.ListenAndServe(":"+playByPlayPort, router); err != nil {
			logger.Error("play-by-play server error", "err", err)
			os.Exit(1)
		}
	}()

	// Port 8124: MoneyPuck stats
	router := mux.NewRouter()
	router.PathPrefix("/moneypuck/gameData/").HandlerFunc(statsServer.HandleStats)

	logger.Info("starting stats server", "port", statsPort)
	if err := http.ListenAndServe(":"+statsPort, router); err != nil {
		logger.Error("stats server error", "err", err)
		os.Exit(1)
	}
}

// newLogger builds the root text-handler logger from the LOG_LEVEL env var,
// writing to w (os.Stdout in production; a buffer in tests). An unrecognized
// value warns and falls back to info — a misconfigured deploy overlay must
// not crash the emulator.
func newLogger(levelEnv string, w io.Writer) *slog.Logger {
	level := slog.LevelInfo
	invalid := false
	switch strings.ToLower(levelEnv) {
	case "", "info":
		level = slog.LevelInfo
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		invalid = true
	}
	logger := slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
	if invalid {
		logger.Warn("invalid LOG_LEVEL, falling back to info", "value", levelEnv)
	}
	return logger
}
