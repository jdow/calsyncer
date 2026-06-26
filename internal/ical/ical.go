// Package ical provides types for iCal event groups and HTTP fetching of .ics feeds.
package ical

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	goical "github.com/emersion/go-ical"
)

const (
	// PropSource is the custom iCal property tagging synced events with the
	// name of the source calendar they came from (e.g. "oncall").
	PropSource = "X-CALSYNCER-SRC"

	// PropOriginUID stores the UID from the source calendar, so we can match
	// destination objects back to their source group across runs.
	PropOriginUID = "X-CALSYNCER-ORIGIN-UID"

	// PropOriginHash is a hash of the entire source EventGroup's serialized
	// content. If this matches, the group is unchanged and we skip it.
	PropOriginHash = "X-CALSYNCER-HASH"
)

// EventGroup holds a recurring event's parent VEVENT and its exception VEVENTs
// (those with a RECURRENCE-ID). For non-recurring events, Exceptions is empty.
type EventGroup struct {
	UID        string
	Key        string              // UID for recurring series; UID+DTSTART for one-off events
	Parent     *goical.Component   // parent VEVENT (no RECURRENCE-ID)
	Exceptions []*goical.Component // per-occurrence overrides
}

// Summary returns a human-readable label for logging.
func (g *EventGroup) Summary() string {
	if s := PropValue(g.Parent, goical.PropSummary); s != "" {
		return s
	}
	return "(no summary)"
}

// PropValue extracts a property value from a component by name.
func PropValue(ev *goical.Component, name string) string {
	p := ev.Props.Get(name)
	if p != nil {
		return p.Value
	}
	return ""
}

// HashGroup produces a stable content hash for an EventGroup.
// Changes to RRULE, EXDATE, SUMMARY, DTSTART, DTEND, DESCRIPTION, LOCATION,
// STATUS, or RECURRENCE-ID on any component will produce a different hash.
func HashGroup(g *EventGroup) string {
	var sb strings.Builder

	writeComponentHash(&sb, g.Parent)

	// Sort exceptions by RECURRENCE-ID so the hash is stable regardless of
	// the order the feed delivers them.
	sorted := make([]*goical.Component, len(g.Exceptions))
	copy(sorted, g.Exceptions)
	sort.Slice(sorted, func(i, j int) bool {
		ri := PropValue(sorted[i], goical.PropRecurrenceID)
		rj := PropValue(sorted[j], goical.PropRecurrenceID)
		return ri < rj
	})
	for _, ex := range sorted {
		sb.WriteString("---exception---")
		writeComponentHash(&sb, ex)
	}

	h := sha256.Sum256([]byte(sb.String()))
	return fmt.Sprintf("%x", h[:8])
}

var hashedProps = []string{
	goical.PropSummary,
	goical.PropDateTimeStart,
	goical.PropDateTimeEnd,
	goical.PropDuration,
	goical.PropDescription,
	goical.PropLocation,
	goical.PropRecurrenceRule,
	goical.PropExceptionDates,
	goical.PropRecurrenceDates,
	goical.PropRecurrenceID,
	goical.PropStatus,
}

func writeComponentHash(sb *strings.Builder, ev *goical.Component) {
	for _, name := range hashedProps {
		// A property can appear multiple times (e.g. EXDATE).
		for _, p := range ev.Props[name] {
			sb.WriteString(name)
			sb.WriteByte('=')
			sb.WriteString(p.Value)
			sb.WriteByte(';')
		}
	}
}

// CloneComponent deep-copies a VEVENT component.
func CloneComponent(ev *goical.Component) *goical.Component {
	dst := goical.NewComponent(ev.Name)
	for name, props := range ev.Props {
		dst.Props[name] = append([]goical.Prop(nil), props...)
	}
	return dst
}

