package ics

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func parseCalendar(t *testing.T, s string) *Calendar {
	t.Helper()
	c, err := ParseCalendar(strings.NewReader(s))
	require.NoError(t, err)
	return c
}

// veventBuilder assembles an ICS calendar with a master VEVENT and optional
// override VEVENTs. Lines use CRLF as RFC 5545 requires.
func buildCalendar(t *testing.T, masterLines []string, overrideBlocks ...[]string) *Calendar {
	t.Helper()
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:test\r\n")
	writeEvent := func(uid string, lines []string) {
		b.WriteString("BEGIN:VEVENT\r\n")
		b.WriteString("UID:" + uid + "\r\n")
		for _, l := range lines {
			b.WriteString(l + "\r\n")
		}
		b.WriteString("END:VEVENT\r\n")
	}
	writeEvent("u1", masterLines)
	for i, ob := range overrideBlocks {
		_ = i
		writeEvent("u1", ob)
	}
	b.WriteString("END:VCALENDAR\r\n")
	return parseCalendar(t, b.String())
}

var testWindowStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
var testWindowEnd = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

func sourcesKinds(inst *Instance) []SourceKind {
	out := make([]SourceKind, 0, len(inst.Sources))
	for _, s := range inst.Sources {
		out = append(out, s.Kind)
	}
	return out
}

func hasKind(inst *Instance, k SourceKind) bool {
	for _, s := range inst.Sources {
		if s.Kind == k {
			return true
		}
	}
	return false
}

// TestExpandDayWeekMonthCombinations covers the required table-driven matrix of
// daily/weekly/monthly rule combinations including interval, BYDAY, BYMONTHDAY,
// BYSETPOS and ordinal expansions.
func TestExpandDayWeekMonthCombinations(t *testing.T) {
	tests := []struct {
		name       string
		dtstart    string
		rrule      string
		wantStarts []string // expected local-wall times in America/New_York
	}{
		{
			name:       "daily count",
			dtstart:    "20240101T090000",
			rrule:      "FREQ=DAILY;COUNT=3",
			wantStarts: []string{"2024-01-01T09:00:00", "2024-01-02T09:00:00", "2024-01-03T09:00:00"},
		},
		{
			name:       "weekly mo we fr",
			dtstart:    "20240101T090000", // Monday
			rrule:      "FREQ=WEEKLY;BYDAY=MO,WE,FR;COUNT=4",
			wantStarts: []string{"2024-01-01T09:00:00", "2024-01-03T09:00:00", "2024-01-05T09:00:00", "2024-01-08T09:00:00"},
		},
		{
			name:       "monthly bymonthday skip missing",
			dtstart:    "20240131T090000",
			rrule:      "FREQ=MONTHLY;BYMONTHDAY=31;COUNT=5",
			wantStarts: []string{"2024-01-31T09:00:00", "2024-03-31T09:00:00", "2024-05-31T09:00:00", "2024-07-31T09:00:00", "2024-08-31T09:00:00"},
		},
		{
			name:       "monthly last friday",
			dtstart:    "20240126T090000",
			rrule:      "FREQ=MONTHLY;BYDAY=-1FR;COUNT=3",
			wantStarts: []string{"2024-01-26T09:00:00", "2024-02-23T09:00:00", "2024-03-29T09:00:00"},
		},
		{
			// DTSTART (Mon Jan 1) is occurrence one even though BYSETPOS=-1
			// selects the last weekday of the month; then COUNT continues with
			// the last weekday of Jan, Feb, Mar.
			name:       "monthly last weekday bysetpos",
			dtstart:    "20240101T090000",
			rrule:      "FREQ=MONTHLY;BYDAY=MO,TU,WE,TH,FR;BYSETPOS=-1;COUNT=4",
			wantStarts: []string{"2024-01-01T09:00:00", "2024-01-31T09:00:00", "2024-02-29T09:00:00", "2024-03-29T09:00:00"},
		},
		{
			name:       "daily interval across DST keeps wall time",
			dtstart:    "20240308T090000",
			rrule:      "FREQ=DAILY;COUNT=4",
			wantStarts: []string{"2024-03-08T09:00:00", "2024-03-09T09:00:00", "2024-03-10T09:00:00", "2024-03-11T09:00:00"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lines := []string{
				"DTSTART;TZID=America/New_York:" + tc.dtstart,
				"RRULE:" + tc.rrule,
			}
			cal := buildCalendar(t, lines)
			series, err := ExpandCalendar(cal, testWindowStart, testWindowEnd, 100)
			require.NoError(t, err)
			require.Len(t, series, 1)
			require.Len(t, series[0].Result.Instances, len(tc.wantStarts))
			for i, inst := range series[0].Result.Instances {
				assert.Equal(t, TimeKindTZID, inst.Start.Kind)
				assert.Equal(t, "America/New_York", inst.Start.TZID)
				wall := inst.Start.Time.Format("2006-01-02T15:04:05")
				assert.Equal(t, tc.wantStarts[i], wall)
				if i == 0 {
					assert.True(t, hasKind(inst, SourceStart))
					assert.True(t, hasKind(inst, SourceRRule), "DTSTART coinciding with first RRULE candidate keeps both sources")
				} else {
					assert.True(t, hasKind(inst, SourceRRule))
				}
			}
		})
	}
}

