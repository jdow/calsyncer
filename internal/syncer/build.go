package syncer

import (
	"path"
	"time"

	goical "github.com/emersion/go-ical"
	"github.com/google/uuid"
	"github.com/jdow/calsyncer/internal/caldav"
	"github.com/jdow/calsyncer/internal/config"
	"github.com/jdow/calsyncer/internal/ical"
)

// BuildDestObject builds the VCALENDAR to write to the destination for group.
// The parent VEVENT gets tracking metadata and an emoji prefix (if configured).
// Exception VEVENTs are copied with the rewritten UID. The object path is stable
// across runs because the destination UID is deterministic.
func BuildDestObject(
	src config.SourceConfig,
	group *ical.EventGroup,
	hash string,
	existing *caldav.SyncedObject,
	calendarURL string,
) (*goical.Calendar, string) {
	destUID := deterministicUID(src.Name, group.Key)

	cal := goical.NewCalendar()
	cal.Props.SetText("PRODID", "-//calsyncer//calsyncer//EN")
	cal.Props.SetText("VERSION", "2.0")

	parent := copyComponent(group.Parent)
	ensureDTSTAMP(parent)
	removeDurationOrDTEnd(parent)
	parent.Props.SetText(goical.PropUID, destUID)

	if src.Emoji != "" {
		existingSummary := ical.PropValue(parent, goical.PropSummary)
		parent.Props.SetText(goical.PropSummary, src.Emoji+" "+existingSummary)
	}

	setCustomProp(parent, goical.PropSource, src.Name)
	setCustomProp(parent, ical.PropOriginUID, group.Key)
	setCustomProp(parent, ical.PropOriginHash, hash)
	cal.Children = append(cal.Children, parent)

	for _, ex := range group.Exceptions {
		destEx := copyComponent(ex)
		ensureDTSTAMP(destEx)
		removeDurationOrDTEnd(destEx)
		destEx.Props.SetText(goical.PropUID, destUID)
		cal.Children = append(cal.Children, destEx)
	}

	var objPath string
	if existing != nil {
		objPath = existing.Path
	} else {
		objPath = path.Join(calendarURL, destUID+".ics")
	}

	return cal, objPath
}

func removeDurationOrDTEnd(c *goical.Component) {
	dtend := c.Props.Get(goical.PropDateTimeEnd)
	duration := c.Props.Get(goical.PropDuration)

	if dtend != nil && duration != nil {
		// Prefer DTEND; drop DURATION
		c.Props.Del(goical.PropDuration)
	}
}

func copyComponent(ev *goical.Component) *goical.Component {
	dst := goical.NewComponent(ev.Name)
	for name, props := range ev.Props {
		dst.Props[name] = append([]goical.Prop(nil), props...)
	}
	return dst
}

// deterministicUID returns a UUID v5 derived from sourceName and key,
// so the same source event always maps to the same destination UID.
func deterministicUID(sourceName, key string) string {
	ns := uuid.NameSpaceURL
	return uuid.NewSHA1(ns, []byte(sourceName+"|"+key)).String()
}

func setCustomProp(ev *goical.Component, name, value string) {
	prop := goical.NewProp(name)
	prop.Value = value
	ev.Props[name] = []goical.Prop{*prop}
}

// ensureDTSTAMP adds a DTSTAMP if absent. RFC 5545 requires one on every VEVENT.
func ensureDTSTAMP(ev *goical.Component) {
	if len(ev.Props[goical.PropDateTimeStamp]) == 0 {
		p := goical.NewProp(goical.PropDateTimeStamp)
		p.SetDateTime(time.Now().UTC())
		ev.Props[goical.PropDateTimeStamp] = []goical.Prop{*p}
	}
}
