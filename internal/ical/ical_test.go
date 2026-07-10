package ical_test

import (
	"testing"
	"time"

	goical "github.com/emersion/go-ical"
	"github.com/jdow/calsyncer/internal/ical"
)

func makeEvent(uid, summary, dtstart, dtend string) *goical.Component {
	ev := goical.NewComponent(goical.CompEvent)
	ev.Props.SetText(goical.PropUID, uid)
	ev.Props.SetText(goical.PropSummary, summary)

	if dtstart != "" {
		p := goical.NewProp(goical.PropDateTimeStart)
		p.Value = dtstart
		ev.Props[goical.PropDateTimeStart] = []goical.Prop{*p}
	}
	if dtend != "" {
		p := goical.NewProp(goical.PropDateTimeEnd)
		p.Value = dtend
		ev.Props[goical.PropDateTimeEnd] = []goical.Prop{*p}
	}
	return ev
}

func makeGroup(uid, summary, dtstart, dtend string) *ical.EventGroup {
	ev := makeEvent(uid, summary, dtstart, dtend)
	key := uid + ":" + dtstart
	return &ical.EventGroup{UID: uid, Key: key, Parent: ev}
}

func makeDateGroup(uid, summary, dtstart, dtend string) *ical.EventGroup {
	ev := goical.NewComponent(goical.CompEvent)
	ev.Props.SetText(goical.PropUID, uid)
	ev.Props.SetText(goical.PropSummary, summary)

	// DATE-only DTSTART
	p := goical.NewProp(goical.PropDateTimeStart)
	p.SetDate(mustParseDate(dtstart))
	ev.Props[goical.PropDateTimeStart] = []goical.Prop{*p}

	pe := goical.NewProp(goical.PropDateTimeEnd)
	pe.SetDate(mustParseDate(dtend))
	ev.Props[goical.PropDateTimeEnd] = []goical.Prop{*pe}

	key := uid + ":" + dtstart
	return &ical.EventGroup{UID: uid, Key: key, Parent: ev}
}

func mustParseDate(s string) time.Time {
	t, err := time.ParseInLocation("20060102", s, time.UTC)
	if err != nil {
		panic("bad date: " + s)
	}
	return t
}

func makeTimedGroup(uid, summary string, dtstart, dtend time.Time) *ical.EventGroup {
	ev := goical.NewComponent(goical.CompEvent)
	ev.Props.SetText(goical.PropUID, uid)
	ev.Props.SetText(goical.PropSummary, summary)

	ps := goical.NewProp(goical.PropDateTimeStart)
	ps.SetDateTime(dtstart)
	ev.Props[goical.PropDateTimeStart] = []goical.Prop{*ps}

	pe := goical.NewProp(goical.PropDateTimeEnd)
	pe.SetDateTime(dtend)
	ev.Props[goical.PropDateTimeEnd] = []goical.Prop{*pe}

	key := uid + ":" + dtstart.Format("20060102T150405Z")
	return &ical.EventGroup{UID: uid, Key: key, Parent: ev}
}

func TestSummary_WithSummary(t *testing.T) {
	g := makeGroup("uid1", "My Event", "20240115T090000Z", "20240115T100000Z")
	if got := g.Summary(); got != "My Event" {
		t.Errorf("got %q, want %q", got, "My Event")
	}
}

func TestSummary_NoSummary(t *testing.T) {
	ev := goical.NewComponent(goical.CompEvent)
	g := &ical.EventGroup{UID: "uid1", Key: "uid1", Parent: ev}
	if got := g.Summary(); got != "(no summary)" {
		t.Errorf("got %q, want \"(no summary)\"", got)
	}
}

func makeException(uid, summary, dtstart, recurrenceID string) *goical.Component {
	ev := makeEvent(uid, summary, dtstart, "")
	rp := goical.NewProp(goical.PropRecurrenceID)
	rp.Value = recurrenceID
	ev.Props[goical.PropRecurrenceID] = []goical.Prop{*rp}
	return ev
}

