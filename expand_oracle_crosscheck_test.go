package ics

import (
	"testing"
	"time"
)

// TestOracleVsImplementation enumerates a finite matrix of small rule
// combinations and requires the independent brute-force oracle and the
// production expander to agree on every instant and its source categories.
func TestOracleVsImplementation(t *testing.T) {
	starts := map[string]string{
		"utc_jan_monday": "20240101T090000Z",
		"utc_mar_mid":    "20240313T090000Z",
	}
	rules := []string{
		"FREQ=DAILY;COUNT=4",
		"FREQ=DAILY;INTERVAL=2;COUNT=4",
		"FREQ=DAILY;UNTIL=20240108T090000Z",
		"FREQ=WEEKLY;BYDAY=MO,WE,FR;COUNT=6",
		"FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,FR;COUNT=4",
		"FREQ=MONTHLY;BYMONTHDAY=15;COUNT=4",
		"FREQ=MONTHLY;BYMONTHDAY=-1;COUNT=4",
		"FREQ=MONTHLY;BYDAY=2MO;COUNT=3",
		"FREQ=MONTHLY;BYDAY=-1FR;COUNT=3",
		"FREQ=MONTHLY;BYDAY=MO,TU,WE,TH,FR;BYSETPOS=-1;COUNT=3",
		"FREQ=YEARLY;COUNT=3",
		"FREQ=YEARLY;BYMONTH=6;BYMONTHDAY=15;COUNT=3",
	}

	for startName, startRaw := range starts {
		for _, ruleStr := range rules {
			orule, ok := parseOracleRule(ruleStr)
			if !ok {
				t.Fatalf("oracle declined to parse %q", ruleStr)
			}
			name := startName + "/" + ruleStr
			t.Run(name, func(t *testing.T) {
				cfg := defaultExpansionConfig()
				cfg.maxInstances = 200
				cfg.window = Window{From: mustUTC(t, "2024-01-01T00:00:00Z"), To: mustUTC(t, "2025-06-01T00:00:00Z")}
				startTV, err := cfg.parseTimeValueTyped(startRaw, nil)
				if err != nil {
					t.Fatal(err)
				}
				horizon := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
				oc := oracleExpansion(startTV, []*oracleRule{orule}, nil, nil, horizon)

				body := "BEGIN:VEVENT\r\nUID:u\r\nDTSTART:" + startRaw + "\r\nRRULE:" + ruleStr + "\r\nEND:VEVENT\r\n"
				cal := buildCalendar(t, body)
				res, err := ExpandEvent(cal.Events()[0],
					WithWindow(cfg.window),
					WithMaxInstances(200),
					WithDiagnostics())
				if err != nil {
					t.Fatal(err)
				}

				if len(res.Instances) != len(oc.ticks) {
					t.Fatalf("count mismatch: impl %d oracle %d", len(res.Instances), len(oc.ticks))
				}
				for i, ot := range oc.ticks {
					if !res.Instances[i].Start.Time.Equal(ot.t) {
						t.Fatalf("instance %d impl %s oracle %s", i, res.Instances[i].Start.Time, ot.t)
					}
					implHasDTStart := false
					implHasRule := false
					for _, s := range res.Instances[i].Sources {
						if s.Kind == SourceDTStart {
							implHasDTStart = true
						}
						if s.Kind == SourceRRULE {
							implHasRule = true
						}
					}
					if implHasDTStart != ot.sources["DTSTART"] {
						t.Errorf("instance %d DTSTART source mismatch", i)
					}
					if implHasRule != ot.sources["RRULE:0"] {
						t.Errorf("instance %d RRULE source mismatch", i)
					}
				}
			})
		}
	}
}

