package ics

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// This file contains an independent, deliberately simple "oracle" used to
// cross-check ExpandEvent over small finite combinations.
//
// The oracle does not share the production expansion loop. It re-parses each
// RRULE itself and answers membership by literally testing every candidate tick
// (one second for timed events, one day for all-day events) between DTSTART and
// a fixed horizon, applying every BY-part as a direct predicate. It only
// supports the small, finite rule subset exercised by the oracle test cases
// (FREQ DAILY/WEEKLY/MONTHLY/YEARLY with INTERVAL, COUNT, UNTIL, BYDAY,
// BYMONTHDAY, BYMONTH, BYSETPOS, WKST). Exotic parts fall back to a skip so a
// malformed oracle never silently agrees with the implementation.

type oracleRule struct {
	freq      string
	until     time.Time
	untilDate bool
	count     int
	interval  int
	byDay     []oracleWeekday
	byMday    []int
	byMonth   []int
	bySetPos  []int
	wkst      time.Weekday
}

type oracleWeekday struct {
	ord int
	wd  time.Weekday
}

type oracleTick struct {
	t       time.Time
	sources map[string]bool // category keys: DTSTART/RRULE:n/RDATE
}

type oracleExcluded struct {
	t      time.Time
	reason string
}

type oracleOutput struct {
	ticks    []oracleTick
	excluded []oracleExcluded
}

func parseOracleRule(s string) (*oracleRule, bool) {
	r := &oracleRule{interval: 1, wkst: time.Monday}
	for _, part := range strings.Split(s, ";") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			return nil, false
		}
		k, v := kv[0], kv[1]
		switch k {
		case "FREQ":
			switch v {
			case "DAILY", "WEEKLY", "MONTHLY", "YEARLY":
				r.freq = v
			default:
				return nil, false
			}
		case "INTERVAL":
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				return nil, false
			}
			r.interval = n
		case "COUNT":
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				return nil, false
			}
			r.count = n
		case "UNTIL":
			if len(v) == 8 {
				t, err := time.Parse("20060102", v)
				if err != nil {
					return nil, false
				}
				r.until = t
				r.untilDate = true
			} else if strings.HasSuffix(v, "Z") {
				t, err := time.Parse("20060102T150405Z", v)
				if err != nil {
					return nil, false
				}
				r.until = t
			} else {
				return nil, false
			}
		case "BYDAY":
			for _, item := range strings.Split(v, ",") {
				wd, ord, ok := parseOracleByDay(item)
				if !ok {
					return nil, false
				}
				r.byDay = append(r.byDay, oracleWeekday{ord, wd})
			}
		case "BYMONTHDAY":
			for _, item := range strings.Split(v, ",") {
				n, err := strconv.Atoi(item)
				if err != nil || n == 0 || n < -31 || n > 31 {
					return nil, false
				}
				r.byMday = append(r.byMday, n)
			}
		case "BYMONTH":
			for _, item := range strings.Split(v, ",") {
				n, err := strconv.Atoi(item)
				if err != nil || n < 1 || n > 12 {
					return nil, false
				}
				r.byMonth = append(r.byMonth, n)
			}
		case "BYSETPOS":
			for _, item := range strings.Split(v, ",") {
				n, err := strconv.Atoi(item)
				if err != nil || n == 0 {
					return nil, false
				}
				r.bySetPos = append(r.bySetPos, n)
			}
		case "WKST":
			switch v {
			case "SU":
				r.wkst = time.Sunday
			case "MO":
				r.wkst = time.Monday
			case "TU":
				r.wkst = time.Tuesday
			case "WE":
				r.wkst = time.Wednesday
			case "TH":
				r.wkst = time.Thursday
			case "FR":
				r.wkst = time.Friday
			case "SA":
				r.wkst = time.Saturday
			}
		default:
			// Unsupported part: the oracle declines the case.
			return nil, false
		}
	}
	if r.freq == "" {
		return nil, false
	}
	return r, true
}

func parseOracleByDay(s string) (time.Weekday, int, bool) {
	var wd time.Weekday
	switch s[len(s)-2:] {
	case "SU":
		wd = time.Sunday
	case "MO":
		wd = time.Monday
	case "TU":
		wd = time.Tuesday
	case "WE":
		wd = time.Wednesday
	case "TH":
		wd = time.Thursday
	case "FR":
		wd = time.Friday
	case "SA":
		wd = time.Saturday
	default:
		return 0, 0, false
	}
	ord := 0
	if len(s) > 2 {
		n, err := strconv.Atoi(s[:len(s)-2])
		if err != nil || n == 0 {
			return 0, 0, false
		}
		ord = n
	}
	return wd, ord, true
}