func TestHashGroup_WithExceptions(t *testing.T) {
	g := makeGroup("uid1", "Recurring", "20240115T090000Z", "20240115T100000Z")
	ex1 := makeException("uid1", "Exception A", "20240116T090000Z", "20240116T090000Z")
	ex2 := makeException("uid1", "Exception B", "20240117T090000Z", "20240117T090000Z")
	g.Exceptions = []*goical.Component{ex1, ex2}

	h1 := ical.HashGroup(g)

	// Reverse exception order — hash must be stable (exceptions are sorted by RECURRENCE-ID)
	g2 := makeGroup("uid1", "Recurring", "20240115T090000Z", "20240115T100000Z")
	g2.Exceptions = []*goical.Component{ex2, ex1}
	h2 := ical.HashGroup(g2)

	if h1 != h2 {
		t.Errorf("hash should be stable regardless of exception order: %q vs %q", h1, h2)
	}

	// Change an exception — hash must differ
	ex1mod := makeException("uid1", "Exception A MODIFIED", "20240116T090000Z", "20240116T090000Z")
	g3 := makeGroup("uid1", "Recurring", "20240115T090000Z", "20240115T100000Z")
	g3.Exceptions = []*goical.Component{ex1mod, ex2}
	h3 := ical.HashGroup(g3)
	if h1 == h3 {
		t.Error("hash should change when exception is modified")
	}
}

func TestSplitMultiDayGroup_NoDTSTART(t *testing.T) {
	// Event with no DTSTART at all — should return original group unchanged
	ev := goical.NewComponent(goical.CompEvent)
	ev.Props.SetText(goical.PropUID, "uid-nodtstart")
	ev.Props.SetText(goical.PropSummary, "No DTSTART")
	g := &ical.EventGroup{UID: "uid-nodtstart", Key: "uid-nodtstart", Parent: ev}

	result := ical.SplitMultiDayGroup(g, time.Local)
	if len(result) != 1 {
		t.Fatalf("expected 1 group for event with no DTSTART, got %d", len(result))
	}
	if result[0] != g {
		t.Error("expected original group returned unchanged")
	}
}

func TestSplitMultiDayGroup_UnparsableDTSTART(t *testing.T) {
	// Event with an unparseable DTSTART value — eventTimeRange fails, return original
	ev := goical.NewComponent(goical.CompEvent)
	ev.Props.SetText(goical.PropUID, "uid-badstart")
	p := goical.NewProp(goical.PropDateTimeStart)
	p.Value = "NOTADATE"
	ev.Props[goical.PropDateTimeStart] = []goical.Prop{*p}

	g := &ical.EventGroup{UID: "uid-badstart", Key: "uid-badstart", Parent: ev}
	result := ical.SplitMultiDayGroup(g, time.Local)
	if len(result) != 1 || result[0] != g {
		t.Error("expected original group for unparseable DTSTART")
	}
}

func TestSplitMultiDayGroup_DurationProp(t *testing.T) {
	// Event with DURATION spanning only adjacent local days (22:00 → 02:00 next day)
	// should NOT be split — no all-day middle segment exists.
	start := time.Date(2024, 1, 15, 22, 0, 0, 0, time.Local)
	ev := goical.NewComponent(goical.CompEvent)
	ev.Props.SetText(goical.PropUID, "uid-dur")
	ev.Props.SetText(goical.PropSummary, "Duration Event")
	ps := goical.NewProp(goical.PropDateTimeStart)
	ps.SetDateTime(start)
	ev.Props[goical.PropDateTimeStart] = []goical.Prop{*ps}
	dur := goical.NewProp(goical.PropDuration)
	dur.Value = "PT4H"
	ev.Props[goical.PropDuration] = []goical.Prop{*dur}

	key := "uid-dur:" + start.Format("20060102T150405Z")
	g := &ical.EventGroup{UID: "uid-dur", Key: key, Parent: ev}

	result := ical.SplitMultiDayGroup(g, time.Local)
	if len(result) != 1 {
		t.Fatalf("expected 1 segment for adjacent-day duration event, got %d", len(result))
	}
	if result[0] != g {
		t.Error("expected original group returned unchanged")
	}
}

