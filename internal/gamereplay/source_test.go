package gamereplay

import (
	"context"
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

// TestResolveUpstreamID verifies synthetic duplicate IDs map to their real game,
// and non-aliased IDs pass through unchanged.
func TestResolveUpstreamID(t *testing.T) {
	cases := map[string]string{
		"20250292251": "2025020001", // 2026-06-25 copy of game 1
		"20250292263": "2025020003", // 2026-06-26 copy of game 3
		"2025020001":  "2025020001", // real ID passes through
		"9999999999":  "9999999999", // unknown passes through
	}
	for in, want := range cases {
		if got := resolveUpstreamID(in); got != want {
			t.Errorf("resolveUpstreamID(%q) = %q, want %q", in, got, want)
		}
	}
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

	src := NewSourceWithBaseURLs(srv.URL, srv.URL)
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

	src := NewSourceWithBaseURLs(srv.URL, srv.URL)
	_, err := src.FetchPlayByPlay(context.Background(), "2025020001")
	if err == nil {
		t.Error("expected error on non-2xx, got nil")
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
