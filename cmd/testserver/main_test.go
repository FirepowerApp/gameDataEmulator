package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestNewLoggerValidLevels(t *testing.T) {
	cases := []struct {
		in    string
		want  slog.Level
		below slog.Level // must NOT be enabled at this level
	}{
		{"", slog.LevelInfo, slog.LevelDebug},
		{"info", slog.LevelInfo, slog.LevelDebug},
		{"INFO", slog.LevelInfo, slog.LevelDebug},
		{"debug", slog.LevelDebug, slog.Level(-8)}, // nothing below debug
		{"warn", slog.LevelWarn, slog.LevelInfo},
		{"error", slog.LevelError, slog.LevelWarn},
	}
	for _, tc := range cases {
		buf := &bytes.Buffer{}
		logger := newLogger(tc.in, buf)
		if !logger.Enabled(nil, tc.want) {
			t.Errorf("newLogger(%q): expected %v enabled", tc.in, tc.want)
		}
		if tc.below > slog.Level(-8) && logger.Enabled(nil, tc.below) {
			t.Errorf("newLogger(%q): expected %v disabled (below threshold %v)", tc.in, tc.below, tc.want)
		}
		if strings.Contains(buf.String(), "invalid LOG_LEVEL") {
			t.Errorf("newLogger(%q): unexpected invalid-level warning: %s", tc.in, buf.String())
		}
	}
}

// TestNewLoggerInvalidLevelWarnsAndFallsBackToInfo verifies an unrecognized
// LOG_LEVEL value doesn't crash the server — it warns (visible in the output)
// and defaults to info, since a misconfigured k8s overlay must not take the
// emulator down.
func TestNewLoggerInvalidLevelWarnsAndFallsBackToInfo(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := newLogger("bogus", buf)

	if !logger.Enabled(nil, slog.LevelInfo) {
		t.Error("expected info level to be enabled after invalid LOG_LEVEL fallback")
	}
	if logger.Enabled(nil, slog.LevelDebug) {
		t.Error("expected debug level to stay disabled after invalid LOG_LEVEL fallback (default is info)")
	}
	if !strings.Contains(buf.String(), "invalid LOG_LEVEL") {
		t.Errorf("expected an invalid LOG_LEVEL warning to be logged, got: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "value=bogus") {
		t.Errorf("expected the offending value in the warning, got: %s", buf.String())
	}
}