func TestSplitMultiDayGroup_UnparsableDTEND(t *testing.T) {
	// Event where DTEND is present but unparseable — eventTimeRange error → return original
	start := time.Date(2024, 1, 15, 9, 0, 0, 0, time.UTC)
	ev := goical.NewComponent(goical.CompEvent)
	ev.Props.SetText(goical.PropUID, "uid-badend")
	ps := goical.NewProp(goical.PropDateTimeStart)
	ps.SetDateTime(start)
	ev.Props[goical.PropDateTimeStart] = []goical.Prop{*ps}
	pe := goical.NewProp(goical.PropDateTimeEnd)
	pe.Value = "NOTADATE"
	ev.Props[goical.PropDateTimeEnd] = []goical.Prop{*pe}

	g := &ical.EventGroup{UID: "uid-badend", Key: "uid-badend", Parent: ev}
	result := ical.SplitMultiDayGroup(g, time.Local)
	if len(result) != 1 || result[0] != g {
		t.Error("expected original group for unparseable DTEND")
	}
}

func TestSplitMultiDayGroup_UnparsableDuration(t *testing.T) {
	// Event where DURATION is present but unparseable — eventTimeRange error → return original
	start := time.Date(2024, 1, 15, 9, 0, 0, 0, time.UTC)
	ev := goical.NewComponent(goical.CompEvent)
	ev.Props.SetText(goical.PropUID, "uid-baddur")
	ps := goical.NewProp(goical.PropDateTimeStart)
	ps.SetDateTime(start)
	ev.Props[goical.PropDateTimeStart] = []goical.Prop{*ps}
	dur := goical.NewProp(goical.PropDuration)
	dur.Value = "NOTADURATION"
	ev.Props[goical.PropDuration] = []goical.Prop{*dur}

	g := &ical.EventGroup{UID: "uid-baddur", Key: "uid-baddur", Parent: ev}
	result := ical.SplitMultiDayGroup(g, time.Local)
	if len(result) != 1 || result[0] != g {
		t.Error("expected original group for unparseable DURATION")
	}
}

func TestSplitMultiDayGroup_NoDTEND(t *testing.T) {
	// Event with no DTEND and no DURATION: treated as zero-duration, no split
	start := time.Date(2024, 1, 15, 9, 0, 0, 0, time.UTC)
	ev := goical.NewComponent(goical.CompEvent)
	ev.Props.SetText(goical.PropUID, "uid-nodtend")
	ev.Props.SetText(goical.PropSummary, "No DTEND")
	ps := goical.NewProp(goical.PropDateTimeStart)
	ps.SetDateTime(start)
	ev.Props[goical.PropDateTimeStart] = []goical.Prop{*ps}

	key := "uid-nodtend:" + start.Format("20060102T150405Z")
	g := &ical.EventGroup{UID: "uid-nodtend", Key: key, Parent: ev}

	result := ical.SplitMultiDayGroup(g, time.Local)
	if len(result) != 1 {
		t.Fatalf("expected 1 group for zero-duration event, got %d", len(result))
	}
	if result[0] != g {
		t.Error("expected original group returned unchanged")
	}
}

func TestPropValue(t *testing.T) {
	ev := goical.NewComponent(goical.CompEvent)
	ev.Props.SetText(goical.PropSummary, "Hello World")

	got := ical.PropValue(ev, goical.PropSummary)
	if got != "Hello World" {
		t.Errorf("expected 'Hello World', got %q", got)
	}

	missing := ical.PropValue(ev, goical.PropDescription)
	if missing != "" {
		t.Errorf("expected empty string for missing prop, got %q", missing)
	}
}

