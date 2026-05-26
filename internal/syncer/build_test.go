package syncer_test

import (
	"testing"
	"time"

	goical "github.com/emersion/go-ical"
	"github.com/jdow/calsyncer/internal/caldav"
	"github.com/jdow/calsyncer/internal/config"
	"github.com/jdow/calsyncer/internal/ical"
	"github.com/jdow/calsyncer/internal/syncer"
)

func TestBuildDestObject_NewPath(t *testing.T) {
	g := makeGroup("uid1", "Test Event", "20240115T090000Z", "20240115T100000Z")
	src := config.SourceConfig{Name: "src1", Type: "ical", URL: "http://x"}

	cal, objPath := syncer.BuildDestObject(src, g, "hash1", nil, "/calendars/user/main/")

	if cal == nil {
		t.Fatal("expected non-nil calendar")
	}
	if objPath == "" {
		t.Fatal("expected non-empty path")
	}
	// Path should end in .ics
	if len(objPath) < 4 || objPath[len(objPath)-4:] != ".ics" {
		t.Errorf("expected path ending in .ics, got %q", objPath)
	}

	// Parent VEVENT should have tracking props
	var parent *goical.Component
	for _, ch := range cal.Children {
		if ch.Name == goical.CompEvent {
			parent = ch
			break
		}
	}
	if parent == nil {
		t.Fatal("no VEVENT in calendar")
	}
	if ical.PropValue(parent, ical.PropOriginUID) != g.Key {
		t.Errorf("X-CALSYNCER-ORIGIN-UID: got %q, want %q", ical.PropValue(parent, ical.PropOriginUID), g.Key)
	}
	if ical.PropValue(parent, ical.PropOriginHash) != "hash1" {
		t.Errorf("X-CALSYNCER-HASH: got %q, want hash1", ical.PropValue(parent, ical.PropOriginHash))
	}
}

func TestBuildDestObject_ExistingPath(t *testing.T) {
	g := makeGroup("uid1", "Test Event", "20240115T090000Z", "20240115T100000Z")
	src := config.SourceConfig{Name: "src1", Type: "ical", URL: "http://x"}
	existing := &caldav.SyncedObject{
		Path: "/calendars/user/main/existing.ics",
		ETag: `"etag1"`,
	}

	_, objPath := syncer.BuildDestObject(src, g, "hash1", existing, "/calendars/user/main/")

	if objPath != existing.Path {
		t.Errorf("expected existing path %q, got %q", existing.Path, objPath)
	}
}

func TestBuildDestObject_DeterministicUID(t *testing.T) {
	g := makeGroup("uid1", "Test Event", "20240115T090000Z", "20240115T100000Z")
	src := config.SourceConfig{Name: "src1", Type: "ical", URL: "http://x"}

	cal1, _ := syncer.BuildDestObject(src, g, "hash1", nil, "/cal/")
	cal2, _ := syncer.BuildDestObject(src, g, "hash1", nil, "/cal/")

	uid1 := ical.PropValue(cal1.Children[0], goical.PropUID)
	uid2 := ical.PropValue(cal2.Children[0], goical.PropUID)
	if uid1 != uid2 {
		t.Errorf("UIDs should be deterministic: %q vs %q", uid1, uid2)
	}
}

func TestBuildDestObject_WithEmoji(t *testing.T) {
	g := makeGroup("uid1", "Meeting", "20240115T090000Z", "20240115T100000Z")
	src := config.SourceConfig{Name: "src1", Type: "ical", URL: "http://x", Emoji: "🔴"}

	cal, _ := syncer.BuildDestObject(src, g, "hash1", nil, "/cal/")

	summary := ical.PropValue(cal.Children[0], goical.PropSummary)
	if summary != "🔴 Meeting" {
		t.Errorf("expected emoji-prefixed summary, got %q", summary)
	}
}

func TestBuildDestObject_WithExceptions(t *testing.T) {
	g := makeGroup("uid1", "Recurring", "20240115T090000Z", "20240115T100000Z")
	ex := goical.NewComponent(goical.CompEvent)
	ex.Props.SetText(goical.PropUID, "uid1")
	ex.Props.SetText(goical.PropSummary, "Exception")
	rp := goical.NewProp(goical.PropRecurrenceID)
	rp.Value = "20240116T090000Z"
	ex.Props[goical.PropRecurrenceID] = []goical.Prop{*rp}
	g.Exceptions = []*goical.Component{ex}

	src := config.SourceConfig{Name: "src1", Type: "ical", URL: "http://x"}
	cal, _ := syncer.BuildDestObject(src, g, "hash1", nil, "/cal/")

	// Should have parent + 1 exception = 2 VEVENTs
	if len(cal.Children) != 2 {
		t.Errorf("expected 2 VEVENTs (parent+exception), got %d", len(cal.Children))
	}
}

func TestBuildDestObject_EnsuresDTSTAMP(t *testing.T) {
	// Event with no DTSTAMP — should get one added
	g := makeGroup("uid1", "No DTSTAMP", "20240115T090000Z", "20240115T100000Z")
	src := config.SourceConfig{Name: "src1", Type: "ical", URL: "http://x"}

	cal, _ := syncer.BuildDestObject(src, g, "hash1", nil, "/cal/")

	dtstamp := cal.Children[0].Props.Get(goical.PropDateTimeStamp)
	if dtstamp == nil {
		t.Error("expected DTSTAMP to be added")
	}
}

func TestBuildDestObject_PreservesDTSTAMP(t *testing.T) {
	// Event with an existing DTSTAMP — should be preserved
	g := makeGroup("uid1", "Has DTSTAMP", "20240115T090000Z", "20240115T100000Z")
	existingStamp := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	p := goical.NewProp(goical.PropDateTimeStamp)
	p.SetDateTime(existingStamp)
	g.Parent.Props[goical.PropDateTimeStamp] = []goical.Prop{*p}

	src := config.SourceConfig{Name: "src1", Type: "ical", URL: "http://x"}
	cal, _ := syncer.BuildDestObject(src, g, "hash1", nil, "/cal/")

	dtstamp := cal.Children[0].Props.Get(goical.PropDateTimeStamp)
	if dtstamp == nil {
		t.Fatal("expected DTSTAMP")
	}
	got, err := dtstamp.DateTime(time.UTC)
	if err != nil {
		t.Fatalf("parsing DTSTAMP: %v", err)
	}
	if !got.Equal(existingStamp) {
		t.Errorf("DTSTAMP changed: got %v, want %v", got, existingStamp)
	}
}