// oracleExpansion is the brute-force recurrence set of one event.
func oracleExpansion(start TimeValue, rules []*oracleRule, rdates []time.Time, exdates []time.Time, horizon time.Time) oracleOutput {
	step := time.Second
	dateOnly := start.DateOnly
	if dateOnly {
		step = 24 * time.Hour
	}

	merged := map[int64]oracleTick{}
	note := func(t time.Time, src string) {
		k := t.UnixNano()
		tk, ok := merged[k]
		if !ok {
			tk = oracleTick{t: t, sources: map[string]bool{}}
		}
		tk.sources[src] = true
		merged[k] = tk
	}
	note(start.Time, "DTSTART")

	for ri, rule := range rules {
		var emitted []time.Time
		for t := start.Time; !t.After(horizon); t = t.Add(step) {
			if rule.matches(start.Time, t) {
				emitted = append(emitted, t)
				if rule.count > 0 && len(emitted) >= rule.count {
					break
				}
			}
			if rule.count == 0 && !rule.until.IsZero() && t.After(oracleUntilEnd(rule, start)) {
				break
			}
		}
		for _, t := range emitted {
			note(t, "RRULE:"+strconv.Itoa(ri))
		}
	}

	for _, rd := range rdates {
		note(rd, "RDATE")
	}

	var excluded []oracleExcluded
	for _, ex := range exdates {
		if tk, ok := merged[ex.UnixNano()]; ok {
			excluded = append(excluded, oracleExcluded{t: tk.t, reason: "EXDATE"})
			delete(merged, ex.UnixNano())
		}
	}

	ticks := make([]oracleTick, 0, len(merged))
	for _, tk := range merged {
		ticks = append(ticks, tk)
	}
	sort.Slice(ticks, func(i, j int) bool { return ticks[i].t.Before(ticks[j].t) })
	sort.Slice(excluded, func(i, j int) bool { return excluded[i].t.Before(excluded[j].t) })
	return oracleOutput{ticks: ticks, excluded: excluded}
}

func oracleUntilEnd(r *oracleRule, start TimeValue) time.Time {
	if r.until.IsZero() {
		return time.Time{}
	}
	if r.untilDate {
		// End of the UNTIL day in the rule's own zone coordinates.
		return time.Date(r.until.Year(), r.until.Month(), r.until.Day(), 23, 59, 59, 0, start.Time.Location())
	}
	return r.until
}

// matches is the direct per-tick RFC 5545 predicate.
func (r *oracleRule) matches(start, t time.Time) bool {
	if !r.until.IsZero() && t.After(oracleUntilEnd(r, timeValueOf(start))) {
		return false
	}
	switch r.freq {
	case "DAILY":
		days := int(t.Sub(start).Hours() / 24)
		if days < 0 || days%r.interval != 0 {
			return false
		}
	case "WEEKLY":
		if !oracleWeekIntervalMatches(start, t, r) {
			return false
		}
	case "MONTHLY":
		startPeriod := (start.Year()*12 + int(start.Month()) - 1)
		tPeriod := (t.Year()*12 + int(t.Month()) - 1)
		months := tPeriod - startPeriod
		if months < 0 || months%r.interval != 0 {
			return false
		}
	case "YEARLY":
		years := t.Year() - start.Year()
		if years < 0 || years%r.interval != 0 {
			return false
		}
	}

	if len(r.byMonth) > 0 && !intInList(int(t.Month()), r.byMonth) {
		return false
	}

	dayOK := true
	switch {
	case len(r.byMday) > 0:
		dayOK = oracleMonthDayMatch(t, r.byMday)
	case len(r.byDay) > 0:
		dayOK = oracleByDayMatch(start, t, r)
	default:
		switch r.freq {
		case "WEEKLY":
			dayOK = t.Weekday() == start.Weekday()
		case "MONTHLY":
			dayOK = t.Day() == start.Day()
		case "YEARLY":
			dayOK = t.Month() == start.Month() && t.Day() == start.Day()
		}
	}
	if !dayOK {
		return false
	}

	// Time-of-day must equal DTSTART for this oracle subset.
	if t.Hour() != start.Hour() || t.Minute() != start.Minute() || t.Second() != start.Second() {
		return false
	}

	if len(r.bySetPos) > 0 && !oracleSetPosMatch(start, t, r) {
		return false
	}
	return true
}

func timeValueOf(t time.Time) TimeValue {
	return TimeValue{Time: t}
}

func intInList(n int, xs []int) bool {
	for _, x := range xs {
		if x == n {
			return true
		}
	}
	return false
}