func TestHashGroup_Stable(t *testing.T) {
	g := makeGroup("uid1", "Test Event", "20240115T090000Z", "20240115T100000Z")
	h1 := ical.HashGroup(g)
	h2 := ical.HashGroup(g)
	if h1 != h2 {
		t.Errorf("hash not stable: %q != %q", h1, h2)
	}
}

func TestHashGroup_ContentChange(t *testing.T) {
	g1 := makeGroup("uid1", "Original Summary", "20240115T090000Z", "20240115T100000Z")
	g2 := makeGroup("uid1", "Changed Summary", "20240115T090000Z", "20240115T100000Z")

	h1 := ical.HashGroup(g1)
	h2 := ical.HashGroup(g2)
	if h1 == h2 {
		t.Errorf("expected different hashes after summary change, got same: %q", h1)
	}
}

func TestSplitMultiDayGroup_SingleDay(t *testing.T) {
	// Same local day: no split expected.
	start := time.Date(2024, 1, 15, 9, 0, 0, 0, time.Local)
	end := time.Date(2024, 1, 15, 17, 0, 0, 0, time.Local)
	g := makeTimedGroup("uid1", "Single Day", start, end)

	result := ical.SplitMultiDayGroup(g, time.Local)
	if len(result) != 1 {
		t.Errorf("expected 1 group, got %d", len(result))
	}
	if result[0] != g {
		t.Error("expected original group returned unchanged")
	}
}

func TestSplitMultiDayGroup_TwoDay(t *testing.T) {
	// Mon 09:00 → Tue 17:00 local — spans adjacent days only, no all-day middle
	// segment would ever exist, so it should NOT be split.
	start := time.Date(2024, 1, 15, 9, 0, 0, 0, time.Local)
	end := time.Date(2024, 1, 16, 17, 0, 0, 0, time.Local)
	g := makeTimedGroup("uid1", "Two Day", start, end)

	result := ical.SplitMultiDayGroup(g, time.Local)
	if len(result) != 1 {
		t.Fatalf("expected 1 group (adjacent-day span should not be split), got %d", len(result))
	}
	if result[0] != g {
		t.Error("expected original group returned unchanged")
	}
}

func TestSplitMultiDayGroup_ThreeDay(t *testing.T) {
	// Mon 09:00 → Wed 17:00 local — timed + allday + timed.
	start := time.Date(2024, 1, 15, 9, 0, 0, 0, time.Local)
	end := time.Date(2024, 1, 17, 17, 0, 0, 0, time.Local)
	g := makeTimedGroup("uid1", "Three Day", start, end)

	result := ical.SplitMultiDayGroup(g, time.Local)
	if len(result) != 3 {
		t.Fatalf("expected 3 groups, got %d", len(result))
	}

	// First: Mon 09:00 local → Tue 00:00 UTC
	checkTimedSegment(t, result[0], start, time.Date(2024, 1, 16, 0, 0, 0, 0, time.Local).UTC(), "uid1:20240115T090000Z:split:0")

	// Middle: all-day Tue
	seg1 := result[1]
	if seg1.Key != "uid1:20240115T090000Z:split:1" {
		t.Errorf("middle segment key: got %q", seg1.Key)
	}
	dtsProp := seg1.Parent.Props.Get(goical.PropDateTimeStart)
	if dtsProp == nil {
		t.Fatal("middle segment has no DTSTART")
	}
	if dtsProp.ValueType() != goical.ValueDate {
		t.Errorf("middle segment DTSTART should be DATE, got %q", dtsProp.ValueType())
	}

	// Last: Wed 00:00 UTC → Wed 17:00 local
	checkTimedSegment(t, result[2], time.Date(2024, 1, 17, 0, 0, 0, 0, time.Local).UTC(), end, "uid1:20240115T090000Z:split:2")
}

