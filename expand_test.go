package ics

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"
)

func mustUTC(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

func buildCalendar(t *testing.T, body string) *Calendar {
	t.Helper()
	cal, err := ParseCalendar(strings.NewReader("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:test\r\n" + body + "END:VCALENDAR\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	return cal
}

type wantInstance struct {
	start    string
	kinds    []InstanceSourceKind
	override bool
}

type expandCase struct {
	name        string
	body        string
	from, to    string
	includeTo   bool
	max         int
	diagnostics bool
	want        []wantInstance
	wantExcl    []string
	wantTrunc   TruncatedReason
}

func runExpandCase(t *testing.T, tc expandCase) *ExpansionResult {
	t.Helper()
	cal := buildCalendar(t, tc.body)
	results, err := ExpandCalendar(cal,
		WithWindow(Window{From: mustUTC(t, tc.from), To: mustUTC(t, tc.to), IncludeTo: tc.includeTo}),
		WithMaxInstances(tc.max),
		diagnosticsOpt(tc.diagnostics),
	)
	if err != nil {
		t.Fatalf("ExpandCalendar: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result group, got %d", len(results))
	}
	res := results[0]

	var got []wantInstance
	for _, in := range res.Instances {
		var kinds []InstanceSourceKind
		for _, s := range in.Sources {
			kinds = append(kinds, s.Kind)
		}
		got = append(got, wantInstance{start: in.Start.Time.UTC().Format(time.RFC3339), kinds: kinds, override: in.Override != nil})
	}
	if len(got) != len(tc.want) {
		t.Fatalf("instance count = %d, want %d\ngot:  %+v\nwant: %+v", len(got), len(tc.want), got, tc.want)
	}
	for i := range got {
		if got[i].start != tc.want[i].start {
			t.Errorf("instance %d start = %s, want %s", i, got[i].start, tc.want[i].start)
		}
		if tc.want[i].kinds != nil && !equalKindSet(got[i].kinds, tc.want[i].kinds) {
			t.Errorf("instance %d (%s) kinds = %v, want %v", i, got[i].start, got[i].kinds, tc.want[i].kinds)
		}
		if got[i].override != tc.want[i].override {
			t.Errorf("instance %d (%s) override = %v, want %v", i, got[i].start, got[i].override, tc.want[i].override)
		}
	}
	// Stable ordering.
	for i := 1; i < len(res.Instances); i++ {
		if res.Instances[i-1].Start.Time.After(res.Instances[i].Start.Time) {
			t.Errorf("instances not sorted at %d", i)
		}
	}
	if res.Truncated != tc.wantTrunc {
		t.Errorf("truncated = %q, want %q", res.Truncated, tc.wantTrunc)
	}
	if tc.diagnostics {
		var excl []string
		for _, e := range res.Excluded {
			excl = append(excl, e.Start.Time.UTC().Format(time.RFC3339))
		}
		if len(excl) != len(tc.wantExcl) {
			t.Fatalf("excluded = %v, want %v", excl, tc.wantExcl)
		}
		for i := range excl {
			if excl[i] != tc.wantExcl[i] {
				t.Errorf("excluded %d = %s, want %s", i, excl[i], tc.wantExcl[i])
			}
		}
	}
	return res
}

func diagnosticsOpt(on bool) ExpandOption {
	if on {
		return WithDiagnostics()
	}
	return func(*expansionConfig) error { return nil }
}

func equalKindSet(a, b []InstanceSourceKind) bool {
	if len(a) != len(b) {
		return false
	}
	ca := append([]InstanceSourceKind(nil), a...)
	cb := append([]InstanceSourceKind(nil), b...)
	sort.Slice(ca, func(i, j int) bool { return ca[i] < ca[j] })
	sort.Slice(cb, func(i, j int) bool { return cb[i] < cb[j] })
	for i := range ca {
		if ca[i] != cb[i] {
			return false
		}
	}
	return true
}

func TestExpandTable(t *testing.T) {
	tests := []expandCase{
		{
			name: "single event no rrule",
			body: "BEGIN:VEVENT\r\nUID:u\r\nDTSTART:20240101T090000Z\r\nEND:VEVENT\r\n",
			from: "2024-01-01T00:00:00Z", to: "2024-02-01T00:00:00Z",
			want: []wantInstance{{start: "2024-01-01T09:00:00Z", kinds: []InstanceSourceKind{SourceDTStart}}},
		},
		{
			name: "daily count",
			body: "BEGIN:VEVENT\r\nUID:u\r\nDTSTART:20240101T090000Z\r\nRRULE:FREQ=DAILY;COUNT=3\r\nEND:VEVENT\r\n",
			from: "2024-01-01T00:00:00Z", to: "2024-02-01T00:00:00Z",
			want: []wantInstance{
				{"2024-01-01T09:00:00Z", []InstanceSourceKind{SourceDTStart, SourceRRULE}, false},
				{"2024-01-02T09:00:00Z", []InstanceSourceKind{SourceRRULE}, false},
				{"2024-01-03T09:00:00Z", []InstanceSourceKind{SourceRRULE}, false},
			},
		},
		{
			name: "window half open excludes end boundary",
			body: "BEGIN:VEVENT\r\nUID:u\r\nDTSTART:20240101T090000Z\r\nRRULE:FREQ=DAILY;COUNT=3\r\nEND:VEVENT\r\n",
			from: "2024-01-01T09:00:00Z", to: "2024-01-03T09:00:00Z",
			want: []wantInstance{
				{"2024-01-01T09:00:00Z", []InstanceSourceKind{SourceDTStart, SourceRRULE}, false},
				{"2024-01-02T09:00:00Z", []InstanceSourceKind{SourceRRULE}, false},
			},
		},
		{
			name: "window inclusive end boundary",
			body: "BEGIN:VEVENT\r\nUID:u\r\nDTSTART:20240101T090000Z\r\nRRULE:FREQ=DAILY;COUNT=3\r\nEND:VEVENT\r\n",
			from: "2024-01-01T09:00:00Z", to: "2024-01-03T09:00:00Z", includeTo: true,
			want: []wantInstance{
				{"2024-01-01T09:00:00Z", []InstanceSourceKind{SourceDTStart, SourceRRULE}, false},
				{"2024-01-02T09:00:00Z", []InstanceSourceKind{SourceRRULE}, false},
				{"2024-01-03T09:00:00Z", []InstanceSourceKind{SourceRRULE}, false},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { runExpandCase(t, tc) })
	}
}

func TestExpandUnboundedTruncation(t *testing.T) {
	body := "BEGIN:VEVENT\r\nUID:u\r\nDTSTART:20240101T090000Z\r\nRRULE:FREQ=DAILY\r\nEND:VEVENT\r\n"
	res := runExpandCase(t, expandCase{
		body: body,
		from: "2024-01-01T00:00:00Z", to: "2026-01-01T00:00:00Z", max: 5,
		want: []wantInstance{
			{"2024-01-01T09:00:00Z", []InstanceSourceKind{SourceDTStart, SourceRRULE}, false},
			{"2024-01-02T09:00:00Z", []InstanceSourceKind{SourceRRULE}, false},
			{"2024-01-03T09:00:00Z", []InstanceSourceKind{SourceRRULE}, false},
			{"2024-01-04T09:00:00Z", []InstanceSourceKind{SourceRRULE}, false},
			{"2024-01-05T09:00:00Z", []InstanceSourceKind{SourceRRULE}, false},
		},
		wantTrunc: TruncatedMaxInstances,
	})
	if res.Instances[len(res.Instances)-1].Start.Time.Day() != 5 {
		t.Fatal("truncation stopped at wrong instance")
	}
}

func TestExpandUntilNaturalEnd(t *testing.T) {
	runExpandCase(t, expandCase{
		name: "until ends naturally, not truncated",
		body: "BEGIN:VEVENT\r\nUID:u\r\nDTSTART:20240101T090000Z\r\nRRULE:FREQ=DAILY;UNTIL=20240103T090000Z\r\nEND:VEVENT\r\n",
		from: "2024-01-01T00:00:00Z", to: "2025-01-01T00:00:00Z", max: 100,
		want: []wantInstance{
			{"2024-01-01T09:00:00Z", []InstanceSourceKind{SourceDTStart, SourceRRULE}, false},
			{"2024-01-02T09:00:00Z", []InstanceSourceKind{SourceRRULE}, false},
			{"2024-01-03T09:00:00Z", []InstanceSourceKind{SourceRRULE}, false},
		},
		wantTrunc: NotTruncated,
	})
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

var _ = fmt.Sprintf