// oracleWeekIntervalMatches reports whether t's week is an INTERVAL-selected
// week measured from the week containing DTSTART.
func oracleWeekIntervalMatches(start, t time.Time, r *oracleRule) bool {
	startWS := oracleWeekStart(start, r.wkst)
	tWS := oracleWeekStart(t, r.wkst)
	weeks := int(tWS.Sub(startWS).Hours() / 24 / 7)
	return weeks >= 0 && weeks%r.interval == 0
}

func oracleWeekStart(t time.Time, wkst time.Weekday) time.Time {
	diff := (int(t.Weekday()) - int(wkst) + 7) % 7
	y, m, d := t.Date()
	return time.Date(y, m, d-diff, 0, 0, 0, 0, t.Location())
}

func oracleMonthDayMatch(t time.Time, mdays []int) bool {
	last := time.Date(t.Year(), t.Month()+1, 0, 0, 0, 0, 0, t.Location()).Day()
	for _, md := range mdays {
		if md > 0 && md == t.Day() {
			return true
		}
		if md < 0 && last+md+1 == t.Day() {
			return true
		}
	}
	return false
}

func oracleByDayMatch(start, t time.Time, r *oracleRule) bool {
	for _, bd := range r.byDay {
		if bd.wd != t.Weekday() {
			continue
		}
		if bd.ord == 0 {
			return true
		}
		switch r.freq {
		case "MONTHLY":
			if bd.ord > 0 && oracleOrdMonth(t, bd.wd) == bd.ord {
				return true
			}
			if bd.ord < 0 && oracleOrdMonthNeg(t, bd.wd) == bd.ord {
				return true
			}
		case "YEARLY":
			if bd.ord > 0 && oracleOrdYear(t, bd.wd) == bd.ord {
				return true
			}
			if bd.ord < 0 && oracleOrdYearNeg(t, bd.wd) == bd.ord {
				return true
			}
		default:
			return true
		}
	}
	return false
}

func oracleOrdMonth(t time.Time, wd time.Weekday) int {
	c := 0
	for d := 1; d <= t.Day(); d++ {
		if time.Date(t.Year(), t.Month(), d, 0, 0, 0, 0, t.Location()).Weekday() == wd {
			c++
		}
	}
	return c
}

func oracleOrdMonthNeg(t time.Time, wd time.Weekday) int {
	last := time.Date(t.Year(), t.Month()+1, 0, 0, 0, 0, 0, t.Location()).Day()
	c := 0
	for d := last; d >= t.Day(); d-- {
		if time.Date(t.Year(), t.Month(), d, 0, 0, 0, 0, t.Location()).Weekday() == wd {
			c--
		}
	}
	return c
}

func oracleOrdYear(t time.Time, wd time.Weekday) int {
	c := 0
	for d := time.Date(t.Year(), 1, 1, 0, 0, 0, 0, t.Location()); d.Year() == t.Year(); d = d.AddDate(0, 0, 1) {
		if d.Weekday() == wd && (d.Before(t) || d.Equal(t)) {
			c++
		}
	}
	return c
}

func oracleOrdYearNeg(t time.Time, wd time.Weekday) int {
	c := 0
	last := time.Date(t.Year(), 12, 31, 0, 0, 0, 0, t.Location())
	for d := last; !d.Before(t); d = d.AddDate(0, 0, -1) {
		if d.Weekday() == wd {
			c--
		}
	}
	return c
}

func oracleSetPosMatch(start, t time.Time, r *oracleRule) bool {
	var candidates []time.Time
	var pStart, pEnd time.Time
	switch r.freq {
	case "MONTHLY":
		pStart = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
		pEnd = time.Date(t.Year(), t.Month()+1, 0, 0, 0, 0, 0, t.Location())
	case "YEARLY":
		pStart = time.Date(t.Year(), 1, 1, 0, 0, 0, 0, t.Location())
		pEnd = time.Date(t.Year(), 12, 31, 0, 0, 0, 0, t.Location())
	case "WEEKLY":
		pStart = oracleWeekStart(t, r.wkst)
		pEnd = pStart.AddDate(0, 0, 7*r.interval-1)
	default:
		pStart = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
		pEnd = pStart
	}
	for d := pStart; !d.After(pEnd); d = d.AddDate(0, 0, 1) {
		cand := time.Date(d.Year(), d.Month(), d.Day(), start.Hour(), start.Minute(), start.Second(), 0, start.Location())
		dayPart := r
		cp := *dayPart
		cp.bySetPos = nil
		if cp.matches(start, cand) {
			candidates = append(candidates, cand)
		}
	}
	for _, pos := range r.bySetPos {
		idx := pos
		if idx < 0 {
			idx = len(candidates) + idx + 1
		}
		if idx >= 1 && idx <= len(candidates) && candidates[idx-1].Equal(t) {
			return true
		}
	}
	return false
}
