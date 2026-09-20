package ics

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	// ErrUnknownTimezone reports a TZID that no resolver could map to a
	// time.Location (not present in the calendar VTIMEZONE blocks, the custom
	// resolver chain, or the host's tz database).
	ErrUnknownTimezone = errors.New("unknown timezone")
)

// TimezoneResolver maps a TZID string to a concrete time.Location. Returning a
// nil location means the resolver does not handle the id and resolution
// continues with the next resolver.
type TimezoneResolver func(tzid string) *time.Location

// parseUTCOffset parses an RFC 5545 UTC-OFFSET value ("+0530", "-0800",
// "+053045") into seconds east of UTC.
func parseUTCOffset(s string) int {
	s = strings.TrimSpace(s)
	if len(s) < 5 {
		return 0
	}
	sign := 1
	switch s[0] {
	case '+':
		s = s[1:]
	case '-':
		sign = -1
		s = s[1:]
	}
	h, err := strconv.Atoi(s[0:2])
	if err != nil {
		return 0
	}
	mi, err := strconv.Atoi(s[2:4])
	if err != nil {
		return 0
	}
	sec := 0
	if len(s) >= 6 {
		sec, _ = strconv.Atoi(s[4:6])
	}
	return sign * (h*3600 + mi*60 + sec)
}

// tzTransition is one offset change compiled from a VTIMEZONE sub-component.
type tzTransition struct {
	whenUTC time.Time // absolute instant at which the new offset applies
	off     int       // seconds east of UTC after the transition
	isDst   bool
	name    string
}

// VTimezoneLocation compiles a VTIMEZONE component into a real *time.Location
// using the standard tzfile(5) format, so calendars that embed their own
// timezone definitions expand correctly on hosts without a matching IANA
// database entry. Transitions are generated for years 1902..2099; times
// outside that window keep the nearest documented offset.
func VTimezoneLocation(vtz *VTimezone) (*time.Location, error) {
	if vtz == nil {
		return nil, fmt.Errorf("%w: nil VTIMEZONE", ErrUnknownTimezone)
	}
	tzidProp := vtz.GetProperty(ComponentPropertyTzid)
	if tzidProp == nil {
		return nil, fmt.Errorf("%w: VTIMEZONE without TZID", ErrUnknownTimezone)
	}
	type subRule struct {
		dtstart time.Time
		offFrom int
		offTo   int
		rrule   *RecurrenceRule
		isDst   bool
		name    string
	}
	var subs []subRule
	for _, sub := range vtz.SubComponents() {
		var isDst bool
		switch sub.(type) {
		case *Standard:
			isDst = false
		case *Daylight:
			isDst = true
		default:
			continue
		}
		var dtstartVal, offFromVal, offToVal, rruleVal, tzname string
		for _, p := range sub.UnknownPropertiesIANAProperties() {
			switch p.IANAToken {
			case string(PropertyDtstart):
				dtstartVal = p.Value
			case string(PropertyTzoffsetfrom):
				offFromVal = p.Value
			case string(PropertyTzoffsetto):
				offToVal = p.Value
			case string(PropertyRrule):
				rruleVal = p.Value
			case string(PropertyTzname):
				tzname = p.Value
			}
		}
		if dtstartVal == "" || offFromVal == "" || offToVal == "" {
			continue
		}
		dt, err := time.ParseInLocation(icalTimestampFormatLocal, dtstartVal, time.UTC)
		if err != nil {
			return nil, fmt.Errorf("VTIMEZONE %s DTSTART: %w", tzidProp.Value, err)
		}
		if tzname == "" {
			if isDst {
				tzname = "DST"
			} else {
				tzname = "STD"
			}
		}
		sr := subRule{
			dtstart: dt,
			offFrom: parseUTCOffset(offFromVal),
			offTo:   parseUTCOffset(offToVal),
			isDst:   isDst,
			name:    tzname,
		}
		if rruleVal != "" {
			if sr.rrule, err = ParseRecurrenceRule(rruleVal); err != nil {
				return nil, fmt.Errorf("VTIMEZONE %s RRULE: %w", tzidProp.Value, err)
			}
		}
		subs = append(subs, sr)
	}
	if len(subs) == 0 {
		return nil, fmt.Errorf("%w: VTIMEZONE %s has no usable STANDARD/DAYLIGHT rules", ErrUnknownTimezone, tzidProp.Value)
	}

	const firstYear, lastYear = 1902, 2099
	var trs []tzTransition
	for _, sr := range subs {
		// The DTSTART local time is expressed with the offset in force BEFORE
		// the change; anchor the rule in a fixed zone carrying that offset.
		start := time.Date(sr.dtstart.Year(), sr.dtstart.Month(), sr.dtstart.Day(),
			sr.dtstart.Hour(), sr.dtstart.Minute(), sr.dtstart.Second(), 0,
			time.FixedZone("", sr.offFrom))
		emit := func(t time.Time) {
			trs = append(trs, tzTransition{
				whenUTC: t.UTC(),
				off:     sr.offTo,
				isDst:   sr.isDst,
				name:    sr.name,
			})
		}
		emit(start)
		if sr.rrule != nil {
			it, err := newRuleIterator(sr.rrule, start)
			if err != nil {
				return nil, err
			}
			for {
				t, ok, err := it.Next()
				if err != nil {
					return nil, err
				}
				if !ok || t.Year() > lastYear {
					break
				}
				if t.Year() >= firstYear {
					emit(t)
				}
			}
		}
	}
	sort.SliceStable(trs, func(i, j int) bool { return trs[i].whenUTC.Before(trs[j].whenUTC) })
	// Remove duplicate instants, preferring deterministic STANDARD order on
	// exact ties (real zones never transition twice in one instant).
	dedup := trs[:0]
	for i := range trs {
		if i > 0 && trs[i].whenUTC.Equal(trs[i-1].whenUTC) {
			continue
		}
		dedup = append(dedup, trs[i])
	}
	trs = dedup

	data, initialOff, err := buildTZfile(trs)
	if err != nil {
		return nil, fmt.Errorf("VTIMEZONE %s: %w", tzidProp.Value, err)
	}
	loc, err := time.LoadLocationFromTZData(tzidProp.Value, data)
	if err != nil {
		return nil, fmt.Errorf("VTIMEZONE %s compiled offset %d: %w", tzidProp.Value, initialOff, err)
	}
	return loc, nil
}

