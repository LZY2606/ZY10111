package ics

import (
	"testing"
	"time"
)

func TestExpandRuleCombinations(t *testing.T) {
	tests := []expandCase{
		{
			name: "weekly multi day",
			body: "BEGIN:VEVENT\r\nUID:u\r\nDTSTART:20240101T090000Z\r\nRRULE:FREQ=WEEKLY;BYDAY=MO,WE,FR;COUNT=6\r\nEND:VEVENT\r\n",
			from: "2024-01-01T00:00:00Z", to: "2024-02-01T00:00:00Z",
			want: []wantInstance{
				{"2024-01-01T09:00:00Z", []InstanceSourceKind{SourceDTStart, SourceRRULE}, false},
				{"2024-01-03T09:00:00Z", []InstanceSourceKind{SourceRRULE}, false},
				{"2024-01-05T09:00:00Z", []InstanceSourceKind{SourceRRULE}, false},
				{"2024-01-08T09:00:00Z", []InstanceSourceKind{SourceRRULE}, false},
				{"2024-01-10T09:00:00Z", []InstanceSourceKind{SourceRRULE}, false},
				{"2024-01-12T09:00:00Z", []InstanceSourceKind{SourceRRULE}, false},
			},
		},
		{
			name: "weekly interval 2 with wkst",
			body: "BEGIN:VEVENT\r\nUID:u\r\nDTSTART:20240101T090000Z\r\nRRULE:FREQ=WEEKLY;INTERVAL=2;WKST=MO;BYDAY=MO,FR;COUNT=4\r\nEND:VEVENT\r\n",
			from: "2024-01-01T00:00:00Z", to: "2024-02-15T00:00:00Z",
			want: []wantInstance{
				{"2024-01-01T09:00:00Z", []InstanceSourceKind{SourceDTStart, SourceRRULE}, false},
				{"2024-01-05T09:00:00Z", []InstanceSourceKind{SourceRRULE}, false},
				{"2024-01-15T09:00:00Z", []InstanceSourceKind{SourceRRULE}, false},
				{"2024-01-19T09:00:00Z", []InstanceSourceKind{SourceRRULE}, false},
			},
		},
		{
			name: "monthly ordinal weekday last friday",
			body: "BEGIN:VEVENT\r\nUID:u\r\nDTSTART:20240126T090000Z\r\nRRULE:FREQ=MONTHLY;BYDAY=-1FR;COUNT=4\r\nEND:VEVENT\r\n",
			from: "2024-01-01T00:00:00Z", to: "2024-06-01T00:00:00Z",
			want: []wantInstance{
				{"2024-01-26T09:00:00Z", []InstanceSourceKind{SourceDTStart, SourceRRULE}, false},
				{"2024-02-23T09:00:00Z", []InstanceSourceKind{SourceRRULE}, false},
				{"2024-03-29T09:00:00Z", []InstanceSourceKind{SourceRRULE}, false},
				{"2024-04-26T09:00:00Z", []InstanceSourceKind{SourceRRULE}, false},
			},
		},
		{
			name: "monthly second monday",
			body: "BEGIN:VEVENT\r\nUID:u\r\nDTSTART:20240108T090000Z\r\nRRULE:FREQ=MONTHLY;BYDAY=2MO;COUNT=3\r\nEND:VEVENT\r\n",
			from: "2024-01-01T00:00:00Z", to: "2024-05-01T00:00:00Z",
			want: []wantInstance{
				{"2024-01-08T09:00:00Z", []InstanceSourceKind{SourceDTStart, SourceRRULE}, false},
				{"2024-02-12T09:00:00Z", []InstanceSourceKind{SourceRRULE}, false},
				{"2024-03-11T09:00:00Z", []InstanceSourceKind{SourceRRULE}, false},
			},
		},
		{
			name: "monthly bymonthday 31 skips short months",
			body: "BEGIN:VEVENT\r\nUID:u\r\nDTSTART:20240131T090000Z\r\nRRULE:FREQ=MONTHLY;BYMONTHDAY=31;COUNT=3\r\nEND:VEVENT\r\n",
			from: "2024-01-01T00:00:00Z", to: "2024-08-01T00:00:00Z",
			want: []wantInstance{
				{"2024-01-31T09:00:00Z", []InstanceSourceKind{SourceDTStart, SourceRRULE}, false},
				{"2024-03-31T09:00:00Z", []InstanceSourceKind{SourceRRULE}, false},
				{"2024-05-31T09:00:00Z", []InstanceSourceKind{SourceRRULE}, false},
			},
		},
		{
			name: "monthly bysetpos last weekday",
			body: "BEGIN:VEVENT\r\nUID:u\r\nDTSTART:20240131T090000Z\r\nRRULE:FREQ=MONTHLY;BYDAY=MO,TU,WE,TH,FR;BYSETPOS=-1;COUNT=3\r\nEND:VEVENT\r\n",
			from: "2024-01-01T00:00:00Z", to: "2024-06-01T00:00:00Z",
			want: []wantInstance{
				{"2024-01-31T09:00:00Z", []InstanceSourceKind{SourceDTStart, SourceRRULE}, false},
				{"2024-02-29T09:00:00Z", []InstanceSourceKind{SourceRRULE}, false},
				{"2024-03-29T09:00:00Z", []InstanceSourceKind{SourceRRULE}, false},
			},
		},
		{
			name: "yearly leap day",
			body: "BEGIN:VEVENT\r\nUID:u\r\nDTSTART:20240229T090000Z\r\nRRULE:FREQ=YEARLY;COUNT=3\r\nEND:VEVENT\r\n",
			from: "2024-01-01T00:00:00Z", to: "2033-01-01T00:00:00Z",
			want: []wantInstance{
				{"2024-02-29T09:00:00Z", []InstanceSourceKind{SourceDTStart, SourceRRULE}, false},
				{"2028-02-29T09:00:00Z", []InstanceSourceKind{SourceRRULE}, false},
				{"2032-02-29T09:00:00Z", []InstanceSourceKind{SourceRRULE}, false},
			},
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) { runExpandCase(t, tc) })
	}
}

