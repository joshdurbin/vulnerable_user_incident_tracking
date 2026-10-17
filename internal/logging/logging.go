// Package logging builds the structured zerolog logger used by every binary.
package logging

import (
	"io"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

// New returns a logger writing to w. format is "json" (default) or "console".
// Article bodies and credentials must never be passed to it.
func New(w io.Writer, level, format string) zerolog.Logger {
	lvl, err := zerolog.ParseLevel(strings.ToLower(level))
	if err != nil || level == "" {
		lvl = zerolog.InfoLevel
	}
	if strings.EqualFold(format, "console") {
		w = zerolog.ConsoleWriter{Out: w, TimeFormat: time.RFC3339}
	}
	return zerolog.New(w).Level(lvl).With().Timestamp().Logger()
}