// TestOracleExdateRdate cross checks EXDATE/RDATE assembly against the oracle.
func TestOracleExdateRdate(t *testing.T) {
	cfg := defaultExpansionConfig()
	cfg.maxInstances = 100
	cfg.window = Window{From: mustUTC(t, "2024-01-01T00:00:00Z"), To: mustUTC(t, "2024-03-01T00:00:00Z")}
	startTV, err := cfg.parseTimeValueTyped("20240101T090000Z", nil)
	if err != nil {
		t.Fatal(err)
	}
	orule, ok := parseOracleRule("FREQ=DAILY;COUNT=20")
	if !ok {
		t.Fatal("rule parse")
	}
	rdates := []time.Time{mustUTC(t, "2024-01-15T12:00:00Z"), mustUTC(t, "2024-01-02T09:00:00Z")}
	exdates := []time.Time{mustUTC(t, "2024-01-03T09:00:00Z"), mustUTC(t, "2024-01-15T12:00:00Z")}
	oc := oracleExpansion(startTV, []*oracleRule{orule}, rdates, exdates, mustUTC(t, "2024-03-01T00:00:00Z"))

	body := "BEGIN:VEVENT\r\nUID:u\r\nDTSTART:20240101T090000Z\r\nRRULE:FREQ=DAILY;COUNT=20\r\n" +
		"RDATE:20240115T120000Z,20240102T090000Z\r\nEXDATE:20240103T090000Z,20240115T120000Z\r\nEND:VEVENT\r\n"
	cal := buildCalendar(t, body)
	res, err := ExpandEvent(cal.Events()[0], WithWindow(cfg.window), WithMaxInstances(100), WithDiagnostics())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Instances) != len(oc.ticks) {
		t.Fatalf("count impl %d oracle %d", len(res.Instances), len(oc.ticks))
	}
	for i, ot := range oc.ticks {
		if !res.Instances[i].Start.Time.Equal(ot.t) {
			t.Fatalf("instance %d mismatch impl %s oracle %s", i, res.Instances[i].Start.Time, ot.t)
		}
	}
	if len(res.Excluded) != len(oc.excluded) {
		t.Fatalf("excluded mismatch impl %d oracle %d", len(res.Excluded), len(oc.excluded))
	}
}

// TestOracleMultiRule checks two rules collapsing onto shared instants.
func TestOracleMultiRule(t *testing.T) {
	cfg := defaultExpansionConfig()
	cfg.maxInstances = 100
	cfg.window = Window{From: mustUTC(t, "2024-01-01T00:00:00Z"), To: mustUTC(t, "2024-02-01T00:00:00Z")}
	startTV, err := cfg.parseTimeValueTyped("20240101T090000Z", nil)
	if err != nil {
		t.Fatal(err)
	}
	r1, _ := parseOracleRule("FREQ=DAILY;COUNT=10")
	r2, _ := parseOracleRule("FREQ=WEEKLY;BYDAY=MO,WE,FR;COUNT=5")
	oc := oracleExpansion(startTV, []*oracleRule{r1, r2}, nil, nil, mustUTC(t, "2024-02-01T00:00:00Z"))

	body := "BEGIN:VEVENT\r\nUID:u\r\nDTSTART:20240101T090000Z\r\n" +
		"RRULE:FREQ=DAILY;COUNT=10\r\nRRULE:FREQ=WEEKLY;BYDAY=MO,WE,FR;COUNT=5\r\nEND:VEVENT\r\n"
	cal := buildCalendar(t, body)
	res, err := ExpandEvent(cal.Events()[0], WithWindow(cfg.window), WithMaxInstances(100))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Instances) != len(oc.ticks) {
		t.Fatalf("count impl %d oracle %d", len(res.Instances), len(oc.ticks))
	}
	for i, ot := range oc.ticks {
		if !res.Instances[i].Start.Time.Equal(ot.t) {
			t.Fatalf("instance %d impl %s oracle %s", i, res.Instances[i].Start.Time, ot.t)
		}
		nRules := 0
		for _, s := range res.Instances[i].Sources {
			if s.Kind == SourceRRULE {
				nRules++
			}
		}
		wantRules := 0
		if ot.sources["RRULE:0"] {
			wantRules++
		}
		if ot.sources["RRULE:1"] {
			wantRules++
		}
		if nRules != wantRules {
			t.Errorf("instance %s rule source count impl %d oracle %d", ot.t, nRules, wantRules)
		}
	}
}
