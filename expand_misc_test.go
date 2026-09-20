package ics

import (
	"testing"
	"time"
)

func TestExpandDurationAndEnd(t *testing.T) {
	t.Run("dtend duration preserved across dst", func(t *testing.T) {
		body := "BEGIN:VEVENT\r\nUID:u\r\nDTSTART;TZID=America/New_York:20240308T090000\r\nDTEND;TZID=America/New_York:20240308T100000\r\nRRULE:FREQ=DAILY;COUNT=3\r\nEND:VEVENT\r\n"
		cal := buildCalendar(t, body)
		res, err := ExpandCalendar(cal, WithWindow(Window{
			From: mustUTC(t, "2024-03-01T00:00:00Z"), To: mustUTC(t, "2024-04-01T00:00:00Z"),
		}))
		if err != nil {
			t.Fatal(err)
		}
		for _, in := range res[0].Instances {
			if d := in.End.Time.Sub(in.Start.Time); d != time.Hour {
				t.Fatalf("duration %s on %s, want 1h", d, in.Start.Time)
			}
		}
	})
	t.Run("duration property", func(t *testing.T) {
		body := "BEGIN:VEVENT\r\nUID:u\r\nDTSTART:20240101T090000Z\r\nDURATION:PT2H30M\r\nRRULE:FREQ=DAILY;COUNT=2\r\nEND:VEVENT\r\n"
		cal := buildCalendar(t, body)
		res, err := ExpandCalendar(cal, WithWindow(Window{
			From: mustUTC(t, "2024-01-01T00:00:00Z"), To: mustUTC(t, "2024-02-01T00:00:00Z"),
		}))
		if err != nil {
			t.Fatal(err)
		}
		for _, in := range res[0].Instances {
			if d := in.End.Time.Sub(in.Start.Time); d != 2*time.Hour+30*time.Minute {
				t.Fatalf("duration %s, want 2h30m", d)
			}
		}
	})
}

func TestExpandSubDaily(t *testing.T) {
	t.Run("hourly interval", func(t *testing.T) {
		body := "BEGIN:VEVENT\r\nUID:u\r\nDTSTART:20240101T090000Z\r\nRRULE:FREQ=HOURLY;INTERVAL=2;COUNT=4\r\nEND:VEVENT\r\n"
		cal := buildCalendar(t, body)
		res, err := ExpandCalendar(cal,
			WithWindow(Window{From: mustUTC(t, "2024-01-01T00:00:00Z"), To: mustUTC(t, "2024-01-02T00:00:00Z")}),
			WithMaxInstances(100))
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"09:00:00", "11:00:00", "13:00:00", "15:00:00"}
		if len(res[0].Instances) != 4 {
			t.Fatalf("count %d", len(res[0].Instances))
		}
		for i, w := range want {
			if got := res[0].Instances[i].Start.Time.UTC().Format("15:04:05"); got != w {
				t.Errorf("instance %d = %s, want %s", i, got, w)
			}
		}
	})
	t.Run("minutely byminute expansion within window fast forward", func(t *testing.T) {
		body := "BEGIN:VEVENT\r\nUID:u\r\nDTSTART:20240101T090000Z\r\nRRULE:FREQ=MINUTELY;BYMINUTE=0,15,30,45;BYHOUR=10\r\nEND:VEVENT\r\n"
		cal := buildCalendar(t, body)
		res, err := ExpandCalendar(cal,
			WithWindow(Window{From: mustUTC(t, "2024-01-02T10:00:00Z"), To: mustUTC(t, "2024-01-02T11:00:00Z")}),
			WithMaxInstances(100))
		if err != nil {
			t.Fatal(err)
		}
		if len(res[0].Instances) != 4 {
			t.Fatalf("want 4 quarter-hour instances on Jan 2, got %d", len(res[0].Instances))
		}
		if res[0].Truncated != NotTruncated {
			t.Fatalf("unexpected truncation %q", res[0].Truncated)
		}
	})
	t.Run("hourly byhour excludes dtstart hour but keeps dtstart instance", func(t *testing.T) {
		body := "BEGIN:VEVENT\r\nUID:u\r\nDTSTART:20240101T080000Z\r\nRRULE:FREQ=HOURLY;BYHOUR=9,17;COUNT=4\r\nEND:VEVENT\r\n"
		cal := buildCalendar(t, body)
		res, err := ExpandCalendar(cal,
			WithWindow(Window{From: mustUTC(t, "2024-01-01T00:00:00Z"), To: mustUTC(t, "2024-01-03T00:00:00Z")}),
			WithMaxInstances(100))
		if err != nil {
			t.Fatal(err)
		}
		want := []string{
			"2024-01-01T08:00:00Z", // DTSTART only (does not satisfy BYHOUR)
			"2024-01-01T09:00:00Z",
			"2024-01-01T17:00:00Z",
			"2024-01-02T09:00:00Z",
			"2024-01-02T17:00:00Z",
		}
		if len(res[0].Instances) != len(want) {
			t.Fatalf("count %d want %d", len(res[0].Instances), len(want))
		}
		for i, w := range want {
			if got := res[0].Instances[i].Start.Time.UTC().Format("2006-01-02T15:04:05Z"); got != w {
				t.Errorf("instance %d = %s, want %s", i, got, w)
			}
		}
		first := res[0].Instances[0]
		for _, s := range first.Sources {
			if s.Kind == SourceRRULE {
				t.Errorf("08:00 DTSTART must not carry an RRULE source: %+v", first.Sources)
			}
		}
	})
}

