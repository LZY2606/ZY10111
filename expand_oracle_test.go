package ics

import (
	"sort"
	"testing"
	"time"
)

// This file contains an independent recurrence oracle used to cross-check the
// production ruleIterator. The oracle deliberately uses a different strategy:
// instead of expanding rule periods and generating candidates, it scans a
// finite day-by-day calendar and asks a predicate "does the RRULE select this
// day?". It shares no enumeration, period, BYSETPOS or sorting code with the
// production engine - only the string parser (ParseRecurrenceRule), which is
// not the code under test. Only finite (COUNT- or UNTIL-bounded) rules with
// day-or-coarser frequency are covered; sub-day frequencies are unit-tested
// separately because minute-accurate day scanning would be needlessly slow.

type oracleRule struct {
	r     *RecurrenceRule
	start time.Time
}

// oracleExpand returns the occurrences of the rule (including DTSTART) up to
// a hard calendar horizon, in ascending UTC order.
func oracleExpand(t *testing.T, r *RecurrenceRule, start time.Time) []time.Time {
	t.Helper()
	loc := start.Location()
	hour, min, sec := start.Hour(), start.Minute(), start.Second()

	// Effective BY-hour/min/sec: missing parts default to DTSTART fields.
	hours := orDefault(r.ByHour, []int{hour})
	mins := orDefault(r.ByMinute, []int{min})
	secs := orDefault(r.BySecond, []int{sec})
	subDay := r.ByHour != nil || r.ByMinute != nil || r.BySecond != nil

	// Build every H:M:S tuple that a selected day emits.
	type hms struct{ h, m, s int }
	var tuples []hms
	for _, h := range hours {
		for _, mi := range mins {
			for _, s := range secs {
				tuples = append(tuples, hms{h, mi, s})
			}
		}
	}

	daySelects := func(y int, m time.Month, d int) bool {
		day := time.Date(y, m, d, 0, 0, 0, 0, loc)
		// Note: days before DTSTART within the first period ARE selected so
		// that BYSETPOS positions count against the full period set; they are
		// filtered out after setpos evaluation.
		switch r.Freq {
		case FrequencyDaily:
			if !inFreqStep(day, start, r.Interval, 0) {
				return false
			}
		case FrequencyWeekly:
			if !weekStep(day, start, r.Interval, r.Wkst) {
				return false
			}
		case FrequencyMonthly:
			if !monthStep(day, start, r.Interval) {
				return false
			}
		case FrequencyYearly:
			if !yearStep(day, start, r.Interval) {
				return false
			}
		default:
			return false
		}
		if r.Freq == FrequencyWeekly {
			if len(r.ByDay) > 0 {
				if !orContainsDay(flattenDays(r.ByDay), weekdayToICS(day.Weekday())) {
					return false
				}
			} else if day.Weekday() != start.Weekday() {
				return false
			}
		}
		if len(r.ByMonth) > 0 && !orContains(r.ByMonth, int(day.Month())) {
			return false
		}
		// Default day parts: with no day-expansion rules, MONTHLY/YEARLY keep
		// the DTSTART day of month; WEEKLY keeps DTSTART weekday (handled above);
		// DAILY keeps every selected day.
		if r.Freq == FrequencyMonthly && len(r.ByMonthDay) == 0 && len(r.ByDay) == 0 && len(r.ByYearDay) == 0 {
			if day.Day() != start.Day() {
				return false
			}
		}
		if r.Freq == FrequencyYearly && len(r.ByMonthDay) == 0 && len(r.ByDay) == 0 &&
			len(r.ByYearDay) == 0 && len(r.ByWeekNo) == 0 {
			if len(r.ByMonth) == 0 {
				if day.Month() != start.Month() || day.Day() != start.Day() {
					return false
				}
			} else if day.Day() != start.Day() {
				// BYMONTH without day parts: DTSTART day of month within each
				// named month; nonexistent days (Feb 30) simply do not match.
				return false
			}
		}
		if len(r.ByMonthDay) > 0 {
			dom := day.Day()
			n := daysInMonth(y, m)
			ok := false
			for _, md := range r.ByMonthDay {
				want := md
				if want < 0 {
					want = n + want + 1
				}
				if want == dom {
					ok = true
				}
			}
			if !ok {
				return false
			}
		}
		if len(r.ByYearDay) > 0 {
			yd := day.YearDay()
			total := daysInYear(y)
			ok := false
			for _, v := range r.ByYearDay {
				want := v
				if want < 0 {
					want = total + want + 1
				}
				if want == yd {
					ok = true
				}
			}
			if !ok {
				return false
			}
		}
		if len(r.ByDay) > 0 && !orBydayMatches(r.ByDay, y, m, d, day, loc, r.Freq, r.ByMonth) {
			return false
		}
		return true
	}

	// Period grouping for BYSETPOS: for daily rules the period is one day (so
	// setpos with multiple times only matters with sub-day tuples), weekly the
	// WKST week, monthly the month, yearly the year.
	inSamePeriod := func(a, b time.Time) bool {
		switch r.Freq {
		case FrequencyWeekly:
			return isoWeekKey(a, r.Wkst) == isoWeekKey(b, r.Wkst)
		case FrequencyMonthly:
			return a.Year() == b.Year() && a.Month() == b.Month()
		case FrequencyYearly:
			return a.Year() == b.Year()
		default:
			return sameDay(a, b)
		}
	}

	// Scan from the start of the period containing DTSTART so that BYSETPOS
	// positions count against the complete first period (e.g. BYDAY=TH with a
	// Wednesday DTSTART still counts the Thursday earlier that week/month).
	scanStart := firstPeriodStart(r, start, weekdayToGo(r.Wkst))
	if r.Wkst == "" {
		scanStart = firstPeriodStart(r, start, time.Monday)
	}
	sy0, sm0, sd0 := scanStart.Date()
	horizon := start.AddDate(30, 0, 0)
	var selected []time.Time
	cur := time.Date(sy0, sm0, sd0, 0, 0, 0, 0, loc)
	end := time.Date(horizon.Year(), horizon.Month(), horizon.Day(), 0, 0, 0, 0, loc)
	for !cur.After(end) {
		y, m, d := cur.Date()
		if daySelects(y, m, d) {
			if subDay {
				for _, tu := range tuples {
					selected = append(selected, normalizeWall(y, m, d, tu.h, tu.m, tu.s, loc))
				}
			} else {
				selected = append(selected, normalizeWall(y, m, d, hour, min, sec, loc))
			}
		}
		cur = cur.AddDate(0, 0, 1)
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Before(selected[j]) })

	// BYSETPOS positions refer to rule-generated candidates only. When
	// DTSTART does not satisfy the rule pattern it was never added to
	// selected (daySelects rejects it), so nothing to remove here; when it does
	// satisfy the pattern it legitimately participates in set positions.

	// BYSETPOS filter within periods.
	if len(r.BySetPos) > 0 {
		filtered := selected[:0]
		var period []time.Time
		flush := func() {
			n := len(period)
			for _, pos := range r.BySetPos {
				idx := pos
				if idx < 0 {
					idx = n + idx + 1
				} else {
					idx = pos
				}
				idx--
				if idx >= 0 && idx < n {
					filtered = append(filtered, period[idx])
				}
			}
			period = period[:0]
		}
		for _, c := range selected {
			if len(period) == 0 || inSamePeriod(period[0], c) {
				period = append(period, c)
			} else {
				flush()
				period = append(period, c)
			}
		}
		flush()
		selected = filtered
		sort.Slice(selected, func(i, j int) bool { return selected[i].Before(selected[j]) })
	}

	// Floor: candidates before DTSTART only participated in BYSETPOS
	// position counting within the first period; they are not occurrences.
	var floor []time.Time
	for _, c := range selected {
		if !c.Before(start) {
			floor = append(floor, c)
		}
	}
	selected = floor

	// DTSTART is always occurrence number one (RFC 5545 3.8.5.3), even when
	// the rule pattern does not select it, and it counts toward COUNT.
	dtstartMatches := len(selected) > 0 && selected[0].Equal(start)
	prepend := !dtstartMatches
	if r.Count > 0 {
		generatedCap := r.Count
		if prepend {
			generatedCap = r.Count - 1
		}
		if len(selected) > generatedCap {
			selected = selected[:generatedCap]
		}
	}
	var out []time.Time
	if prepend {
		out = append(out, start)
	}
	out = append(out, selected...)

	if !r.Until.IsZero() {
		var kept []time.Time
		for _, c := range out {
			if r.UntilDateOnly {
				uy, um, ud := r.Until.Date()
				cy, cm, cd := c.Date()
				if cy < uy || (cy == uy && (cm < um || (cm == um && cd <= ud))) {
					kept = append(kept, c)
				}
			} else if !c.UTC().After(r.Until.UTC()) {
				kept = append(kept, c)
			}
		}
		out = kept
	}
	return out
}

