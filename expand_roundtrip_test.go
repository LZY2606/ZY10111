package ics

import (
	"strings"
	"testing"
	"time"
)

func TestExpandSerializationRoundTrip(t *testing.T) {
	data := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:test\r\n" +
		"BEGIN:VTIMEZONE\r\nTZID:America/New_York\r\n" +
		"BEGIN:DAYLIGHT\r\nTZOFFSETFROM:-0500\r\nTZOFFSETTO:-0400\r\nTZNAME:EDT\r\nDTSTART:19700308T020000\r\nRRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=2SU\r\nEND:DAYLIGHT\r\n" +
		"BEGIN:STANDARD\r\nTZOFFSETFROM:-0400\r\nTZOFFSETTO:-0500\r\nTZNAME:EST\r\nDTSTART:19701101T020000\r\nRRULE:FREQ=YEARLY;BYMONTH=11;BYDAY=1SU\r\nEND:STANDARD\r\n" +
		"END:VTIMEZONE\r\n" +
		"BEGIN:VEVENT\r\nUID:rt\r\nDTSTART;TZID=America/New_York:20240307T090000\r\n" +
		"RRULE:FREQ=DAILY;COUNT=5\r\nEXDATE;TZID=America/New_York:20240308T090000\r\n" +
		"RDATE;TZID=America/New_York:20240320T090000\r\nSUMMARY:base\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:rt\r\nRECURRENCE-ID;TZID=America/New_York:20240310T090000\r\n" +
		"DTSTART;TZID=America/New_York:20240310T160000\r\nSUMMARY:moved\r\nEND:VEVENT\r\n" +
		"END:VCALENDAR\r\n"

	win := Window{From: mustUTC(t, "2024-03-01T00:00:00Z"), To: mustUTC(t, "2024-04-01T00:00:00Z")}
	first := expandSerialized(t, data, win)

	cal, err := ParseCalendar(strings.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	if err := cal.SerializeTo(&sb); err != nil {
		t.Fatal(err)
	}
	second := expandSerialized(t, sb.String(), win)

	if len(first.Instances) != len(second.Instances) {
		t.Fatalf("instance count changed: %d -> %d", len(first.Instances), len(second.Instances))
	}
	for i := range first.Instances {
		a, b := first.Instances[i], second.Instances[i]
		if !a.Start.Time.Equal(b.Start.Time) {
			t.Errorf("instance %d instant changed: %s -> %s", i, a.Start.Time, b.Start.Time)
		}
		if a.Start.Kind != b.Start.Kind || a.Start.TZID != b.Start.TZID {
			t.Errorf("instance %d semantics changed: %s/%s -> %s/%s", i, a.Start.Kind, a.Start.TZID, b.Start.Kind, b.Start.TZID)
		}
		if (a.Override == nil) != (b.Override == nil) {
			t.Errorf("instance %d override relation changed: %v -> %v", i, a.Override != nil, b.Override != nil)
		}
		if len(a.Sources) != len(b.Sources) {
			t.Errorf("instance %d source count changed: %d -> %d", i, len(a.Sources), len(b.Sources))
		}
	}
}

func expandSerialized(t *testing.T, data string, win Window) *ExpansionResult {
	t.Helper()
	cal, err := ParseCalendar(strings.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	results, err := ExpandCalendar(cal, WithWindow(win), WithMaxInstances(100), WithDiagnostics())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("want 1 group, got %d", len(results))
	}
	return results[0]
}

func TestExpandHostTimezoneIndependent(t *testing.T) {
	body := "BEGIN:VEVENT\r\nUID:u\r\nDTSTART:20240101T090000\r\nRRULE:FREQ=DAILY;COUNT=3\r\nEND:VEVENT\r\n"
	win := Window{From: mustUTC(t, "2024-01-01T00:00:00Z"), To: mustUTC(t, "2024-02-01T00:00:00Z")}
	expand := func() []string {
		cal := buildCalendar(t, body)
		res, err := ExpandCalendar(cal, WithWindow(win))
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, len(res[0].Instances))
		for i, in := range res[0].Instances {
			out[i] = in.Start.Time.UTC().Format(time.RFC3339)
		}
		return out
	}
	orig := time.Local
	defer func() { time.Local = orig }()

	time.Local = time.UTC
	a := expand()
	loc, _ := time.LoadLocation("America/New_York")
	time.Local = loc
	b := expand()
	tok, _ := time.LoadLocation("Asia/Tokyo")
	time.Local = tok
	c := expand()

	for i := range a {
		if a[i] != b[i] || a[i] != c[i] {
			t.Fatalf("floating expansion depends on host zone:\n%v\n%v\n%v", a, b, c)
		}
	}
}

