package ics

import (
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExpandSerializationRoundTrip is the key stability guarantee: serializing
// the calendar to text and re-parsing it must yield identical instants,
// source categories and override relationships for the same window.
func TestExpandSerializationRoundTrip(t *testing.T) {
	input := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:rt\r\n" +
		"BEGIN:VTIMEZONE\r\nTZID:America/New_York\r\n" +
		"BEGIN:DAYLIGHT\r\nTZOFFSETFROM:-0500\r\nTZOFFSETTO:-0400\r\nTZNAME:EDT\r\n" +
		"DTSTART:19700308T020000\r\nRRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=2SU\r\nEND:DAYLIGHT\r\n" +
		"BEGIN:STANDARD\r\nTZOFFSETFROM:-0400\r\nTZOFFSETTO:-0500\r\nTZNAME:EST\r\n" +
		"DTSTART:19701101T020000\r\nRRULE:FREQ=YEARLY;BYMONTH=11;BYDAY=1SU\r\nEND:STANDARD\r\n" +
		"END:VTIMEZONE\r\n" +
		"BEGIN:VEVENT\r\nUID:rt1\r\nDTSTART;TZID=America/New_York:20240101T090000\r\n" +
		"DTEND;TZID=America/New_York:20240101T103000\r\n" +
		"RRULE:FREQ=WEEKLY;BYDAY=MO,WE,FR;COUNT=12\r\n" +
		"RDATE;TZID=America/New_York:20240215T090000\r\n" +
		"EXDATE;TZID=America/New_York:20240108T090000\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:rt1\r\nRECURRENCE-ID;TZID=America/New_York:20240110T090000\r\n" +
		"DTSTART;TZID=America/New_York:20240110T110000\r\nSUMMARY:shifted\r\nEND:VEVENT\r\n" +
		"END:VCALENDAR\r\n"
	cal1 := parseCalendar(t, input)
	serialized := cal1.Serialize()
	cal2 := parseCalendar(t, serialized)

	winS := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	winE := time.Date(2024, 4, 1, 0, 0, 0, 0, time.UTC)
	s1, err := ExpandCalendar(cal1, winS, winE, 200, WithDiagnostics())
	require.NoError(t, err)
	s2, err := ExpandCalendar(cal2, winS, winE, 200, WithDiagnostics())
	require.NoError(t, err)
	require.Len(t, s1, 1)
	require.Len(t, s2, 1)
	r1, r2 := s1[0].Result, s2[0].Result
	require.Len(t, r1.Instances, len(r2.Instances))
	require.Len(t, r1.Diagnostics.Excluded, len(r2.Diagnostics.Excluded))

	for i := range r1.Instances {
		a, b := r1.Instances[i], r2.Instances[i]
		assert.True(t, a.Start.Time.Equal(b.Start.Time), "instant %d %s vs %s", i, a.Start.Time, b.Start.Time)
		assert.Equal(t, a.Start.Kind, b.Start.Kind)
		assert.Equal(t, a.Start.TZID, b.Start.TZID)
		assert.Equal(t, sourceSignature(a), sourceSignature(b))
		assert.Equal(t, a.Override != nil, b.Override != nil)
		if a.Override != nil {
			assert.True(t, a.Override.NewStart.Time.Equal(b.Override.NewStart.Time))
			assert.True(t, a.Start.Time.Equal(b.Start.Time))
		}
		assert.True(t, a.End.Time.Equal(b.End.Time), "end %d %s vs %s", i, a.End.Time, b.End.Time)
	}
	for i := range r1.Diagnostics.Excluded {
		assert.True(t, r1.Diagnostics.Excluded[i].Start.Time.Equal(r2.Diagnostics.Excluded[i].Start.Time))
	}
}

func sourceSignature(inst *Instance) []SourceKind {
	out := make([]SourceKind, len(inst.Sources))
	for i, s := range inst.Sources {
		out[i] = s.Kind
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// TestExpandHostTimezoneIndependent runs the same floating-time expansion
// under a forced non-UTC host local zone and confirms instants (represented as
// wall clocks in the synthetic FLOATING zone) are identical.
func TestExpandHostTimezoneIndependent(t *testing.T) {
	lines := []string{"DTSTART:20240101T090000", "RRULE:FREQ=DAILY;COUNT=3", "RDATE:20240201T090000"}
	cal := buildCalendar(t, lines)
	zone := time.FixedZone("HOST+05:30", 5*3600+1800)
	loc := time.Local
	time.Local = zone
	defer func() { time.Local = loc }()

	res, err := ExpandEvent(cal.Events()[0], testWindowStart, testWindowEnd, 10, nil)
	require.NoError(t, err)
	require.Len(t, res.Instances, 4)
	assert.Equal(t, "2024-01-01T09:00:00Z", res.Instances[0].Start.Time.UTC().Format(time.RFC3339))
	assert.Equal(t, "2024-01-02T09:00:00Z", res.Instances[1].Start.Time.UTC().Format(time.RFC3339))
	assert.Equal(t, "2024-01-03T09:00:00Z", res.Instances[2].Start.Time.UTC().Format(time.RFC3339))
	assert.Equal(t, "2024-02-01T09:00:00Z", res.Instances[3].Start.Time.UTC().Format(time.RFC3339))
	for _, inst := range res.Instances {
		assert.Equal(t, TimeKindFloating, inst.Start.Kind)
		assert.Equal(t, "FLOATING", inst.Start.Time.Location().String())
	}
}

// TestExpandAllDayEvents checks VALUE=DATE semantics: whole-day instants,
// half-open end windowing by day, and DTEND-exclusive duration in days.
func TestExpandAllDayEvents(t *testing.T) {
	lines := []string{
		"DTSTART;VALUE=DATE:20240101",
		"DTEND;VALUE=DATE:20240103",
		"RRULE:FREQ=DAILY;COUNT=3",
	}
	cal := buildCalendar(t, lines)
	res, err := ExpandEvent(cal.Events()[0], testWindowStart, testWindowEnd, 10, nil)
	require.NoError(t, err)
	require.Len(t, res.Instances, 3)
	for i, inst := range res.Instances {
		assert.Equal(t, TimeKindDate, inst.Start.Kind)
		assert.True(t, inst.Start.DateOnly)
		assert.Equal(t, 2024, inst.Start.Time.Year())
		assert.Equal(t, time.January, inst.Start.Time.Month())
		assert.Equal(t, 1+i, inst.Start.Time.Day())
		assert.Equal(t, time.January, inst.End.Time.Month())
		assert.Equal(t, 3+i, inst.End.Time.Day(), "DTEND-exclusive two-day span")
		assert.Equal(t, TimeKindDate, inst.End.Kind)
	}
}

// TestExpandStableSorting ensures output is ascending even when properties are
// given out of order and multiple rules/RDATES interleave.
func TestExpandStableSorting(t *testing.T) {
	lines := []string{
		"DTSTART:20240105T090000Z",
		"RRULE:FREQ=WEEKLY;COUNT=3",
		"RDATE:20240102T090000Z,20240120T090000Z",
	}
	cal := buildCalendar(t, lines)
	res, err := ExpandEvent(cal.Events()[0], testWindowStart, testWindowEnd, 100, nil)
	require.NoError(t, err)
	require.Len(t, res.Instances, 5)
	var prev time.Time
	for i, inst := range res.Instances {
		if i > 0 {
			assert.False(t, inst.Start.Time.Before(prev), "instances must be ascending")
		}
		prev = inst.Start.Time
	}
	assert.Equal(t, "2024-01-02T09:00:00Z", res.Instances[0].Start.Time.UTC().Format(time.RFC3339))
	assert.Equal(t, "2024-01-20T09:00:00Z", res.Instances[4].Start.Time.UTC().Format(time.RFC3339))
}