func TestSplitMultiDayGroup_Recurring(t *testing.T) {
	start := time.Date(2024, 1, 15, 9, 0, 0, 0, time.Local)
	end := time.Date(2024, 1, 17, 17, 0, 0, 0, time.Local)
	g := makeTimedGroup("uid1", "Recurring Multi-Day", start, end)

	// Add RRULE to make it recurring
	rrule := goical.NewProp(goical.PropRecurrenceRule)
	rrule.Value = "FREQ=WEEKLY"
	g.Parent.Props[goical.PropRecurrenceRule] = []goical.Prop{*rrule}

	result := ical.SplitMultiDayGroup(g, time.Local)
	if len(result) != 1 {
		t.Errorf("recurring event should not be split, got %d groups", len(result))
	}
	if result[0] != g {
		t.Error("expected original group returned unchanged")
	}
}

func TestSplitMultiDayGroup_AllDayInput(t *testing.T) {
	// DATE-only DTSTART should not be split
	g := makeDateGroup("uid1", "All Day", "20240115", "20240117")

	result := ical.SplitMultiDayGroup(g, time.Local)
	if len(result) != 1 {
		t.Errorf("all-day event should not be split, got %d groups", len(result))
	}
	if result[0] != g {
		t.Error("expected original group returned unchanged")
	}
}

