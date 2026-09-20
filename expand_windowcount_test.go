package ics

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExpandCountBeforeWindow verifies that when the window starts after many
// COUNT-bounded occurrences, the instances that fall inside the window are the
// tail of the rule (the engine must consume earlier occurrences rather than
// restart counting inside the window).
func TestExpandCountBeforeWindow(t *testing.T) {
	lines := []string{"DTSTART:20230101T090000Z", "RRULE:FREQ=MONTHLY;COUNT=24"}
	cal := buildCalendar(t, lines)
	master := cal.Events()[0]
	winS := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	winE := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	res, err := ExpandEvent(master, winS, winE, 100, nil)
	require.NoError(t, err)
	require.Len(t, res.Instances, 7, "Jul..Dec 2023 rule ends Dec; in-window: Jun..Dec 2024")
	assert.Equal(t, "2024-06-01T09:00:00Z", res.Instances[0].Start.Time.UTC().Format(time.RFC3339))
	assert.Equal(t, "2024-12-01T09:00:00Z", res.Instances[6].Start.Time.UTC().Format(time.RFC3339))
	assert.False(t, res.Truncated)
}

// TestExpandWindowStartsAfterSeriesEnd verifies an empty result when the window
// is entirely after a bounded rule ends.
func TestExpandWindowStartsAfterSeriesEnd(t *testing.T) {
	lines := []string{"DTSTART:20200101T090000Z", "RRULE:FREQ=DAILY;COUNT=3"}
	cal := buildCalendar(t, lines)
	res, err := ExpandEvent(cal.Events()[0], time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), 100, nil)
	require.NoError(t, err)
	assert.Empty(t, res.Instances)
	assert.False(t, res.Truncated)
}

// TestExpandFloatingWindowing verifies floating instances window by wall clock
// and never collide with anchored instances.
func TestExpandFloatingWindowing(t *testing.T) {
	lines := []string{"DTSTART:20240101T120000", "RRULE:FREQ=DAILY;COUNT=3"}
	cal := buildCalendar(t, lines)
	// Window expressed in UTC; floating noon always maps to the synthetic
	// FLOATING (+00:00) zone, so Jan 1 12:00Z onwards is included and Jan 3 is
	// before the Jan 3 00:00Z window end.
	winS := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	winE := time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC)
	res, err := ExpandEvent(cal.Events()[0], winS, winE, 100, nil)
	require.NoError(t, err)
	require.Len(t, res.Instances, 1, "only Jan 2 floating noon in [Jan2, Jan3)")
	assert.Equal(t, "2024-01-02 12:00:00", res.Instances[0].Start.Time.Format("2006-01-02 15:04:05"))
}