func TestExpandDSTFallBack(t *testing.T) {
	// America/New_York falls back at 02:00 EDT to 01:00 EST on 2024-11-03.
	// A daily 02:30 local rule crosses the offset flip between consecutive days.
	body := "BEGIN:VEVENT\r\nUID:u\r\nDTSTART;TZID=America/New_York:20241102T023000\r\nRRULE:FREQ=DAILY;COUNT=3\r\nEND:VEVENT\r\n"
	cal := buildCalendar(t, body)
	res, err := ExpandCalendar(cal, WithWindow(Window{
		From: mustUTC(t, "2024-11-01T00:00:00Z"),
		To:   mustUTC(t, "2024-11-06T00:00:00Z"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2024-11-02T06:30:00Z", "2024-11-03T07:30:00Z", "2024-11-04T07:30:00Z"}
	r := res[0]
	if len(r.Instances) != len(want) {
		t.Fatalf("count %d, want %d", len(r.Instances), len(want))
	}
	for i, w := range want {
		if got := r.Instances[i].Start.Time.UTC().Format("2006-01-02T15:04:05Z"); got != w {
			t.Errorf("instance %d = %s, want %s", i, got, w)
		}
	}
}

func TestExpandAllDay(t *testing.T) {
	body := "BEGIN:VEVENT\r\nUID:u\r\nDTSTART;VALUE=DATE:20240101\r\nRRULE:FREQ=DAILY;COUNT=3\r\nEXDATE;VALUE=DATE:20240102\r\nEND:VEVENT\r\n"
	cal := buildCalendar(t, body)
	res, err := ExpandCalendar(cal,
		WithWindow(Window{From: mustUTC(t, "2024-01-01T00:00:00Z"), To: mustUTC(t, "2024-02-01T00:00:00Z")}),
		WithDiagnostics())
	if err != nil {
		t.Fatal(err)
	}
	r := res[0]
	if len(r.Instances) != 2 {
		t.Fatalf("want 2 all-day instances, got %d", len(r.Instances))
	}
	for _, in := range r.Instances {
		if in.Start.Kind != TimeKindDate || !in.Start.DateOnly {
			t.Fatalf("want DATE kind, got %s", in.Start.Kind)
		}
	}
	if r.Instances[0].Start.Time.UTC().Format("2006-01-02") != "2024-01-01" ||
		r.Instances[1].Start.Time.UTC().Format("2006-01-02") != "2024-01-03" {
		t.Fatalf("all-day dates wrong: %s %s", r.Instances[0].Start.Time, r.Instances[1].Start.Time)
	}
	if len(r.Excluded) != 1 || r.Excluded[0].Reason != ExcludedByEXDATE {
		t.Fatalf("all-day exdate wrong: %+v", r.Excluded)
	}
}

func TestExpandEXRULE(t *testing.T) {
	body := "BEGIN:VEVENT\r\nUID:u\r\nDTSTART:20240101T090000Z\r\nRRULE:FREQ=DAILY;COUNT=6\r\nEXRULE:FREQ=DAILY;INTERVAL=2\r\nEND:VEVENT\r\n"
	cal := buildCalendar(t, body)
	res, err := ExpandCalendar(cal,
		WithWindow(Window{From: mustUTC(t, "2024-01-01T00:00:00Z"), To: mustUTC(t, "2024-02-01T00:00:00Z")}),
		WithDiagnostics())
	if err != nil {
		t.Fatal(err)
	}
	wantDays := []int{2, 4, 6}
	r := res[0]
	if len(r.Instances) != 3 {
		t.Fatalf("want 3 instances after EXRULE, got %d", len(r.Instances))
	}
	for i, d := range wantDays {
		if r.Instances[i].Start.Time.Day() != d {
			t.Errorf("instance %d day = %d, want %d", i, r.Instances[i].Start.Time.Day(), d)
		}
	}
	if len(r.Excluded) != 3 {
		t.Fatalf("want 3 exrule diagnostics, got %d", len(r.Excluded))
	}
}
