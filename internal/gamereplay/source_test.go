package gamereplay

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"testserver/internal/models"
)

// fakeSource implements Source using in-memory data — no network required.
type fakeSource struct {
	plays  map[string][]models.Play
	mpRows map[string][]MPRow
	err    error // if set, all methods return this error
}

func (f *fakeSource) FetchPlayByPlay(_ context.Context, gameID string) ([]models.Play, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.plays[gameID], nil
}

func (f *fakeSource) FetchMoneyPuck(_ context.Context, gameID string) ([]MPRow, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.mpRows[gameID], nil
}

// TestHTTPSourceUserAgent verifies the real httpSource sets a non-blank User-Agent.
// MoneyPuck's Cloudflare gate rejects blank UAs.
func TestHTTPSourceUserAgent(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"plays":[]}`))
	}))
	defer srv.Close()

	src := NewSourceWithBaseURLs(srv.URL, srv.URL, nil)
	src.FetchPlayByPlay(context.Background(), "2025020001") //nolint:errcheck
	if gotUA == "" {
		t.Error("User-Agent was empty; Cloudflare gate would block MoneyPuck")
	}
}

// TestHTTPSourceNon2xxIsError verifies a non-2xx upstream response becomes an error.
func TestHTTPSourceNon2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	src := NewSourceWithBaseURLs(srv.URL, srv.URL, nil)
	_, err := src.FetchPlayByPlay(context.Background(), "2025020001")
	if err == nil {
		t.Error("expected error on non-2xx, got nil")
	}
}

// TestFetchLogsGameID verifies the fetch log line carries the game ID so
// requests are findable by filtering logs on it.
func TestFetchLogsGameID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"plays":[]}`))
	}))
	defer srv.Close()

	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	src := NewSourceWithBaseURLs(srv.URL, srv.URL, logger)

	_, err := src.FetchPlayByPlay(context.Background(), "2025020001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "game=2025020001") {
		t.Errorf("expected game=<schedule ID>, got: %s", out)
	}
}

// TestFetchMoneyPuckNotFoundIsNotAnError verifies a 404 (e.g. preseason games,
// which MoneyPuck doesn't publish) yields no rows instead of failing the game.
func TestFetchMoneyPuckNotFoundIsNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	rows, err := NewSourceWithBaseURLs(srv.URL, srv.URL, nil).FetchMoneyPuck(context.Background(), "2025010001")
	if err != nil || len(rows) != 0 {
		t.Errorf("FetchMoneyPuck on 404 = (%v rows, %v), want (0 rows, nil)", len(rows), err)
	}
}

// TestParseMoneyPuckCSVMissingColumn verifies a missing required column returns an error.
func TestParseMoneyPuckCSVMissingColumn(t *testing.T) {
	data := []byte("id,time,homeTeamGoals\n1,0,0\n") // missing awayTeamGoals etc.
	_, err := parseMoneyPuckCSV(data)
	if err == nil {
		t.Fatal("expected error for missing required column, got nil")
	}
	if !strings.Contains(err.Error(), "missing required column") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestParseMoneyPuckCSVByHeaderName verifies values are extracted by column name,
// not position (resilient to upstream column reordering).
func TestParseMoneyPuckCSVByHeaderName(t *testing.T) {
	// Columns in reversed order to prove we're not reading by position.
	data := []byte(
		"awayTeamExpectedGoals,homeTeamExpectedGoals,awayTeamShootOutGoals,homeTeamShootOutGoals,awayTeamGoals,homeTeamGoals,time\n" +
			"1.3,2.5,0,0,1,2,600\n",
	)
	rows, err := parseMoneyPuckCSV(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	r := rows[0]
	if r.GameSecs != 600 {
		t.Errorf("GameSecs = %d, want 600", r.GameSecs)
	}
	if r.HomeGoals != 2 {
		t.Errorf("HomeGoals = %d, want 2", r.HomeGoals)
	}
	if r.AwayGoals != 1 {
		t.Errorf("AwayGoals = %d, want 1", r.AwayGoals)
	}
	if r.HomeExpectedGoals != 2.5 {
		t.Errorf("HomeExpectedGoals = %f, want 2.5", r.HomeExpectedGoals)
	}
}