func TestExpandCountCountsPreWindow(t *testing.T) {
	// COUNT=3 starts Jan 1; a window opening Jan 3 only sees the last instance.
	body := "BEGIN:VEVENT\r\nUID:u\r\nDTSTART:20240101T090000Z\r\nRRULE:FREQ=DAILY;COUNT=3\r\nEND:VEVENT\r\n"
	cal := buildCalendar(t, body)
	res, err := ExpandCalendar(cal, WithWindow(Window{
		From: mustUTC(t, "2024-01-03T09:00:00Z"), To: mustUTC(t, "2024-02-01T00:00:00Z"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(res[0].Instances) != 1 {
		t.Fatalf("want 1 in-window instance, got %d", len(res[0].Instances))
	}
	if res[0].Instances[0].Start.Time.Day() != 3 {
		t.Fatalf("wrong day %d", res[0].Instances[0].Start.Time.Day())
	}
}

func TestExpandSourceOrderStable(t *testing.T) {
	// Add the same RDATE via a second pass and confirm sources remain
	// de-duplicated and deterministic.
	body := "BEGIN:VEVENT\r\nUID:u\r\nDTSTART:20240101T090000Z\r\nRRULE:FREQ=DAILY;COUNT=2\r\nRDATE:20240102T090000Z\r\nRDATE:20240102T090000Z\r\nEND:VEVENT\r\n"
	for i := 0; i < 5; i++ {
		cal := buildCalendar(t, body)
		res, err := ExpandCalendar(cal, WithWindow(Window{
			From: mustUTC(t, "2024-01-01T00:00:00Z"), To: mustUTC(t, "2024-02-01T00:00:00Z"),
		}))
		if err != nil {
			t.Fatal(err)
		}
		in := res[0].Instances[1]
		counts := map[InstanceSourceKind]int{}
		for _, s := range in.Sources {
			counts[s.Kind]++
		}
		if counts[SourceRDATE] != 1 || counts[SourceRRULE] != 1 {
			t.Fatalf("iteration %d sources not de-duplicated: %+v", i, in.Sources)
		}
	}
}

func TestExpandInvalidOptions(t *testing.T) {
	cal := buildCalendar(t, "BEGIN:VEVENT\r\nUID:u\r\nDTSTART:20240101T090000Z\r\nEND:VEVENT\r\n")
	if _, err := ExpandEvent(cal.Events()[0], WithWindow(Window{
		From: mustUTC(t, "2024-02-01T00:00:00Z"), To: mustUTC(t, "2024-01-01T00:00:00Z"),
	})); err == nil {
		t.Fatal("expected error for inverted window")
	}
	if _, err := ExpandEvent(nil); err == nil {
		t.Fatal("expected error for nil event")
	}
}

func TestExpandDeterministicAcrossRuns(t *testing.T) {
	// A busy event with two rules, multiple RDATEs and EXDATEs must produce a
	// bit-identical ordered result on repeated runs (map order independence).
	body := "BEGIN:VEVENT\r\nUID:u\r\nDTSTART:20240101T090000Z\r\n" +
		"RRULE:FREQ=DAILY;COUNT=12\r\nRRULE:FREQ=WEEKLY;BYDAY=MO,WE,FR;COUNT=6\r\n" +
		"RDATE:20240108T090000Z\r\nRDATE:20240115T090000Z\r\nRDATE:20240102T090000Z\r\n" +
		"EXDATE:20240103T090000Z\r\nEXDATE:20240110T090000Z\r\nEND:VEVENT\r\n"
	sig := func() string {
		cal := buildCalendar(t, body)
		res, err := ExpandCalendar(cal,
			WithWindow(Window{From: mustUTC(t, "2024-01-01T00:00:00Z"), To: mustUTC(t, "2024-02-01T00:00:00Z")}),
			WithDiagnostics())
		if err != nil {
			t.Fatal(err)
		}
		var sb []byte
		for _, in := range res[0].Instances {
			sb = append(sb, in.Start.Time.UTC().Format(time.RFC3339)...)
			for _, s := range in.Sources {
				sb = append(sb, string(s.Kind)...)
				sb = append(sb, byte('0'+s.RuleIndex))
			}
			sb = append(sb, '|')
		}
		for _, e := range res[0].Excluded {
			sb = append(sb, e.Start.Time.UTC().Format(time.RFC3339)...)
			sb = append(sb, string(e.Reason)...)
		}
		return string(sb)
	}
	first := sig()
	for i := 0; i < 20; i++ {
		if got := sig(); got != first {
			t.Fatalf("run %d differs", i)
		}
	}
}