// TestExpandExdateRdateConflict covers EXDATE removing generated instances and
// RDATE injecting new ones, including an RDATE at the same instant as an
// EXDATE (RDATE still injects; EXDATE only removes rule-generated/DTSTART
// occurrences) and two RRULEs collapsing onto one instant.
func TestExpandExdateRdateConflict(t *testing.T) {
	tests := []struct {
		name         string
		extraLines   []string
		wantInstants []string // UTC RFC3339
		wantKinds    func(t *testing.T, insts []*Instance)
	}{
		{
			name: "exdate removes rrule instance",
			extraLines: []string{
				"RRULE:FREQ=DAILY;COUNT=4",
				"EXDATE;TZID=America/New_York:20240103T090000",
			},
			wantInstants: []string{"2024-01-01T14:00:00Z", "2024-01-02T14:00:00Z", "2024-01-04T14:00:00Z"},
		},
		{
			name: "rdate injects",
			extraLines: []string{
				"RRULE:FREQ=DAILY;COUNT=2",
				"RDATE;TZID=America/New_York:20240110T090000",
			},
			wantInstants: []string{"2024-01-01T14:00:00Z", "2024-01-02T14:00:00Z", "2024-01-10T14:00:00Z"},
		},
		{
			name: "two rrules merge at same instant keeping both sources",
			extraLines: []string{
				"RRULE:FREQ=DAILY;COUNT=2",
				"RRULE:FREQ=DAILY;INTERVAL=1;COUNT=2",
			},
			wantInstants: []string{"2024-01-01T14:00:00Z", "2024-01-02T14:00:00Z"},
			wantKinds: func(t *testing.T, insts []*Instance) {
				counts := 0
				for _, s := range insts[0].Sources {
					if s.Kind == SourceRRule {
						counts++
					}
				}
				assert.Equal(t, 2, counts, "two RRULE sources attached to one instance")
			},
		},
		{
			// EXDATE authoritatively declares the date absent; an RDATE at the
			// same instant does not resurrect it (the EXDATE wins).
			name: "rdate same instant as exdate stays excluded",
			extraLines: []string{
				"RRULE:FREQ=DAILY;COUNT=4",
				"EXDATE;TZID=America/New_York:20240103T090000",
				"RDATE;TZID=America/New_York:20240103T090000",
			},
			wantInstants: []string{"2024-01-01T14:00:00Z", "2024-01-02T14:00:00Z", "2024-01-04T14:00:00Z"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lines := append([]string{"DTSTART;TZID=America/New_York:20240101T090000"}, tc.extraLines...)
			cal := buildCalendar(t, lines)
			series, err := ExpandCalendar(cal, testWindowStart, testWindowEnd, 100)
			require.NoError(t, err)
			insts := series[0].Result.Instances
			require.Len(t, insts, len(tc.wantInstants))
			for i, inst := range insts {
				assert.Equal(t, tc.wantInstants[i], inst.Start.Time.UTC().Format("2006-01-02T15:04:05Z"))
			}
			if tc.wantKinds != nil {
				tc.wantKinds(t, insts)
			}
		})
	}
}

// TestExpandDiagnosticsExcluded ensures EXDATEd occurrences are visible only
// via diagnostics and never in the normal iteration.
func TestExpandDiagnosticsExcluded(t *testing.T) {
	lines := []string{
		"DTSTART;TZID=America/New_York:20240101T090000",
		"RRULE:FREQ=DAILY;COUNT=4",
		"EXDATE;TZID=America/New_York:20240102T090000",
		"EXDATE;TZID=America/New_York:20240103T090000",
	}
	cal := buildCalendar(t, lines)
	master := cal.Events()[0]
	normal, err := ExpandEvent(master, testWindowStart, testWindowEnd, 100, nil)
	require.NoError(t, err)
	require.Len(t, normal.Instances, 2)
	assert.Empty(t, normal.Diagnostics.Excluded)

	diag, err := ExpandEvent(master, testWindowStart, testWindowEnd, 100, nil, WithDiagnostics())
	require.NoError(t, err)
	require.Len(t, diag.Instances, 2, "diagnostics do not change normal instances")
	require.Len(t, diag.Diagnostics.Excluded, 2)
	for _, ex := range diag.Diagnostics.Excluded {
		require.NotEmpty(t, ex.Reasons)
		assert.Equal(t, SourceExclusion, ex.Reasons[0].Kind)
	}
}

// TestExpandTruncation verifies that unbounded rules are capped and the result
// is distinguishable from a natural rule end.
func TestExpandTruncation(t *testing.T) {
	lines := []string{"DTSTART;TZID=America/New_York:20240101T090000", "RRULE:FREQ=DAILY"}
	cal := buildCalendar(t, lines)
	master := cal.Events()[0]
	res, err := ExpandEvent(master, testWindowStart, testWindowEnd, 5, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrExpansionTruncated))
	require.NotNil(t, res)
	assert.True(t, res.Truncated)
	assert.Equal(t, TruncationMaxInstances, res.Truncation)
	require.Len(t, res.Instances, 5)
	// The five returned must be the first five ascending instances.
	assert.Equal(t, "2024-01-01T14:00:00Z", res.Instances[0].Start.Time.UTC().Format(time.RFC3339))
	assert.Equal(t, "2024-01-05T14:00:00Z", res.Instances[4].Start.Time.UTC().Format(time.RFC3339))
}
