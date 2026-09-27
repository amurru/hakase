package agent

import (
	"strings"
	"testing"
	"time"
)

// TestTimeReminderIsStableWithinADay pins the prompt-cache property. The time
// reminder is part of the system instruction, which is the PREFIX of every
// request, so a change in it invalidates the provider's cache for the whole
// conversation, not just this block. Keying the value by minute made a
// long-running session lose its entire cache once a minute.
func TestTimeReminderIsStableWithinADay(t *testing.T) {
	first := buildTimeReminder()
	if first != buildTimeReminder() {
		t.Fatal("two calls in the same process produced different reminders; the value must be stable")
	}

	// The rendered date line must not carry a wall-clock time: that is what
	// made the value churn. Check only that line, since the body legitimately
	// contains " at " ("frozen at your knowledge cutoff").
	var dateLine string
	for _, line := range strings.Split(first, "\n") {
		if strings.HasPrefix(line, "The current date") {
			dateLine = line
			break
		}
	}
	if dateLine == "" {
		t.Fatalf("reminder lost its date line: %.200s", first)
	}
	if strings.Contains(dateLine, " at ") {
		t.Fatalf("date line still renders a time of day, which churns: %q", dateLine)
	}
	// The date itself must still be there - it is what temporal reasoning
	// actually rests on.
	if !strings.Contains(dateLine, "UTC offset") {
		t.Fatalf("date line lost the zone context: %q", dateLine)
	}
}

// TestTimeReminderCacheKeyIsDaily guards the cache key, since that is what
// decides when the string is rebuilt. A key finer than a day reintroduces the
// per-turn cache invalidation this change exists to remove.
func TestTimeReminderCacheKeyIsDaily(t *testing.T) {
	day := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	later := day.Add(50 * time.Minute)
	tomorrow := day.Add(24 * time.Hour)

	if a, b := day.Format(timeReminderCacheKeyLayout), later.Format(timeReminderCacheKeyLayout); a != b {
		t.Fatalf("cache key changed within a day: %q vs %q", a, b)
	}
	if a, c := day.Format(timeReminderCacheKeyLayout), tomorrow.Format(timeReminderCacheKeyLayout); a == c {
		t.Fatalf("cache key did not change across a day boundary: %q", a)
	}
}
