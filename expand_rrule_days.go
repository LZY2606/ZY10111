package ics

import "time"

// filterDaysByScope walks every day in [from,to] (naive midnights) and keeps
// those matching every present day-level BY-part. scope determines how ordinal
// BYDAY values and the BYWEEKNO/BYYEARDAY parts are interpreted.
func (g *ruleGenerator) filterDaysByScope(from, to time.Time, scope dayScope) []time.Time {
	r := g.rule
	hasDayParts := len(r.ByDay) > 0 || len(r.ByMonthDay) > 0 || len(r.ByYearDay) > 0 || len(r.ByWeekNo) > 0

	var days []time.Time
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		if len(r.ByMonth) > 0 && !containsInt(r.ByMonth, int(d.Month())) {
			continue
		}
		if hasDayParts {
			matched := true
			if len(r.ByMonthDay) > 0 && !g.matchMonthDay(d) {
				matched = false
			}
			if matched && len(r.ByYearDay) > 0 && !g.matchYearDay(d) {
				matched = false
			}
			if matched && len(r.ByWeekNo) > 0 && !g.matchWeekNo(d) {
				matched = false
			}
			if matched && len(r.ByDay) > 0 && !g.matchByDay(d, scope) {
				matched = false
			}
			if !matched {
				continue
			}
		} else {
			// Default day selection per frequency.
			switch g.rule.Freq {
			case FrequencyWeekly:
				if weekdayICS(d.Weekday()) != weekdayICS(g.startWall.Weekday()) {
					continue
				}
			case FrequencyDaily, FrequencyMonthly, FrequencyYearly:
				// Every walked day is a candidate; the period bounds already
				// select the right day for DAILY/MONTHLY/YEARLY starts.
			}
		}
		days = append(days, time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC))
	}

	// FREQ=MONTHLY with no day parts must keep exactly the start day-of-month
	// (calendar arithmetic already places the period start there, but walking a
	// whole month would otherwise emit every day).
	if g.rule.Freq == FrequencyMonthly && !hasDayParts && len(r.ByMonth) == 0 {
		want := g.startWall.Day()
		filtered := days[:0]
		for _, d := range days {
			if d.Day() == want {
				filtered = append(filtered, d)
			}
		}
		days = filtered
	}
	// FREQ=YEARLY with no day parts keeps the start month/day.
	if g.rule.Freq == FrequencyYearly && !hasDayParts && len(r.ByMonth) == 0 {
		filtered := days[:0]
		for _, d := range days {
			if int(d.Month()) == int(g.startWall.Month()) && d.Day() == g.startWall.Day() {
				filtered = append(filtered, d)
			}
		}
		days = filtered
	}
	return days
}

func containsInt(xs []int, n int) bool {
	for _, x := range xs {
		if x == n {
			return true
		}
	}
	return false
}

// matchMonthDay handles positive and negative BYMONTHDAY values (e.g. -1 = last
// day of month). Values that do not exist in short months (30, 31, -30, -31)
// simply do not match that month.
func (g *ruleGenerator) matchMonthDay(d time.Time) bool {
	last := time.Date(d.Year(), d.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
	for _, md := range g.rule.ByMonthDay {
		if md > 0 && md == d.Day() {
			return true
		}
		if md < 0 && last+md+1 == d.Day() {
			return true
		}
	}
	return false
}

// matchYearDay handles positive and negative BYYEARDAY values.
func (g *ruleGenerator) matchYearDay(d time.Time) bool {
	yearDays := time.Date(d.Year(), time.December, 31, 0, 0, 0, 0, time.UTC).YearDay()
	yd := d.YearDay()
	for _, y := range g.rule.ByYearDay {
		if y > 0 && y == yd {
			return true
		}
		if y < 0 && yearDays+y+1 == yd {
			return true
		}
	}
	return false
}

// matchWeekNo implements BYWEEKNO using ISO-8601 week numbers (WKST is MO for
// ISO weeks, per RFC 5545; negative numbers count from the last week).
func (g *ruleGenerator) matchWeekNo(d time.Time) bool {
	year, week := d.ISOWeek()
	_, lastWeek := time.Date(year, time.December, 28, 0, 0, 0, 0, time.UTC).ISOWeek()
	for _, w := range g.rule.ByWeekNo {
		if w > 0 && w == week {
			return true
		}
		if w < 0 && lastWeek+w+1 == week {
			return true
		}
	}
	return false
}

// matchByDay evaluates BYDAY entries. Entries without an ordinal match the
// weekday anywhere in scope. Ordinal entries (e.g. 2MO, -1SU) are counted
// within the scope (month or year; ignored for WEEKLY/DAILY where RFC defines
// them as non-applicable).
func (g *ruleGenerator) matchByDay(d time.Time, scope dayScope) bool {
	wd := weekdayICS(d.Weekday())
	for _, wdn := range g.rule.ByDay {
		if wdn.Day != wd {
			continue
		}
		if wdn.OrdWeek == 0 {
			return true
		}
		switch scope {
		case scopeMonth:
			if wdn.OrdWeek > 0 && ordinalInMonth(d, wdn.Day) == wdn.OrdWeek {
				return true
			}
			if wdn.OrdWeek < 0 && negativeOrdinalInMonth(d, wdn.Day) == wdn.OrdWeek {
				return true
			}
		case scopeYear:
			if wdn.OrdWeek > 0 && ordinalInYear(d, wdn.Day) == wdn.OrdWeek {
				return true
			}
			if wdn.OrdWeek < 0 && negativeOrdinalInYear(d, wdn.Day) == wdn.OrdWeek {
				return true
			}
		default:
			// Ordinal BYDAY is not meaningful within DAILY/WEEKLY scope; treat
			// it as a plain weekday match.
			return true
		}
	}
	return false
}

func ordinalInMonth(d time.Time, wd Weekday) int {
	// Positive ordinal: count of wd from the 1st up to and including d.
	count := 0
	for day := 1; day <= d.Day(); day++ {
		t := time.Date(d.Year(), d.Month(), day, 0, 0, 0, 0, time.UTC)
		if weekdayICS(t.Weekday()) == wd {
			count++
		}
	}
	return count
}

// negativeOrdinalInMonth returns the negative ordinal (e.g. -1) of d's weekday
// within its month.
func negativeOrdinalInMonth(d time.Time, wd Weekday) int {
	last := time.Date(d.Year(), d.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
	count := 0
	for day := last; day >= d.Day(); day-- {
		t := time.Date(d.Year(), d.Month(), day, 0, 0, 0, 0, time.UTC)
		if weekdayICS(t.Weekday()) == wd {
			count--
		}
	}
	return count
}

func ordinalInYear(d time.Time, wd Weekday) int {
	count := 0
	for day := time.Date(d.Year(), time.January, 1, 0, 0, 0, 0, time.UTC); day.Year() == d.Year(); day = day.AddDate(0, 0, 1) {
		if weekdayICS(day.Weekday()) == wd {
			count++
			if day.Month() == d.Month() && day.Day() == d.Day() {
				return count
			}
		}
	}
	return 0
}

func negativeOrdinalInYear(d time.Time, wd Weekday) int {
	last := time.Date(d.Year(), time.December, 31, 0, 0, 0, 0, time.UTC)
	count := 0
	for day := last; !day.Before(d); day = day.AddDate(0, 0, -1) {
		if weekdayICS(day.Weekday()) == wd {
			count--
		}
	}
	return count
}
