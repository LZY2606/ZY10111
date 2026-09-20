package ics

import "time"

// Sub-daily rules are iterated one calendar day at a time. Without expanding
// BY-parts the day is filled from the FREQ/INTERVAL grid anchored at DTSTART;
// with BYHOUR/BYMINUTE/BYSECOND it is their cross product, gated by the same
// grid. fillNextPeriod counts emitted occurrences identically in both cases.

// isSubDaily reports whether the rule frequency is sub-daily.
func (g *ruleGenerator) isSubDaily() bool {
	switch g.rule.Freq {
	case FrequencySecondly, FrequencyMinutely, FrequencyHourly:
		return true
	}
	return false
}

// enumerateSubDailyDay enumerates one calendar day for a sub-daily rule.
// dayIdx counts days from DTSTART's day. Without expanding BY-parts the day is
// filled from the INTERVAL grid anchored at DTSTART; with expanding BY-parts
// the day is the BYHOUR x BYMINUTE x BYSECOND cross product gated by the grid.
func (g *ruleGenerator) enumerateSubDailyDay(dayIdx int) []time.Time {
	s := g.startWall
	day := time.Date(s.Year(), s.Month(), s.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, dayIdx)
	y, m, d := day.Date()

	var walls []time.Time
	if !g.subDailyExpanded() {
		step := g.subDailyStep()
		intv := g.interval()
		dayEnd := day.Add(24*time.Hour - time.Second)
		for n := 0; ; n++ {
			t := s.Add(time.Duration(n*intv) * step)
			if t.After(dayEnd) {
				break
			}
			if t.Day() != d || int(t.Month()) != int(m) || t.Year() != y {
				continue
			}
			if g.subDailyFiltersPass(t) {
				walls = append(walls, t)
			}
		}
	} else {
		for _, t := range g.subDailyCandidates(y, m, d) {
			if g.onIntervalGrid(t) && g.subDailyFiltersPass(t) {
				walls = append(walls, t)
			}
		}
	}

	// Day-level BY-parts apply as well.
	if len(g.rule.ByDay) > 0 || len(g.rule.ByMonthDay) > 0 || len(g.rule.ByMonth) > 0 ||
		len(g.rule.ByYearDay) > 0 || len(g.rule.ByWeekNo) > 0 {
		dayStart := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
		if len(g.filterDaysByScope(dayStart, dayStart, scopeDay)) == 0 {
			return nil
		}
	}
	return g.applyBySetPos(walls, dayStartNaive(y, m, d), dayStartNaive(y, m, d).Add(24*time.Hour-time.Second))
}

func dayStartNaive(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func (g *ruleGenerator) subDailyStep() time.Duration {
	switch g.rule.Freq {
	case FrequencySecondly:
		return time.Second
	case FrequencyMinutely:
		return time.Minute
	default:
		return time.Hour
	}
}

// subDailyPeriodBounds returns the naive first wall clock of "period idx".
// For expanded sub-daily rules a period is a day; for grid rules a grid tick.
func (g *ruleGenerator) subDailyPeriodBounds(idx int) time.Time {
	// fillNextPeriod iterates one day at a time for every sub-daily rule.
	return dayStartNaive(g.startWall.Year(), g.startWall.Month(), g.startWall.Day()).AddDate(0, 0, idx)
}

// subDailyFiltersPass checks the SECOND/MINUTE/HOUR parts for a candidate on the
// interval grid. The grid itself already encodes the frequency, so absent parts
// impose no restriction.
func (g *ruleGenerator) subDailyFiltersPass(t time.Time) bool {
	if len(g.rule.BySecond) > 0 && !containsInt(g.rule.BySecond, t.Second()) {
		return false
	}
	if len(g.rule.ByMinute) > 0 && !containsInt(g.rule.ByMinute, t.Minute()) {
		return false
	}
	if len(g.rule.ByHour) > 0 && !containsInt(g.rule.ByHour, t.Hour()) {
		return false
	}
	return true
}

// subDailyExpanded reports whether any sub-daily BY-part is present.
func (g *ruleGenerator) subDailyExpanded() bool {
	return len(g.rule.BySecond) > 0 || len(g.rule.ByMinute) > 0 || len(g.rule.ByHour) > 0
}

// subDailyCandidates enumerates the BYHOUR x BYMINUTE x BYSECOND cross product
// for the given day. Missing parts default to the DTSTART component, per RFC
// 5545 section 3.3.10.
func (g *ruleGenerator) subDailyCandidates(year int, month time.Month, day int) []time.Time {
	s := g.startWall
	hours := g.rule.ByHour
	if hours == nil {
		hours = []int{s.Hour()}
	}
	mins := g.rule.ByMinute
	if mins == nil {
		mins = []int{s.Minute()}
	}
	secs := g.rule.BySecond
	if secs == nil {
		secs = []int{s.Second()}
	}
	var out []time.Time
	for _, h := range hours {
		for _, mi := range mins {
			for _, se := range secs {
				out = append(out, time.Date(year, month, day, h, mi, se, 0, time.UTC))
			}
		}
	}
	return out
}

// onIntervalGrid reports whether a candidate tick belongs to the FREQ/INTERVAL
// grid anchored at DTSTART.
func (g *ruleGenerator) onIntervalGrid(t time.Time) bool {
	s := g.startWall
	intv := g.interval()
	switch g.rule.Freq {
	case FrequencySecondly:
		return int(t.Sub(s)/time.Second)%intv == 0
	case FrequencyMinutely:
		return int(t.Sub(s)/time.Minute)%intv == 0
	case FrequencyHourly:
		return int(t.Sub(s)/time.Hour)%intv == 0
	}
	return false
}

// subDailyPeriodJump returns the first period index that may reach the window
// start for a no-COUNT sub-daily rule.
func (g *ruleGenerator) subDailyPeriodJump() (int, bool) {
	if !g.isSubDaily() {
		return 0, false
	}
	s := g.startWall
	fromWall := g.cfg.window.From.In(g.loc)
	fromNaive := time.Date(fromWall.Year(), fromWall.Month(), fromWall.Day(),
		fromWall.Hour(), fromWall.Minute(), fromWall.Second(), 0, time.UTC)
	days := int(dayStartNaive(fromNaive.Year(), fromNaive.Month(), fromNaive.Day()).
		Sub(dayStartNaive(s.Year(), s.Month(), s.Day())).Hours() / 24)
	return days - 1, true
}
