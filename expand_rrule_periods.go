package ics

import "time"

// enumerateCalendarPeriod enumerates candidate wall clocks for one DAILY,
// WEEKLY, MONTHLY or YEARLY recurrence period.
func (g *ruleGenerator) enumerateCalendarPeriod(idx int) []time.Time {
	var days []time.Time
	switch g.rule.Freq {
	case FrequencyDaily:
		day := addNaive(g.startWall, 0, 0, idx*g.interval())
		days = g.filterDaysByScope(day, day, scopeDay)
	case FrequencyWeekly:
		periodStart := addNaive(g.weekStart(g.startWall), 0, 0, idx*7)
		periodEnd := addNaive(periodStart, 0, 0, 6)
		if idx%g.interval() != 0 {
			break
		}
		days = g.filterDaysByScope(periodStart, periodEnd, scopeWeek)
	case FrequencyMonthly:
		periodStart := addNaive(g.startWall, 0, idx*g.interval(), -g.startWall.Day()+1)
		periodEnd := addNaive(periodStart, 0, 1, -1)
		days = g.filterDaysByScope(periodStart, periodEnd, scopeMonth)
	case FrequencyYearly:
		periodStart := time.Date(g.startWall.Year()+idx*g.interval(), time.January, 1, 0, 0, 0, 0, time.UTC)
		periodEnd := time.Date(periodStart.Year(), time.December, 31, 0, 0, 0, 0, time.UTC)
		days = g.filterDaysByScope(periodStart, periodEnd, scopeYear)
	}

	timesOfDay := g.dayTimes()
	walls := make([]time.Time, 0, len(days)*len(timesOfDay))
	for _, day := range days {
		for _, tod := range timesOfDay {
			walls = append(walls, time.Date(day.Year(), day.Month(), day.Day(), tod.hour, tod.min, tod.sec, 0, time.UTC))
		}
	}
	ps, pe := g.periodRange(idx)
	return g.applyBySetPos(walls, ps, pe)
}

// dayScope determines how broadly day-matching predicates interpret ordinal
// BYDAY and which BY-parts act as limits vs expansions.
type dayScope int

const (
	scopeDay dayScope = iota
	scopeWeek
	scopeMonth
	scopeYear
)

func (g *ruleGenerator) interval() int {
	if g.rule.Interval <= 0 {
		return 1
	}
	return g.rule.Interval
}

// periodStartNaive returns the first naive wall clock of recurrence period idx.
func (g *ruleGenerator) periodStartNaive(idx int) time.Time {
	switch g.rule.Freq {
	case FrequencyDaily:
		return addNaive(g.startWall, 0, 0, idx*g.interval())
	case FrequencyWeekly:
		return addNaive(g.weekStart(g.startWall), 0, 0, idx*7)
	case FrequencyMonthly:
		return addNaive(g.startWall, 0, idx*g.interval(), -g.startWall.Day()+1)
	case FrequencyYearly:
		return time.Date(g.startWall.Year()+idx*g.interval(), time.January, 1, 0, 0, 0, 0, time.UTC)
	case FrequencySecondly, FrequencyMinutely, FrequencyHourly:
		return g.subDailyPeriodBounds(idx)
	}
	return g.startWall
}

// weekStart moves a wall clock back to the configured week-start weekday.
func (g *ruleGenerator) weekStart(t time.Time) time.Time {
	wkst := time.Monday
	if g.rule.Wkst != "" {
		wkst = weekdayGo(g.rule.Wkst)
	}
	diff := (int(t.Weekday()) - int(wkst) + 7) % 7
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -diff)
}

func weekdayGo(w Weekday) time.Weekday {
	switch w {
	case WeekdaySunday:
		return time.Sunday
	case WeekdayMonday:
		return time.Monday
	case WeekdayTuesday:
		return time.Tuesday
	case WeekdayWednesday:
		return time.Wednesday
	case WeekdayThursday:
		return time.Thursday
	case WeekdayFriday:
		return time.Friday
	case WeekdaySaturday:
		return time.Saturday
	}
	return time.Monday
}

func weekdayICS(w time.Weekday) Weekday {
	switch w {
	case time.Sunday:
		return WeekdaySunday
	case time.Monday:
		return WeekdayMonday
	case time.Tuesday:
		return WeekdayTuesday
	case time.Wednesday:
		return WeekdayWednesday
	case time.Thursday:
		return WeekdayThursday
	case time.Friday:
		return WeekdayFriday
	case time.Saturday:
		return WeekdaySaturday
	}
	return WeekdayMonday
}

type hms struct{ hour, min, sec int }

// dayTimes returns the cross product of BYHOUR/BYMINUTE/BYSECOND restricted to
// the frequency. When a part is absent it defaults to DTSTART's value for
// DAILY+ rules (RFC 5545 section 3.3.10).
func (g *ruleGenerator) dayTimes() []hms {
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
	var out []hms
	for _, h := range hours {
		for _, m := range mins {
			for _, sec := range secs {
				out = append(out, hms{h, m, sec})
			}
		}
	}
	return out
}

// periodRange returns the [start,end] naive day bounds of period idx, used for
// BYSETPOS scoping.
func (g *ruleGenerator) periodRange(idx int) (time.Time, time.Time) {
	switch g.rule.Freq {
	case FrequencyDaily:
		day := addNaive(g.startWall, 0, 0, idx*g.interval())
		return day, day
	case FrequencyWeekly:
		start := addNaive(g.weekStart(g.startWall), 0, 0, idx*7)
		return start, addNaive(start, 0, 0, 6)
	case FrequencyMonthly:
		start := addNaive(g.startWall, 0, idx*g.interval(), -g.startWall.Day()+1)
		return start, addNaive(start, 0, 1, -1)
	case FrequencyYearly:
		start := time.Date(g.startWall.Year()+idx*g.interval(), time.January, 1, 0, 0, 0, 0, time.UTC)
		return start, time.Date(start.Year(), time.December, 31, 0, 0, 0, 0, time.UTC)
	}
	return g.startWall, g.startWall
}

// addNaive performs calendar arithmetic on a UTC-located naive wall clock.
func addNaive(t time.Time, years, months, days int) time.Time {
	return t.AddDate(years, months, days)
}

// applyBySetPos selects members of a period candidate set by 1-based position
// (negative counts from the end). walls is assumed ascending.
func (g *ruleGenerator) applyBySetPos(walls []time.Time, _, _ time.Time) []time.Time {
	if len(g.rule.BySetPos) == 0 {
		return walls
	}
	out := make([]time.Time, 0, len(g.rule.BySetPos))
	for _, pos := range g.rule.BySetPos {
		idx := pos
		if idx < 0 {
			idx = len(walls) + idx + 1
		}
		if idx >= 1 && idx <= len(walls) {
			out = append(out, walls[idx-1])
		}
	}
	return out
}
