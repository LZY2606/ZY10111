package ics

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExpandRecurrenceIDOverride covers moved, cancelled and orphan overrides.
func TestExpandRecurrenceIDOverride(t *testing.T) {
	tests := []struct {
		name            string
		override        []string
		wantInstantsUTC []string
		check           func(t *testing.T, insts []*Instance, diag *Result)
	}{
		{
			name: "moved override reschedules instance",
			override: []string{
				"RECURRENCE-ID;TZID=America/New_York:20240103T090000",
				"DTSTART;TZID=America/New_York:20240103T150000",
				"SUMMARY:later",
			},
			wantInstantsUTC: []string{"2024-01-01T14:00:00Z", "2024-01-02T14:00:00Z", "2024-01-03T20:00:00Z", "2024-01-04T14:00:00Z"},
			check: func(t *testing.T, insts []*Instance, diag *Result) {
				require.NotNil(t, insts[2].Override)
				assert.Equal(t, "later", insts[2].Override.Component.GetProperty(ComponentPropertySummary).Value)
				assert.False(t, insts[2].Override.Orphan)
			},
		},
		{
			name: "cancelled override suppresses instance",
			override: []string{
				"RECURRENCE-ID;TZID=America/New_York:20240103T090000",
				"DTSTART;TZID=America/New_York:20240103T090000",
				"STATUS:CANCELLED",
			},
			wantInstantsUTC: []string{"2024-01-01T14:00:00Z", "2024-01-02T14:00:00Z", "2024-01-04T14:00:00Z"},
			check: func(t *testing.T, insts []*Instance, diag *Result) {
				require.Len(t, diag.Diagnostics.Excluded, 1)
				assert.Equal(t, "2024-01-03 14:00:00 +0000 UTC", diag.Diagnostics.Excluded[0].Start.Time.UTC().String())
			},
		},
		{
			name: "orphan override outside master series is emitted standalone",
			override: []string{
				"RECURRENCE-ID;TZID=America/New_York:20240715T090000",
				"DTSTART;TZID=America/New_York:20240715T110000",
				"SUMMARY:extra",
			},
			wantInstantsUTC: []string{"2024-01-01T14:00:00Z", "2024-01-02T14:00:00Z", "2024-01-03T14:00:00Z", "2024-01-04T14:00:00Z", "2024-07-15T15:00:00Z"},
			check: func(t *testing.T, insts []*Instance, diag *Result) {
				last := insts[len(insts)-1]
				require.NotNil(t, last.Override)
				assert.True(t, last.Override.Orphan)
				assert.True(t, hasKind(last, SourceOverride))
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			master := []string{
				"DTSTART;TZID=America/New_York:20240101T090000",
				"RRULE:FREQ=DAILY;COUNT=4",
			}
			cal := buildCalendar(t, master, tc.override)
			series, err := ExpandCalendar(cal, testWindowStart, testWindowEnd, 100, WithDiagnostics())
			require.NoError(t, err)
			res := series[0].Result
			require.Len(t, res.Instances, len(tc.wantInstantsUTC))
			for i, inst := range res.Instances {
				assert.Equal(t, tc.wantInstantsUTC[i], inst.Start.Time.UTC().Format(time.RFC3339))
			}
			if tc.check != nil {
				tc.check(t, res.Instances, res)
			}
		})
	}
}

// TestExpandTimezoneSemantics covers floating, IANA, UTC and unknown zones.
func TestExpandTimezoneSemantics(t *testing.T) {
	t.Run("floating time never uses host zone", func(t *testing.T) {
		lines := []string{"DTSTART:20240101T090000", "RRULE:FREQ=DAILY;COUNT=2"}
		cal := buildCalendar(t, lines)
		res, err := ExpandEvent(cal.Events()[0], testWindowStart, testWindowEnd, 10, nil)
		require.NoError(t, err)
		require.Len(t, res.Instances, 2)
		for _, inst := range res.Instances {
			assert.Equal(t, TimeKindFloating, inst.Start.Kind)
			assert.Equal(t, "09:00:00", inst.Start.Time.Format("15:04:05"))
			assert.Equal(t, "FLOATING", inst.Start.Time.Location().String())
		}
		// Floating and anchored must never merge.
		loc := res.Instances[0].Start.Time.Location()
		require.NotEqual(t, time.UTC, loc)
	})

	t.Run("utc values stay utc", func(t *testing.T) {
		lines := []string{"DTSTART:20240101T090000Z", "RRULE:FREQ=DAILY;COUNT=2"}
		cal := buildCalendar(t, lines)
		res, err := ExpandEvent(cal.Events()[0], testWindowStart, testWindowEnd, 10, nil)
		require.NoError(t, err)
		for _, inst := range res.Instances {
			assert.Equal(t, TimeKindUTC, inst.Start.Kind)
			assert.Equal(t, time.UTC, inst.Start.Time.Location())
		}
	})

	t.Run("iana zone resolves and offset changes at DST", func(t *testing.T) {
		lines := []string{
			"DTSTART;TZID=Europe/London:20240329T090000",
			"RRULE:FREQ=WEEKLY;COUNT=3",
		}
		cal := buildCalendar(t, lines)
		res, err := ExpandEvent(cal.Events()[0], testWindowStart, testWindowEnd, 10, nil)
		require.NoError(t, err)
		require.Len(t, res.Instances, 3)
		assert.Equal(t, "2024-03-29T09:00:00Z", res.Instances[0].Start.Time.UTC().Format(time.RFC3339))
		assert.Equal(t, "2024-04-05T08:00:00Z", res.Instances[1].Start.Time.UTC().Format(time.RFC3339))
		assert.Equal(t, "2024-04-12T08:00:00Z", res.Instances[2].Start.Time.UTC().Format(time.RFC3339))
	})

	t.Run("unknown timezone returns identifiable error", func(t *testing.T) {
		lines := []string{"DTSTART;TZID=Mars/Olympus:20240101T090000", "RRULE:FREQ=DAILY;COUNT=2"}
		cal := buildCalendar(t, lines)
		_, err := ExpandEvent(cal.Events()[0], testWindowStart, testWindowEnd, 10, nil)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrUnknownTimezone), "err=%v", err)

		// A custom resolver can supply the unknown zone and expansion proceeds.
		mars := time.FixedZone("MST", 0)
		res2, err2 := ExpandEvent(cal.Events()[0], testWindowStart, testWindowEnd, 10, nil,
			WithTimezoneResolvers(func(tzid string) *time.Location {
				if tzid == "Mars/Olympus" {
					return mars
				}
				return nil
			}))
		require.NoError(t, err2)
		require.Len(t, res2.Instances, 2)
	})
}