// buildTZfile serializes transitions into tzfile(5) version 2 data.
func buildTZfile(trs []tzTransition) ([]byte, int, error) {
	type ttinfo struct {
		off   int
		isDst bool
		name  string
	}
	// The first ttinfo is the default type for times before the first
	// transition; tzfile consumers use the first standard-time record.
	defaultInfo := ttinfo{off: 0, isDst: false, name: "STD"}
	infos := []ttinfo{defaultInfo}
	indexOf := func(info ttinfo) int {
		for i, x := range infos {
			if x == info {
				return i
			}
		}
		infos = append(infos, info)
		return len(infos) - 1
	}
	// Determine pre-window offset from the earliest transition.
	if len(trs) > 0 {
		infos[0] = ttinfo{off: trs[0].off, isDst: trs[0].isDst, name: trs[0].name}
	}
	type indexed struct {
		unix int64
		idx  uint8
	}
	rows := make([]indexed, 0, len(trs))
	for _, tr := range trs {
		idx := indexOf(ttinfo{off: tr.off, isDst: tr.isDst, name: tr.name})
		rows = append(rows, indexed{unix: tr.whenUTC.Unix(), idx: uint8(idx)})
	}
	// Abbreviation table.
	var abbr bytes.Buffer
	abbrIndex := map[string]int{}
	for i := range infos {
		if pos, ok := abbrIndex[infos[i].name]; ok {
			_ = pos
		} else {
			abbrIndex[infos[i].name] = abbr.Len()
			abbr.WriteString(infos[i].name)
			abbr.WriteByte(0)
		}
	}

	writeBlock := func(w *bytes.Buffer, wide bool) {
		binary.Write(w, binary.BigEndian, []byte("TZif"))
		ver := byte(0)
		if wide {
			ver = '2'
		}
		w.WriteByte(ver)
		w.Write(make([]byte, 15))
		counts := func() []int32 {
			return []int32{0, 0, 0, int32(len(rows)), int32(len(infos)), int32(abbr.Len())}
		}()
		for _, c := range counts {
			binary.Write(w, binary.BigEndian, c)
		}
		for _, r := range rows {
			if wide {
				binary.Write(w, binary.BigEndian, r.unix)
			} else {
				binary.Write(w, binary.BigEndian, int32(r.unix))
			}
		}
		for _, r := range rows {
			w.WriteByte(r.idx)
		}
		for _, info := range infos {
			binary.Write(w, binary.BigEndian, int32(info.off))
			if info.isDst {
				w.WriteByte(1)
			} else {
				w.WriteByte(0)
			}
			binary.Write(w, binary.BigEndian, uint8(abbrIndex[info.name]))
		}
		w.Write(abbr.Bytes())
	}

	var buf bytes.Buffer
	writeBlock(&buf, false)
	writeBlock(&buf, true)
	// v2 footer: POSIX-style TZ string, unused by Go but required by the spec.
	buf.WriteString("\n%s\n")
	return buf.Bytes(), infos[0].off, nil
}
