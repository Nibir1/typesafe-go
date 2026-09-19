package typesafe_test

import (
	"io"
	"log/slog"
	"time"
)

func newTestLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// measurableWork is how long a test handler must take before a recorded
// duration can be asserted to be greater than zero.
//
// A test server handled in the same process answers in microseconds, and on
// Windows the monotonic clock advances in ticks that can be as coarse as
// 15.6ms. Both readings of time.Now() then land inside one tick and
// time.Since returns exactly zero — a true measurement, not a missing one,
// which is why the hooks test failed only on windows-latest while every Linux
// and macOS job passed.
//
// Weakening the assertion to "not negative" would assert nothing at all,
// because time.Since is never negative. Making the work outlast a tick keeps
// "the duration was recorded" a claim the test can actually check.
const measurableWork = 25 * time.Millisecond
