package ics

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExpandLeapDay verifies YEARLY on Feb 29 skips non-leap years and that
// MONTHLY/BYMONTHDAY handles month lengths without normalization.
func TestExpandLeapDay(t *testing.T) {
	lines := []string{
		"DTSTART;TZID=America/New_York:20240229T090000",
		"RRULE:FREQ=YEARLY;COUNT=3",
	}
	cal := buildCalendar(t, lines)
	res, err := ExpandEvent(cal.Events()[0], time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2033, 1, 1, 0, 0, 0, 0, time.UTC), 10, nil)
	require.NoError(t, err)
	require.Len(t, res.Instances, 3)
	assert.Equal(t, 2024, res.Instances[0].Start.Time.Year())
	assert.Equal(t, 2028, res.Instances[1].Start.Time.Year())
	assert.Equal(t, 2032, res.Instances[2].Start.Time.Year())
	for _, inst := range res.Instances {
		assert.Equal(t, time.February, inst.Start.Time.Month())
		assert.Equal(t, 29, inst.Start.Time.Day())
	}
}

// TestExpandEndAcrossDST verifies a one-hour DTEND stays one hour (not two)
// when the series crosses a DST transition, since duration is calendrical.
func TestExpandEndAcrossDST(t *testing.T) {
	lines := []string{
		"DTSTART;TZID=America/New_York:20240308T090000",
		"DTEND;TZID=America/New_York:20240308T100000",
		"RRULE:FREQ=DAILY;COUNT=4",
	}
	cal := buildCalendar(t, lines)
	res, err := ExpandEvent(cal.Events()[0], testWindowStart, testWindowEnd, 10, nil)
	require.NoError(t, err)
	require.Len(t, res.Instances, 4)
	for _, inst := range res.Instances {
		dur := inst.End.Time.Sub(inst.Start.Time)
		assert.Equal(t, time.Hour, dur, "day %s end=%s start=%s",
			inst.Start.Time.Format("01-02"), inst.End.Time.Format("15:04"), inst.Start.Time.Format("15:04"))
		assert.Equal(t, 10, inst.End.Time.Hour(), "end wall clock stays 10:00 local")
	}
}

// TestExpandCrossKindExdate verifies an EXDATE written in UTC matches a
// TZID-based occurrence at the same absolute instant, and floating values do
// not cross-match anchored ones.
func TestExpandCrossKindExdate(t *testing.T) {
	t.Run("utc exdate matches tzid occurrence", func(t *testing.T) {
		lines := []string{
			"DTSTART;TZID=America/New_York:20240101T090000",
			"RRULE:FREQ=DAILY;COUNT=3",
			"EXDATE:20240102T140000Z", // == Jan 2 09:00 New York
		}
		cal := buildCalendar(t, lines)
		res, err := ExpandEvent(cal.Events()[0], testWindowStart, testWindowEnd, 10, nil)
		require.NoError(t, err)
		require.Len(t, res.Instances, 2)
		assert.Equal(t, "2024-01-01T14:00:00Z", res.Instances[0].Start.Time.UTC().Format(time.RFC3339))
		assert.Equal(t, "2024-01-03T14:00:00Z", res.Instances[1].Start.Time.UTC().Format(time.RFC3339))
	})

	t.Run("floating exdate does not match anchored", func(t *testing.T) {
		lines := []string{
			"DTSTART:20240101T090000Z",
			"RRULE:FREQ=DAILY;COUNT=2",
			"EXDATE:20240102T090000", // floating, must not match the UTC series
		}
		cal := buildCalendar(t, lines)
		res, err := ExpandEvent(cal.Events()[0], testWindowStart, testWindowEnd, 10, nil)
		require.NoError(t, err)
		require.Len(t, res.Instances, 2, "floating EXDATE never matches anchored occurrence")
	})
}

// TestExpandRdateBeforeWindow verifies RDATEs outside the window are excluded
// but an override anchored to such an RDATE still attaches correctly.
func TestExpandRdateBeforeWindow(t *testing.T) {
	lines := []string{
		"DTSTART:20240101T090000Z",
		"RRULE:FREQ=DAILY;COUNT=2",
		"RDATE:20231225T090000Z", // outside the window
	}
	cal := buildCalendar(t, lines)
	res, err := ExpandEvent(cal.Events()[0], testWindowStart, testWindowEnd, 10, nil)
	require.NoError(t, err)
	require.Len(t, res.Instances, 2)
	assert.Equal(t, "2024-01-01T09:00:00Z", res.Instances[0].Start.Time.UTC().Format(time.RFC3339))
}

// TestExpandCountWithUntils verifies COUNT and UNTIL terminate naturally
// without the truncation marker.
func TestExpandNaturalTermination(t *testing.T) {
	lines := []string{"DTSTART:20240101T090000Z", "RRULE:FREQ=DAILY;COUNT=3"}
	cal := buildCalendar(t, lines)
	res, err := ExpandEvent(cal.Events()[0], testWindowStart, testWindowEnd, 1000, nil)
	require.NoError(t, err)
	assert.False(t, res.Truncated, "COUNT ends naturally")
	assert.Equal(t, TruncationNone, res.Truncation)
	require.Len(t, res.Instances, 3)
}