// SplitMultiDayGroup splits a timed event that spans multiple calendar days into
// per-day segments. The first and last days become timed events; any days in
// between become all-day events. loc determines where calendar day boundaries fall.
//
// Recurring events (RRULE) and already all-day events (VALUE=DATE) are returned as-is.
func SplitMultiDayGroup(group *EventGroup, loc *time.Location) []*EventGroup {
	// Don't split recurring events.
	if group.Parent.Props.Get(goical.PropRecurrenceRule) != nil {
		return []*EventGroup{group}
	}

	dtStartProp := group.Parent.Props.Get(goical.PropDateTimeStart)
	if dtStartProp == nil {
		return []*EventGroup{group}
	}

	// Don't split all-day (DATE-only) events.
	if dtStartProp.ValueType() == goical.ValueDate {
		return []*EventGroup{group}
	}

	dtStart, dtEnd, err := eventTimeRange(group.Parent)
	if err != nil {
		return []*EventGroup{group}
	}

	if loc == nil {
		loc = time.Local
	}

	startDay := truncateToDay(dtStart, loc)
	endDay := truncateToDay(dtEnd, loc)

	// Only split if there is at least one full calendar day between start and end
	// (i.e. endDay is strictly after the day after startDay). Events that merely
	// cross a single midnight — like Mon 9am → Tue 8am — are left intact: splitting
	// them would produce two timed segments with no all-day middle segment, which is
	// the whole point of this function. It would also produce wrong split points when
	// the server timezone differs from the user's timezone.
	if !endDay.After(startDay.AddDate(0, 0, 1)) {
		return []*EventGroup{group}
	}

	var result []*EventGroup
	idx := 0

	// Synthetic midnight boundaries are written in UTC so that servers like iCloud
	// receive an unambiguous timestamp. TZID-parameterized local times are not
	// reliably handled by all CalDAV servers.
	firstEnd := startDay.AddDate(0, 0, 1).UTC()
	seg0 := timedSegment(group, dtStart, firstEnd, idx)
	result = append(result, seg0)
	idx++

	// Middle segments: all-day events for days strictly between start and end.
	for d := startDay.AddDate(0, 0, 1); d.Before(endDay); d = d.AddDate(0, 0, 1) {
		seg := allDaySegment(group, d, d.AddDate(0, 0, 1), idx)
		result = append(result, seg)
		idx++
	}

	// Last segment: midnight of last day → original DTEND.
	// Only emit if DTEND is NOT exactly at midnight (i.e. not zero-duration last day).
	lastStart := endDay.UTC()
	if !dtEnd.Equal(lastStart) {
		seg := timedSegment(group, lastStart, dtEnd, idx)
		result = append(result, seg)
	}

	return result
}

// eventTimeRange returns the start and end time of a VEVENT.
// If DTEND is absent but DURATION is present, DTEND is computed as DTSTART + DURATION.
func eventTimeRange(ev *goical.Component) (dtStart, dtEnd time.Time, err error) {
	dtStartProp := ev.Props.Get(goical.PropDateTimeStart)
	dtStart, err = dtStartProp.DateTime(time.Local)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("parsing DTSTART: %w", err)
	}

	dtEndProp := ev.Props.Get(goical.PropDateTimeEnd)
	if dtEndProp != nil {
		dtEnd, err = dtEndProp.DateTime(time.Local)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("parsing DTEND: %w", err)
		}
		return dtStart, dtEnd, nil
	}

	durProp := ev.Props.Get(goical.PropDuration)
	if durProp != nil {
		dur, durErr := durProp.Duration()
		if durErr != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("parsing DURATION: %w", durErr)
		}
		return dtStart, dtStart.Add(dur), nil
	}

	// No DTEND or DURATION: treat as zero-duration event.
	return dtStart, dtStart, nil
}

func truncateToDay(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

func timedSegment(orig *EventGroup, start, end time.Time, idx int) *EventGroup {
	parent := CloneComponent(orig.Parent)
	setDateTimeProp(parent, goical.PropDateTimeStart, start)
	setDateTimeProp(parent, goical.PropDateTimeEnd, end)
	return &EventGroup{
		UID:    orig.UID,
		Key:    orig.Key + ":split:" + strconv.Itoa(idx),
		Parent: parent,
	}
}

func allDaySegment(orig *EventGroup, start, end time.Time, idx int) *EventGroup {
	parent := CloneComponent(orig.Parent)
	setDateProp(parent, goical.PropDateTimeStart, start)
	setDateProp(parent, goical.PropDateTimeEnd, end)
	return &EventGroup{
		UID:    orig.UID,
		Key:    orig.Key + ":split:" + strconv.Itoa(idx),
		Parent: parent,
	}
}

func setDateTimeProp(ev *goical.Component, name string, t time.Time) {
	p := goical.NewProp(name)
	p.SetDateTime(t)
	ev.Props[name] = []goical.Prop{*p}
}

func setDateProp(ev *goical.Component, name string, t time.Time) {
	p := goical.NewProp(name)
	p.SetDate(t)
	ev.Props[name] = []goical.Prop{*p}
}
