package caldav

import (
	"testing"

	ical "github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
	calsyncer "github.com/jdow/calsyncer/internal/ical"
)

func buildCalendarObject(props map[string]string) caldav.CalendarObject {
	ev := ical.NewComponent(ical.CompEvent)
	for name, val := range props {
		p := ical.NewProp(name)
		p.Value = val
		ev.Props[name] = []ical.Prop{*p}
	}

	cal := ical.NewCalendar()
	cal.Children = append(cal.Children, ev)

	return caldav.CalendarObject{
		Path: "/calendars/test/event.ics",
		ETag: `"abc123"`,
		Data: cal,
	}
}

func TestExtractSyncerMeta_Owned(t *testing.T) {
	obj := buildCalendarObject(map[string]string{
		calsyncer.PropSource:     "oncall",
		calsyncer.PropOriginUID:  "uid-123:20240115T090000Z",
		calsyncer.PropOriginHash: "deadbeef",
	})

	c := &Client{}
	source, originUID, hash := c.extractSyncerMeta(obj)

	if source != "oncall" {
		t.Errorf("source: got %q, want %q", source, "oncall")
	}
	if originUID != "uid-123:20240115T090000Z" {
		t.Errorf("originUID: got %q, want %q", originUID, "uid-123:20240115T090000Z")
	}
	if hash != "deadbeef" {
		t.Errorf("hash: got %q, want %q", hash, "deadbeef")
	}
}

func TestExtractSyncerMeta_ICloudFallback(t *testing.T) {
	// iCloud remaps X-CALSYNCER-SRC → SOURCE (the standard SOURCE prop)
	obj := buildCalendarObject(map[string]string{
		ical.PropSource:          "work",
		calsyncer.PropOriginUID:  "uid-456:20240201T100000Z",
		calsyncer.PropOriginHash: "cafebabe",
	})

	c := &Client{}
	source, originUID, hash := c.extractSyncerMeta(obj)

	if source != "work" {
		t.Errorf("source: got %q, want %q", source, "work")
	}
	if originUID != "uid-456:20240201T100000Z" {
		t.Errorf("originUID: got %q, want %q", originUID, "uid-456:20240201T100000Z")
	}
	if hash != "cafebabe" {
		t.Errorf("hash: got %q, want %q", hash, "cafebabe")
	}
}

func TestExtractSyncerMeta_SkipsNonVEVENT(t *testing.T) {
	// A VCALENDAR containing only a VTIMEZONE (non-VEVENT) — loop's continue path
	vtz := ical.NewComponent("VTIMEZONE")
	tzID := ical.NewProp("TZID")
	tzID.Value = "America/New_York"
	vtz.Props["TZID"] = []ical.Prop{*tzID}

	cal := ical.NewCalendar()
	cal.Children = append(cal.Children, vtz)
	obj := caldav.CalendarObject{Path: "/test.ics", Data: cal}

	c := &Client{}
	source, originUID, hash := c.extractSyncerMeta(obj)
	if source != "" || originUID != "" || hash != "" {
		t.Errorf("expected all empty for VTIMEZONE-only calendar, got %q %q %q", source, originUID, hash)
	}
}

func TestExtractSyncerMeta_SkipsExceptionVEVENT(t *testing.T) {
	// An exception VEVENT (has RECURRENCE-ID) should be skipped; metadata read from parent only
	ev := ical.NewComponent(ical.CompEvent)
	recID := ical.NewProp(ical.PropRecurrenceID)
	recID.Value = "20240403T090000Z"
	ev.Props[ical.PropRecurrenceID] = []ical.Prop{*recID}
	// Even if it has source props, they should be ignored because it has RECURRENCE-ID
	p := ical.NewProp(calsyncer.PropSource)
	p.Value = "should-be-ignored"
	ev.Props[calsyncer.PropSource] = []ical.Prop{*p}

	cal := ical.NewCalendar()
	cal.Children = append(cal.Children, ev)
	obj := caldav.CalendarObject{Path: "/test.ics", Data: cal}

	c := &Client{}
	source, _, _ := c.extractSyncerMeta(obj)
	if source != "" {
		t.Errorf("expected empty source from exception VEVENT, got %q", source)
	}
}

func TestExtractSyncerMeta_EmptyCalendar(t *testing.T) {
	// VCALENDAR with no VEVENTs — the for loop body is never entered, returns empty
	cal := ical.NewCalendar()
	obj := caldav.CalendarObject{Path: "/test.ics", Data: cal}

	c := &Client{}
	source, originUID, hash := c.extractSyncerMeta(obj)
	if source != "" || originUID != "" || hash != "" {
		t.Errorf("expected all empty for VCALENDAR with no events, got source=%q uid=%q hash=%q", source, originUID, hash)
	}
}

func TestExtractSyncerMeta_NotOwned(t *testing.T) {
	// Object with neither X-CALSYNCER-SRC nor SOURCE — not owned by calsyncer
	obj := buildCalendarObject(map[string]string{
		ical.PropSummary: "Some External Event",
		ical.PropUID:     "external-uid-789",
	})

	c := &Client{}
	source, originUID, hash := c.extractSyncerMeta(obj)

	if source != "" {
		t.Errorf("source: got %q, want empty", source)
	}
	if originUID != "" {
		t.Errorf("originUID: got %q, want empty", originUID)
	}
	if hash != "" {
		t.Errorf("hash: got %q, want empty", hash)
	}
}
