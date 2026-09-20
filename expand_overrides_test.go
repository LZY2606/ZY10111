package ics

import (
	"testing"
	"time"
)

func mustLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestExpandRecurrenceID(t *testing.T) {
	t.Run("override moves an instance", func(t *testing.T) {
		runExpandCase(t, expandCase{
			body: "BEGIN:VEVENT\r\nUID:u\r\nDTSTART;TZID=America/New_York:20240307T090000\r\nRRULE:FREQ=DAILY;COUNT=5\r\nSUMMARY:base\r\nEND:VEVENT\r\n" +
				"BEGIN:VEVENT\r\nUID:u\r\nRECURRENCE-ID;TZID=America/New_York:20240309T090000\r\nDTSTART;TZID=America/New_York:20240309T150000\r\nSUMMARY:moved\r\nEND:VEVENT\r\n",
			from: "2024-03-01T00:00:00Z", to: "2024-04-01T00:00:00Z",
			want: []wantInstance{
				{"2024-03-07T14:00:00Z", []InstanceSourceKind{SourceDTStart, SourceRRULE}, false},
				{"2024-03-08T14:00:00Z", []InstanceSourceKind{SourceRRULE}, false},
				{"2024-03-09T20:00:00Z", nil, true},
				{"2024-03-10T13:00:00Z", []InstanceSourceKind{SourceRRULE}, false},
				{"2024-03-11T13:00:00Z", []InstanceSourceKind{SourceRRULE}, false},
			},
		})
	})

	t.Run("cancelled override removes and diagnoses", func(t *testing.T) {
		runExpandCase(t, expandCase{
			body: "BEGIN:VEVENT\r\nUID:u\r\nDTSTART:20240101T090000Z\r\nRRULE:FREQ=DAILY;COUNT=3\r\nEND:VEVENT\r\n" +
				"BEGIN:VEVENT\r\nUID:u\r\nRECURRENCE-ID:20240102T090000Z\r\nDTSTART:20240102T090000Z\r\nSTATUS:CANCELLED\r\nEND:VEVENT\r\n",
			from: "2024-01-01T00:00:00Z", to: "2024-02-01T00:00:00Z",
			diagnostics: true,
			want: []wantInstance{
				{"2024-01-01T09:00:00Z", []InstanceSourceKind{SourceDTStart, SourceRRULE}, false},
				{"2024-01-03T09:00:00Z", []InstanceSourceKind{SourceRRULE}, false},
			},
			wantExcl: []string{"2024-01-02T09:00:00Z"},
		})
	})

	t.Run("orphan override reported only in diagnostics", func(t *testing.T) {
		cal := buildCalendar(t,
			"BEGIN:VEVENT\r\nUID:u\r\nDTSTART:20240101T090000Z\r\nRRULE:FREQ=DAILY;COUNT=2\r\nEND:VEVENT\r\n"+
				"BEGIN:VEVENT\r\nUID:u\r\nRECURRENCE-ID:20240120T090000Z\r\nDTSTART:20240120T090000Z\r\nEND:VEVENT\r\n")
		quiet, err := ExpandCalendar(cal, WithWindow(Window{From: mustUTC(t, "2024-01-01T00:00:00Z"), To: mustUTC(t, "2024-02-01T00:00:00Z")}))
		if err != nil {
			t.Fatal(err)
		}
		if len(quiet[0].OrphanOverrides) != 0 {
			t.Fatalf("orphans should be hidden without diagnostics, got %d", len(quiet[0].OrphanOverrides))
		}
		cal = buildCalendar(t,
			"BEGIN:VEVENT\r\nUID:u\r\nDTSTART:20240101T090000Z\r\nRRULE:FREQ=DAILY;COUNT=2\r\nEND:VEVENT\r\n"+
				"BEGIN:VEVENT\r\nUID:u\r\nRECURRENCE-ID:20240120T090000Z\r\nDTSTART:20240120T090000Z\r\nEND:VEVENT\r\n")
		diag, err := ExpandCalendar(cal,
			WithWindow(Window{From: mustUTC(t, "2024-01-01T00:00:00Z"), To: mustUTC(t, "2024-02-01T00:00:00Z")}),
			WithDiagnostics())
		if err != nil {
			t.Fatal(err)
		}
		if len(diag[0].OrphanOverrides) != 1 {
			t.Fatalf("expected 1 orphan override, got %d", len(diag[0].OrphanOverrides))
		}
	})
}