// TestExpandEmbeddedVTimezone covers a calendar carrying its own VTIMEZONE with
// a non-IANA TZID; expansion must succeed even without host tzdata support.
func TestExpandEmbeddedVTimezone(t *testing.T) {
	input := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:t\r\n" +
		"BEGIN:VTIMEZONE\r\nTZID:Custom/Zone\r\n" +
		"BEGIN:DAYLIGHT\r\nTZOFFSETFROM:-0500\r\nTZOFFSETTO:-0400\r\nTZNAME:CDT\r\n" +
		"DTSTART:19700308T020000\r\nRRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=2SU\r\nEND:DAYLIGHT\r\n" +
		"BEGIN:STANDARD\r\nTZOFFSETFROM:-0400\r\nTZOFFSETTO:-0500\r\nTZNAME:CST\r\n" +
		"DTSTART:19701101T020000\r\nRRULE:FREQ=YEARLY;BYMONTH=11;BYDAY=1SU\r\nEND:STANDARD\r\n" +
		"END:VTIMEZONE\r\n" +
		"BEGIN:VEVENT\r\nUID:z\r\nDTSTART;TZID=Custom/Zone:20240308T090000\r\n" +
		"RRULE:FREQ=DAILY;COUNT=4\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	cal := parseCalendar(t, input)
	series, err := ExpandCalendar(cal, testWindowStart, testWindowEnd, 10)
	require.NoError(t, err)
	require.Len(t, series, 1)
	insts := series[0].Result.Instances
	require.Len(t, insts, 4)
	// Mar 8,9 EST(-5); Mar 10,11 EDT(-4)
	assert.Equal(t, "2024-03-08T14:00:00Z", insts[0].Start.Time.UTC().Format(time.RFC3339))
	assert.Equal(t, "2024-03-09T14:00:00Z", insts[1].Start.Time.UTC().Format(time.RFC3339))
	assert.Equal(t, "2024-03-10T13:00:00Z", insts[2].Start.Time.UTC().Format(time.RFC3339))
	assert.Equal(t, "2024-03-11T13:00:00Z", insts[3].Start.Time.UTC().Format(time.RFC3339))
}

// TestExpandWindowSemantics verifies the default half-open window and the
// optional inclusive end.
func TestExpandWindowSemantics(t *testing.T) {
	lines := []string{"DTSTART:20240101T000000Z", "RRULE:FREQ=DAILY;COUNT=10"}
	cal := buildCalendar(t, lines)
	master := cal.Events()[0]
	end := time.Date(2024, 1, 4, 0, 0, 0, 0, time.UTC)
	res, err := ExpandEvent(master, time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), end, 100, nil)
	require.NoError(t, err)
	require.Len(t, res.Instances, 3, "half-open: Jan 1,2,3")
	assert.Equal(t, "2024-01-03T00:00:00Z", res.Instances[2].Start.Time.UTC().Format(time.RFC3339))

	res2, err := ExpandEvent(master, time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), end, 100, nil, WithInclusiveWindowEnd())
	require.NoError(t, err)
	require.Len(t, res2.Instances, 4, "inclusive: Jan 1,2,3,4")
}

// TestExpandUntilVsCount checks COUNT and UNTIL precedence and boundary
// behaviour (UNTIL is inclusive of the specified instant).
func TestExpandUntilVsCount(t *testing.T) {
	lines := []string{"DTSTART:20240101T090000Z", "RRULE:FREQ=DAILY;UNTIL=20240103T090000Z"}
	cal := buildCalendar(t, lines)
	res, err := ExpandEvent(cal.Events()[0], testWindowStart, testWindowEnd, 100, nil)
	require.NoError(t, err)
	require.Len(t, res.Instances, 3)
	assert.Equal(t, "2024-01-03T09:00:00Z", res.Instances[2].Start.Time.UTC().Format(time.RFC3339))

	// Date-only UNTIL ends at the end of that DTSTART-local date.
	lines2 := []string{"DTSTART;TZID=America/New_York:20240101T230000", "RRULE:FREQ=DAILY;UNTIL=20240103"}
	cal2 := buildCalendar(t, lines2)
	res2, err := ExpandEvent(cal2.Events()[0], testWindowStart, testWindowEnd, 100, nil)
	require.NoError(t, err)
	require.Len(t, res2.Instances, 3, "date-only UNTIL covers the whole local day")
	assert.Equal(t, "2024-01-04T04:00:00Z", res2.Instances[2].Start.Time.UTC().Format(time.RFC3339))
}

var _ = strings.TrimSpace
