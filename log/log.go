package log

import "log/slog"

// LevelTrace is a custom slog level below Debug, for very high-frequency
// output (e.g. per-poll dial readings) that would overwhelm debug logs.
// Enable with LOG_LEVEL=trace.
const LevelTrace = slog.Level(-8)