func orDefault(v, def []int) []int {
	if v == nil {
		return def
	}
	return v
}

func orContains(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func orContainsDay(xs []Weekday, v Weekday) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// orBydayMatches decides whether day (y,m,d) satisfies BYDAY for the given
// frequency. Ordinal semantics differ: MONTHLY ordinals count within month,
// YEARLY with BYMONTH within each named month, YEARLY without BYMONTH within
// the year; WEEKLY/DAILY ordinals are invalid.
func orBydayMatches(byday []WeekdayNum, y int, m time.Month, d int, day time.Time, loc *time.Location, freq Frequency, byMonth []int) bool {
	wd := weekdayToICS(day.Weekday())
	var plain, ordinal []WeekdayNum
	for _, w := range byday {
		if w.OrdWeek == 0 {
			plain = append(plain, w)
		} else {
			ordinal = append(ordinal, w)
		}
	}
	plainOK := true
	if len(plain) > 0 {
		plainOK = orContainsDay(flattenDays(plain), wd)
	}
	ordinalOK := true
	if len(ordinal) > 0 {
		ordinalOK = false
		for _, w := range ordinal {
			switch {
			case freq == FrequencyMonthly || (freq == FrequencyYearly && len(byMonth) > 0):
				if w.Day == wd && nthWeekdayInMonth(w.OrdWeek, y, m, w.Day, loc) == d {
					ordinalOK = true
				}
			case freq == FrequencyYearly:
				if w.Day == wd && nthWeekdayInYear(w.OrdWeek, y, w.Day, loc) == day.YearDay() {
					ordinalOK = true
				}
			default:
				// Ordinals invalid for this freq: no day matches.
			}
		}
	}
	return plainOK && ordinalOK
}

func flattenDays(ws []WeekdayNum) []Weekday {
	out := make([]Weekday, len(ws))
	for i, w := range ws {
		out[i] = w.Day
	}
	return out
}

func nthWeekdayInMonth(n int, y int, m time.Month, wd Weekday, loc *time.Location) int {
	count := 0
	last := daysInMonth(y, m)
	if n > 0 {
		for d := 1; d <= last; d++ {
			if time.Date(y, m, d, 0, 0, 0, 0, loc).Weekday() == weekdayToGo(wd) {
				count++
				if count == n {
					return d
				}
			}
		}
		return -1
	}
	target := -n
	for d := last; d >= 1; d-- {
		if time.Date(y, m, d, 0, 0, 0, 0, loc).Weekday() == weekdayToGo(wd) {
			count++
			if count == target {
				return d
			}
		}
	}
	return -1
}

func nthWeekdayInYear(n int, y int, wd Weekday, loc *time.Location) int {
	total := daysInYear(y)
	count := 0
	if n > 0 {
		for i := 1; i <= total; i++ {
			t := time.Date(y, 1, 1, 0, 0, 0, 0, loc).AddDate(0, 0, i-1)
			if t.Weekday() == weekdayToGo(wd) {
				count++
				if count == n {
					return i
				}
			}
		}
		return -1
	}
	target := -n
	for i := total; i >= 1; i-- {
		t := time.Date(y, 1, 1, 0, 0, 0, 0, loc).AddDate(0, 0, i-1)
		if t.Weekday() == weekdayToGo(wd) {
			count++
			if count == target {
				return i
			}
		}
	}
	return -1
}

func weekStep(day, start time.Time, interval int, wkst Weekday) bool {
	wk := time.Monday
	if wkst != "" {
		wk = weekdayToGo(wkst)
	}
	weekStartOf := func(t time.Time) time.Time {
		y, m, d := t.Date()
		noon := time.Date(y, m, d, 12, 0, 0, 0, t.Location())
		diff := (int(noon.Weekday()) - int(wk) + 7) % 7
		return noon.AddDate(0, 0, -diff)
	}
	n := (civilDayNumber(weekStartOf(day)) - civilDayNumber(weekStartOf(start))) / 7
	return n >= 0 && n%interval == 0
}

func monthStep(day, start time.Time, interval int) bool {
	n := (day.Year()-start.Year())*12 + int(day.Month()) - int(start.Month())
	return n >= 0 && n%interval == 0
}

func yearStep(day, start time.Time, interval int) bool {
	n := day.Year() - start.Year()
	return n >= 0 && n%interval == 0
}

type weekKey struct{ y, w int }

func isoWeekKey(t time.Time, wkst Weekday) weekKey {
	wk := time.Monday
	if wkst != "" {
		wk = weekdayToGo(wkst)
	}
	y, m, d := t.Date()
	noon := time.Date(y, m, d, 12, 0, 0, 0, t.Location())
	diff := (int(noon.Weekday()) - int(wk) + 7) % 7
	thursday := noon.AddDate(0, 0, -diff+3)
	year := thursday.Year()
	jan1 := time.Date(year, 1, 1, 12, 0, 0, 0, t.Location())
	jDiff := (int(jan1.Weekday()) - int(wk) + 7) % 7
	firstWS := jan1.AddDate(0, 0, -jDiff)
	w := int(thursday.Sub(firstWS).Hours()/24/7) + 1
	return weekKey{year, w}
}

// civilDayNumber is a zone-independent ordinal for a calendar day, so day
// arithmetic never depends on DST hours.
func civilDayNumber(t time.Time) int {
	y, m, d := t.Date()
	return int(time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix() / 86400)
}

// inFreqStep reports whether day is an INTERVAL-day multiple from start.
// It uses a civil (zone-independent) day count so DST transitions cannot shift
// the step.
func inFreqStep(day, start time.Time, interval, _ int) bool {
	days := civilDayNumber(day) - civilDayNumber(start)
	return days >= 0 && days%interval == 0
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}