func TestExpandExdateRdateConflicts(t *testing.T) {
	t.Run("exdate removes, rdate injects", func(t *testing.T) {
		runExpandCase(t, expandCase{
			body: "BEGIN:VEVENT\r\nUID:u\r\nDTSTART:20240101T090000Z\r\nRRULE:FREQ=DAILY;COUNT=4\r\nEXDATE:20240102T090000Z\r\nRDATE:20240110T090000Z\r\nEND:VEVENT\r\n",
			from: "2024-01-01T00:00:00Z", to: "2024-02-01T00:00:00Z",
			diagnostics: true,
			want: []wantInstance{
				{"2024-01-01T09:00:00Z", []InstanceSourceKind{SourceDTStart, SourceRRULE}, false},
				{"2024-01-03T09:00:00Z", []InstanceSourceKind{SourceRRULE}, false},
				{"2024-01-04T09:00:00Z", []InstanceSourceKind{SourceRRULE}, false},
				{"2024-01-10T09:00:00Z", []InstanceSourceKind{SourceRDATE}, false},
			},
			wantExcl: []string{"2024-01-02T09:00:00Z"},
		})
	})
	t.Run("exdate of rdate removes injected", func(t *testing.T) {
		runExpandCase(t, expandCase{
			body: "BEGIN:VEVENT\r\nUID:u\r\nDTSTART:20240101T090000Z\r\nRDATE:20240105T090000Z\r\nEXDATE:20240105T090000Z\r\nEND:VEVENT\r\n",
			from: "2024-01-01T00:00:00Z", to: "2024-02-01T00:00:00Z",
			diagnostics: true,
			want: []wantInstance{
				{"2024-01-01T09:00:00Z", []InstanceSourceKind{SourceDTStart}, false},
			},
			wantExcl: []string{"2024-01-05T09:00:00Z"},
		})
	})
	t.Run("rdate merging rrule keeps both sources", func(t *testing.T) {
		runExpandCase(t, expandCase{
			body: "BEGIN:VEVENT\r\nUID:u\r\nDTSTART:20240101T090000Z\r\nRRULE:FREQ=DAILY;COUNT=3\r\nRDATE:20240102T090000Z\r\nEND:VEVENT\r\n",
			from: "2024-01-01T00:00:00Z", to: "2024-02-01T00:00:00Z",
			want: []wantInstance{
				{"2024-01-01T09:00:00Z", []InstanceSourceKind{SourceDTStart, SourceRRULE}, false},
				{"2024-01-02T09:00:00Z", []InstanceSourceKind{SourceRRULE, SourceRDATE}, false},
				{"2024-01-03T09:00:00Z", []InstanceSourceKind{SourceRRULE}, false},
			},
		})
	})
}

var _ = time.RFC3339