func TestExpandFloatingTime(t *testing.T) {
	body := "BEGIN:VEVENT\r\nUID:u\r\nDTSTART:20240101T090000\r\nRRULE:FREQ=DAILY;COUNT=3\r\nEXDATE:20240102T090000\r\nEND:VEVENT\r\n"
	cal := buildCalendar(t, body)
	res, err := ExpandCalendar(cal,
		WithWindow(Window{From: mustUTC(t, "2024-01-01T00:00:00Z"), To: mustUTC(t, "2024-02-01T00:00:00Z")}),
		WithDiagnostics())
	if err != nil {
		t.Fatal(err)
	}
	got := res[0]
	if len(got.Instances) != 2 {
		t.Fatalf("want 2 floating instances, got %d", len(got.Instances))
	}
	for _, in := range got.Instances {
		if in.Start.Kind != TimeKindFloating {
			t.Fatalf("want floating kind, got %s", in.Start.Kind)
		}
		if !in.Start.Time.Equal(in.Start.Time.UTC()) {
			t.Fatal("floating values must be deterministically localized")
		}
	}
	if got.Instances[0].Start.Time.Format("15:04:05") != "09:00:00" {
		t.Fatalf("floating wall clock lost: %s", got.Instances[0].Start.Time)
	}
	if len(got.Excluded) != 1 || got.Excluded[0].Start.Kind != TimeKindFloating {
		t.Fatalf("floating exdate not honored: %+v", got.Excluded)
	}

	// Same event with a different floating location yields shifted instants.
	cal2 := buildCalendar(t, body)
	tokyo, err := ExpandCalendar(cal2,
		WithWindow(Window{From: mustUTC(t, "2024-01-01T00:00:00Z"), To: mustUTC(t, "2024-02-01T00:00:00Z")}),
		WithFloatingLocation(mustLocation(t, "Asia/Tokyo")))
	if err != nil {
		t.Fatal(err)
	}
	if tokyo[0].Instances[0].Start.Time.Equal(got.Instances[0].Start.Time) {
		t.Fatal("floating location option had no effect")
	}
	if tokyo[0].Instances[0].Start.Time.UTC().Format("15:04:05") != "00:00:00" {
		t.Fatalf("expected 09:00 Tokyo == 00:00 UTC, got %s", tokyo[0].Instances[0].Start.Time.UTC())
	}
}

func TestExpandTimezoneSemantics(t *testing.T) {
	body := "BEGIN:VEVENT\r\nUID:u\r\nDTSTART;TZID=America/New_York:20240308T090000\r\nRRULE:FREQ=DAILY;COUNT=3\r\nEND:VEVENT\r\n"
	cal := buildCalendar(t, body)
	res, err := ExpandCalendar(cal, WithWindow(Window{From: mustUTC(t, "2024-03-01T00:00:00Z"), To: mustUTC(t, "2024-04-01T00:00:00Z")}))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2024-03-08T14:00:00Z", "2024-03-09T14:00:00Z", "2024-03-10T13:00:00Z"}
	for i, w := range want {
		if got := res[0].Instances[i].Start.Time.UTC().Format("2006-01-02T15:04:05Z"); got != w {
			t.Errorf("instance %d = %s, want %s (DST spring forward)", i, got, w)
		}
		if res[0].Instances[i].Start.TZID != "America/New_York" || res[0].Instances[i].Start.Kind != TimeKindTZID {
			t.Errorf("instance %d lost TZID semantics", i)
		}
	}
}

func TestExpandUnknownTZID(t *testing.T) {
	body := "BEGIN:VEVENT\r\nUID:u\r\nDTSTART;TZID=/Custom/Zone:20240101T090000\r\nRRULE:FREQ=DAILY;COUNT=2\r\nEND:VEVENT\r\n"
	cal := buildCalendar(t, body)

	// Without a resolver: a typed error, not a panic.
	if _, err := ExpandCalendar(cal, WithWindow(Window{From: mustUTC(t, "2024-01-01T00:00:00Z"), To: mustUTC(t, "2024-02-01T00:00:00Z")})); err == nil {
		t.Fatal("expected error for unknown TZID")
	}

	// With a mapper: expansion proceeds using the mapped location.
	cal = buildCalendar(t, body)
	loc := mustLocation(t, "Asia/Tokyo")
	res, err := ExpandCalendar(cal,
		WithWindow(Window{From: mustUTC(t, "2024-01-01T00:00:00Z"), To: mustUTC(t, "2024-02-01T00:00:00Z")}),
		WithExpandTimezoneMapper(func(tzid string) *time.Location {
			if tzid == "/Custom/Zone" {
				return loc
			}
			return nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	if got := res[0].Instances[0].Start.Time.UTC().Format("15:04:05"); got != "00:00:00" {
		t.Fatalf("custom TZID mapping wrong: %s", got)
	}
}