func TestIsAllDayBusy(t *testing.T) {
	// Google's public/basic.ics export never emits VALUE=DATE — a multi-day
	// OOO/busy placeholder shows up as a plain DATE-TIME, and its edges
	// often aren't even midnight-aligned (e.g. Aug 17 08:00 -> Aug 21 17:00
	// Pacific for a work OOO block). Any "Busy" event spanning at least one
	// full calendar day in between must be recognized and dropped whole,
	// not just the all-day middle days SplitMultiDayGroup would carve out.
	midnightToMidnight := makeTimedGroup("uid1", "Busy",
		time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC),
		time.Date(2024, 1, 22, 0, 0, 0, 0, time.UTC))
	offsetEdges := makeTimedGroup("uid1", "Busy",
		time.Date(2024, 1, 15, 8, 0, 0, 0, time.UTC),
		time.Date(2024, 1, 19, 17, 0, 0, 0, time.UTC))
	adjacentDayOnly := makeTimedGroup("uid1", "Busy",
		time.Date(2024, 1, 15, 20, 0, 0, 0, time.UTC),
		time.Date(2024, 1, 16, 8, 0, 0, 0, time.UTC))
	recurringMultiDay := makeTimedGroup("uid1", "Busy",
		time.Date(2024, 1, 15, 8, 0, 0, 0, time.UTC),
		time.Date(2024, 1, 19, 17, 0, 0, 0, time.UTC))
	recurringMultiDay.Parent.Props.SetText(goical.PropRecurrenceRule, "FREQ=WEEKLY")

	cases := []struct {
		name string
		g    *ical.EventGroup
		want bool
	}{
		{"all-day Busy", makeDateGroup("uid1", "Busy", "20240115", "20240116"), true},
		{"all-day other summary", makeDateGroup("uid1", "Vacation", "20240115", "20240116"), false},
		{"timed Busy, same day", makeGroup("uid1", "Busy", "20240115T090000Z", "20240115T100000Z"), false},
		{"all-day lowercase busy", makeDateGroup("uid1", "busy", "20240115", "20240116"), false},
		{"midnight-to-midnight Busy (Google OOO export)", midnightToMidnight, true},
		{"offset-edge multi-day Busy (real OOO shape)", offsetEdges, true},
		{"adjacent-day-only Busy (no full day between)", adjacentDayOnly, false},
		{"recurring multi-day Busy (never split, so not swallowed)", recurringMultiDay, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ical.IsAllDayBusy(tc.g, time.UTC); got != tc.want {
				t.Errorf("IsAllDayBusy() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSplitMultiDayGroup_OncallAdjacentDay(t *testing.T) {
	// Regression: Mon 09:00 → Tue 08:00 local (oncall-style handoff). Crosses midnight
	// but never spans a full calendar day, so it must NOT be split.
	start := time.Date(2024, 1, 15, 9, 0, 0, 0, time.Local)
	end := time.Date(2024, 1, 16, 8, 0, 0, 0, time.Local)
	g := makeTimedGroup("uid1", "Oncall", start, end)

	result := ical.SplitMultiDayGroup(g, time.Local)
	if len(result) != 1 {
		t.Fatalf("expected 1 group for adjacent-day oncall event, got %d", len(result))
	}
	if result[0] != g {
		t.Error("expected original group returned unchanged")
	}
}

func TestSplitMultiDayGroup_MidnightEnd(t *testing.T) {
	// Mon 09:00 → Tue 00:00 local: adjacent days only, no split needed.
	start := time.Date(2024, 1, 15, 9, 0, 0, 0, time.Local)
	end := time.Date(2024, 1, 16, 0, 0, 0, 0, time.Local)
	g := makeTimedGroup("uid1", "Midnight End", start, end)

	result := ical.SplitMultiDayGroup(g, time.Local)
	if len(result) != 1 {
		t.Fatalf("expected 1 group (adjacent-day span should not be split), got %d", len(result))
	}
	if result[0] != g {
		t.Error("expected original group returned unchanged")
	}
}

func TestSplitMultiDayGroup_FloatingTimeUsesConfiguredLoc(t *testing.T) {
	// DTSTART/DTEND with no TZID and no Z suffix (a "floating" local time) must
	// be interpreted in the loc passed by the caller (the user's configured
	// timezone), not in some other default — otherwise day-boundary math and
	// segment instants come out wrong whenever the server's OS timezone
	// (commonly UTC in a Docker container) doesn't match the user's calendar.
	// Asia/Kolkata (fixed +5:30, no DST) is chosen so this fails regardless of
	// the machine running the test: it isn't UTC, and it isn't likely to be
	// whatever the test runner's own time.Local happens to be either.
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatalf("loading location: %v", err)
	}

	// 11pm Jan 15 -> 5pm Jan 19, floating (no TZID/Z on the raw value).
	g := makeGroup("uid1", "OOO", "20240115T230000", "20240119T170000")

	result := ical.SplitMultiDayGroup(g, loc)
	if len(result) != 5 {
		t.Fatalf("expected 5 groups (timed + 3 all-day + timed), got %d", len(result))
	}

	checkTimedSegment(t, result[0],
		time.Date(2024, 1, 15, 17, 30, 0, 0, time.UTC),
		time.Date(2024, 1, 15, 18, 30, 0, 0, time.UTC),
		"uid1:20240115T230000:split:0")

	checkTimedSegment(t, result[4],
		time.Date(2024, 1, 18, 18, 30, 0, 0, time.UTC),
		time.Date(2024, 1, 19, 11, 30, 0, 0, time.UTC),
		"uid1:20240115T230000:split:4")
}

func checkTimedSegment(t *testing.T, seg *ical.EventGroup, wantStart, wantEnd time.Time, wantKey string) {
	t.Helper()
	if seg.Key != wantKey {
		t.Errorf("key: got %q, want %q", seg.Key, wantKey)
	}
	dtsProp := seg.Parent.Props.Get(goical.PropDateTimeStart)
	if dtsProp == nil {
		t.Fatal("segment has no DTSTART")
	}
	gotStart, err := dtsProp.DateTime(time.Local)
	if err != nil {
		t.Fatalf("parsing segment DTSTART: %v", err)
	}
	if !gotStart.Equal(wantStart) {
		t.Errorf("DTSTART: got %v, want %v", gotStart, wantStart)
	}

	dteProp := seg.Parent.Props.Get(goical.PropDateTimeEnd)
	if dteProp == nil {
		t.Fatal("segment has no DTEND")
	}
	gotEnd, err := dteProp.DateTime(time.Local)
	if err != nil {
		t.Fatalf("parsing segment DTEND: %v", err)
	}
	if !gotEnd.Equal(wantEnd) {
		t.Errorf("DTEND: got %v, want %v", gotEnd, wantEnd)
	}
}
