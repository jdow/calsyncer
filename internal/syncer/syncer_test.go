package syncer_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	goical "github.com/emersion/go-ical"
	"github.com/jdow/calsyncer/internal/caldav"
	"github.com/jdow/calsyncer/internal/config"
	"github.com/jdow/calsyncer/internal/ical"
	"github.com/jdow/calsyncer/internal/syncer"
)

// --- Mock types ---

type mockCalDAVClient struct {
	calURL      string
	objects     map[string]*caldav.SyncedObject // keyed by "source|key"
	puts        []string                        // paths put
	deletes     []string                        // paths deleted
	fetchError  error                           // if set, FetchSyncedObjects returns this
	putError    error                           // if set, PutCalendarObject returns this
	deleteError error                           // if set, DeleteCalendarObject returns this
}

func (m *mockCalDAVClient) CalendarURL() string {
	return m.calURL
}

func (m *mockCalDAVClient) FetchSyncedObjects(_ context.Context, sourceName string) (map[string]*caldav.SyncedObject, error) {
	if m.fetchError != nil {
		return nil, m.fetchError
	}
	result := make(map[string]*caldav.SyncedObject)
	for k, v := range m.objects {
		if sourceName == "" || v.SourceName == sourceName {
			result[k] = v
		}
	}
	return result, nil
}

func (m *mockCalDAVClient) PutCalendarObject(_ context.Context, objPath string, _ *goical.Calendar, _ string) error {
	m.puts = append(m.puts, objPath)
	return m.putError
}

func (m *mockCalDAVClient) DeleteCalendarObject(_ context.Context, objPath string) error {
	m.deletes = append(m.deletes, objPath)
	return m.deleteError
}

type mockFetcher struct {
	groups map[string]*ical.EventGroup
	err    error
}

func (m *mockFetcher) FetchEventGroups(_ string) (map[string]*ical.EventGroup, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.groups, nil
}

// --- Helpers ---

func makeGroup(uid, summary, dtstart, dtend string) *ical.EventGroup {
	ev := goical.NewComponent(goical.CompEvent)
	ev.Props.SetText(goical.PropUID, uid)
	ev.Props.SetText(goical.PropSummary, summary)

	ps := goical.NewProp(goical.PropDateTimeStart)
	ps.Value = dtstart
	ev.Props[goical.PropDateTimeStart] = []goical.Prop{*ps}

	pe := goical.NewProp(goical.PropDateTimeEnd)
	pe.Value = dtend
	ev.Props[goical.PropDateTimeEnd] = []goical.Prop{*pe}

	key := uid + ":" + dtstart
	return &ical.EventGroup{UID: uid, Key: key, Parent: ev}
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

	key := uid + ":" + dtstart.UTC().Format("20060102T150405Z")
	return &ical.EventGroup{UID: uid, Key: key, Parent: ev}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// --- Tests ---

func TestSyncer_CreateNew(t *testing.T) {
	g := makeGroup("uid1", "Test Event", "20240115T090000Z", "20240115T100000Z")

	dest := &mockCalDAVClient{
		calURL:  "/calendars/user/main/",
		objects: map[string]*caldav.SyncedObject{},
	}
	fetcher := &mockFetcher{
		groups: map[string]*ical.EventGroup{g.Key: g},
	}
	cfg := &config.Config{
		Sources: []config.SourceConfig{{Name: "src1", Type: "ical", URL: "http://example.com/cal.ics"}},
	}

	s, err := syncer.NewSyncer(
		syncer.WithCalDAVClient(dest),
		syncer.WithICalFetcher(fetcher),
		syncer.WithConfig(cfg),
		syncer.WithLogger(discardLogger()),
		syncer.WithUpdate(true),
	)
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}

	if err := s.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(dest.puts) != 1 {
		t.Errorf("expected 1 Put, got %d", len(dest.puts))
	}
	if len(dest.deletes) != 0 {
		t.Errorf("expected 0 Deletes, got %d", len(dest.deletes))
	}
}

func TestSyncer_SkipUnchanged(t *testing.T) {
	g := makeGroup("uid1", "Test Event", "20240115T090000Z", "20240115T100000Z")
	hash := ical.HashGroup(g)
	key := "src1|" + g.Key

	dest := &mockCalDAVClient{
		calURL: "/calendars/user/main/",
		objects: map[string]*caldav.SyncedObject{
			key: {
				ETag:       `"etag1"`,
				Path:       "/calendars/user/main/event.ics",
				SourceName: "src1",
				OriginUID:  g.Key,
				Hash:       hash,
			},
		},
	}
	fetcher := &mockFetcher{
		groups: map[string]*ical.EventGroup{g.Key: g},
	}
	cfg := &config.Config{
		Sources: []config.SourceConfig{{Name: "src1", Type: "ical", URL: "http://example.com/cal.ics"}},
	}

	s, err := syncer.NewSyncer(
		syncer.WithCalDAVClient(dest),
		syncer.WithICalFetcher(fetcher),
		syncer.WithConfig(cfg),
		syncer.WithLogger(discardLogger()),
		syncer.WithUpdate(true),
	)
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}

	if err := s.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(dest.puts) != 0 {
		t.Errorf("expected 0 Puts (unchanged), got %d", len(dest.puts))
	}
}

func TestSyncer_UpdateChanged(t *testing.T) {
	g := makeGroup("uid1", "Test Event", "20240115T090000Z", "20240115T100000Z")
	key := "src1|" + g.Key

	dest := &mockCalDAVClient{
		calURL: "/calendars/user/main/",
		objects: map[string]*caldav.SyncedObject{
			key: {
				ETag:       `"etag1"`,
				Path:       "/calendars/user/main/event.ics",
				SourceName: "src1",
				OriginUID:  g.Key,
				Hash:       "oldhash",
			},
		},
	}
	fetcher := &mockFetcher{
		groups: map[string]*ical.EventGroup{g.Key: g},
	}
	cfg := &config.Config{
		Sources: []config.SourceConfig{{Name: "src1", Type: "ical", URL: "http://example.com/cal.ics"}},
	}

	s, err := syncer.NewSyncer(
		syncer.WithCalDAVClient(dest),
		syncer.WithICalFetcher(fetcher),
		syncer.WithConfig(cfg),
		syncer.WithLogger(discardLogger()),
		syncer.WithUpdate(true),
	)
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}

	if err := s.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(dest.puts) != 1 {
		t.Errorf("expected 1 Put (updated), got %d", len(dest.puts))
	}
}

func TestSyncer_DeleteRemoved(t *testing.T) {
	// dest has an event, fetcher returns nothing — should delete
	key := "src1|uid1:20240115T090000Z"

	dest := &mockCalDAVClient{
		calURL: "/calendars/user/main/",
		objects: map[string]*caldav.SyncedObject{
			key: {
				ETag:       `"etag1"`,
				Path:       "/calendars/user/main/event.ics",
				SourceName: "src1",
				OriginUID:  "uid1:20240115T090000Z",
				Hash:       "somehash",
			},
		},
	}
	fetcher := &mockFetcher{
		groups: map[string]*ical.EventGroup{},
	}
	cfg := &config.Config{
		Sources: []config.SourceConfig{{Name: "src1", Type: "ical", URL: "http://example.com/cal.ics"}},
	}

	s, err := syncer.NewSyncer(
		syncer.WithCalDAVClient(dest),
		syncer.WithICalFetcher(fetcher),
		syncer.WithConfig(cfg),
		syncer.WithLogger(discardLogger()),
		syncer.WithUpdate(true),
	)
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}

	if err := s.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(dest.deletes) != 1 {
		t.Errorf("expected 1 Delete, got %d", len(dest.deletes))
	}
	if len(dest.puts) != 0 {
		t.Errorf("expected 0 Puts, got %d", len(dest.puts))
	}
}

func TestSyncer_DryRun(t *testing.T) {
	g := makeGroup("uid1", "Test Event", "20240115T090000Z", "20240115T100000Z")
	existingKey := "src1|uid2:20240115T090000Z"

	dest := &mockCalDAVClient{
		calURL: "/calendars/user/main/",
		objects: map[string]*caldav.SyncedObject{
			existingKey: {
				ETag:       `"etag1"`,
				Path:       "/calendars/user/main/old.ics",
				SourceName: "src1",
				OriginUID:  "uid2:20240115T090000Z",
				Hash:       "oldhash",
			},
		},
	}
	fetcher := &mockFetcher{
		groups: map[string]*ical.EventGroup{g.Key: g},
	}
	cfg := &config.Config{
		Sources: []config.SourceConfig{{Name: "src1", Type: "ical", URL: "http://example.com/cal.ics"}},
	}

	// update=false means dry-run
	s, err := syncer.NewSyncer(
		syncer.WithCalDAVClient(dest),
		syncer.WithICalFetcher(fetcher),
		syncer.WithConfig(cfg),
		syncer.WithLogger(discardLogger()),
		syncer.WithUpdate(false),
	)
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}

	if err := s.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(dest.puts) != 0 {
		t.Errorf("dry-run: expected 0 Puts, got %d", len(dest.puts))
	}
	if len(dest.deletes) != 0 {
		t.Errorf("dry-run: expected 0 Deletes, got %d", len(dest.deletes))
	}
}

func TestSyncer_TransformSkip(t *testing.T) {
	g := makeGroup("uid1", "Skip Me", "20240115T090000Z", "20240115T100000Z")

	dest := &mockCalDAVClient{
		calURL:  "/calendars/user/main/",
		objects: map[string]*caldav.SyncedObject{},
	}
	fetcher := &mockFetcher{
		groups: map[string]*ical.EventGroup{g.Key: g},
	}
	cfg := &config.Config{
		Sources: []config.SourceConfig{{
			Name: "src1",
			Type: "ical",
			URL:  "http://example.com/cal.ics",
			Transforms: []config.TransformConfig{{
				WhenSummary: "Skip Me",
				Skip:        true,
			}},
		}},
	}

	s, err := syncer.NewSyncer(
		syncer.WithCalDAVClient(dest),
		syncer.WithICalFetcher(fetcher),
		syncer.WithConfig(cfg),
		syncer.WithLogger(discardLogger()),
		syncer.WithUpdate(true),
	)
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}

	if err := s.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(dest.puts) != 0 {
		t.Errorf("transform skip: expected 0 Puts, got %d", len(dest.puts))
	}
}

func makeDateGroup(uid, summary, dtstart, dtend string) *ical.EventGroup {
	ev := goical.NewComponent(goical.CompEvent)
	ev.Props.SetText(goical.PropUID, uid)
	ev.Props.SetText(goical.PropSummary, summary)

	start, _ := time.ParseInLocation("20060102", dtstart, time.UTC)
	end, _ := time.ParseInLocation("20060102", dtend, time.UTC)

	ps := goical.NewProp(goical.PropDateTimeStart)
	ps.SetDate(start)
	ev.Props[goical.PropDateTimeStart] = []goical.Prop{*ps}

	pe := goical.NewProp(goical.PropDateTimeEnd)
	pe.SetDate(end)
	ev.Props[goical.PropDateTimeEnd] = []goical.Prop{*pe}

	key := uid + ":" + dtstart
	return &ical.EventGroup{UID: uid, Key: key, Parent: ev}
}

func TestSyncer_IgnoreAllDayBusy(t *testing.T) {
	g := makeDateGroup("uid1", "Busy", "20240115", "20240116")

	dest := &mockCalDAVClient{
		calURL:  "/calendars/user/main/",
		objects: map[string]*caldav.SyncedObject{},
	}
	fetcher := &mockFetcher{
		groups: map[string]*ical.EventGroup{g.Key: g},
	}
	cfg := &config.Config{
		Sources: []config.SourceConfig{{
			Name:             "src1",
			Type:             "ical",
			URL:              "http://example.com/cal.ics",
			IgnoreAllDayBusy: true,
		}},
	}

	s, err := syncer.NewSyncer(
		syncer.WithCalDAVClient(dest),
		syncer.WithICalFetcher(fetcher),
		syncer.WithConfig(cfg),
		syncer.WithLogger(discardLogger()),
		syncer.WithUpdate(true),
	)
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}

	if err := s.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(dest.puts) != 0 {
		t.Errorf("ignoreAllDayBusy: expected 0 Puts, got %d", len(dest.puts))
	}
	if s.AllDayBusySkip != 1 {
		t.Errorf("expected AllDayBusySkip=1, got %d", s.AllDayBusySkip)
	}
}

func TestSyncer_IgnoreAllDayBusy_FlagOffStillSyncs(t *testing.T) {
	// Same "Busy" all-day event, but the flag is off (default) — should sync normally.
	g := makeDateGroup("uid1", "Busy", "20240115", "20240116")

	dest := &mockCalDAVClient{
		calURL:  "/calendars/user/main/",
		objects: map[string]*caldav.SyncedObject{},
	}
	fetcher := &mockFetcher{
		groups: map[string]*ical.EventGroup{g.Key: g},
	}
	cfg := &config.Config{
		Sources: []config.SourceConfig{{Name: "src1", Type: "ical", URL: "http://example.com/cal.ics"}},
	}

	s, err := syncer.NewSyncer(
		syncer.WithCalDAVClient(dest),
		syncer.WithICalFetcher(fetcher),
		syncer.WithConfig(cfg),
		syncer.WithLogger(discardLogger()),
		syncer.WithUpdate(true),
	)
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}

	if err := s.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(dest.puts) != 1 {
		t.Errorf("flag off: expected 1 Put, got %d", len(dest.puts))
	}
}

func TestSyncer_SingleCalendarFilter(t *testing.T) {
	// WithSingleCalendar("src1") — src2 source should be skipped entirely
	g := makeGroup("uid1", "Test Event", "20240115T090000Z", "20240115T100000Z")

	dest := &mockCalDAVClient{
		calURL:  "/calendars/user/main/",
		objects: map[string]*caldav.SyncedObject{},
	}
	fetcher := &mockFetcher{
		groups: map[string]*ical.EventGroup{g.Key: g},
	}
	cfg := &config.Config{
		Sources: []config.SourceConfig{
			{Name: "src1", Type: "ical", URL: "http://example.com/src1.ics"},
			{Name: "src2", Type: "ical", URL: "http://example.com/src2.ics"},
		},
	}

	s, err := syncer.NewSyncer(
		syncer.WithCalDAVClient(dest),
		syncer.WithICalFetcher(fetcher),
		syncer.WithConfig(cfg),
		syncer.WithLogger(discardLogger()),
		syncer.WithUpdate(true),
		syncer.WithSingleCalendar("src1"),
	)
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}

	if err := s.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Only src1 events processed — 1 Put for the one event
	if len(dest.puts) != 1 {
		t.Errorf("expected 1 Put (src1 only), got %d", len(dest.puts))
	}
}

func TestSyncer_TransformSetSummary(t *testing.T) {
	g := makeGroup("uid1", "Old Name", "20240115T090000Z", "20240115T100000Z")

	dest := &mockCalDAVClient{
		calURL:  "/calendars/user/main/",
		objects: map[string]*caldav.SyncedObject{},
	}
	fetcher := &mockFetcher{
		groups: map[string]*ical.EventGroup{g.Key: g},
	}
	cfg := &config.Config{
		Sources: []config.SourceConfig{{
			Name: "src1",
			Type: "ical",
			URL:  "http://example.com/cal.ics",
			Transforms: []config.TransformConfig{{
				WhenSummaryContains: "old",
				SetSummary:          "New Name",
			}},
		}},
	}

	s, err := syncer.NewSyncer(
		syncer.WithCalDAVClient(dest),
		syncer.WithICalFetcher(fetcher),
		syncer.WithConfig(cfg),
		syncer.WithLogger(discardLogger()),
		syncer.WithUpdate(true),
	)
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}

	if err := s.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(dest.puts) != 1 {
		t.Errorf("expected 1 Put after transform, got %d", len(dest.puts))
	}
}

func TestSyncer_FetcherError(t *testing.T) {
	// When the fetcher fails, Run should log the error but not return it
	dest := &mockCalDAVClient{
		calURL:  "/calendars/user/main/",
		objects: map[string]*caldav.SyncedObject{},
	}
	fetcher := &mockFetcher{err: fmt.Errorf("fetch failed")}
	cfg := &config.Config{
		Sources: []config.SourceConfig{{Name: "src1", Type: "ical", URL: "http://example.com/cal.ics"}},
	}

	s, err := syncer.NewSyncer(
		syncer.WithCalDAVClient(dest),
		syncer.WithICalFetcher(fetcher),
		syncer.WithConfig(cfg),
		syncer.WithLogger(discardLogger()),
		syncer.WithUpdate(true),
	)
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}

	// Run should not return an error even when a source fetch fails
	if err := s.Run(); err != nil {
		t.Errorf("Run should not return error on source fetch failure, got: %v", err)
	}
	// No puts or deletes expected
	if len(dest.puts) != 0 || len(dest.deletes) != 0 {
		t.Errorf("expected no operations after fetch error")
	}
}

func TestSyncer_FetchSyncedObjectsError(t *testing.T) {
	g := makeGroup("uid1", "Test Event", "20240115T090000Z", "20240115T100000Z")

	dest := &mockCalDAVClient{
		calURL:     "/calendars/user/main/",
		objects:    map[string]*caldav.SyncedObject{},
		fetchError: fmt.Errorf("dest unavailable"),
	}
	fetcher := &mockFetcher{
		groups: map[string]*ical.EventGroup{g.Key: g},
	}
	cfg := &config.Config{
		Sources: []config.SourceConfig{{Name: "src1", Type: "ical", URL: "http://example.com/cal.ics"}},
	}

	s, err := syncer.NewSyncer(
		syncer.WithCalDAVClient(dest),
		syncer.WithICalFetcher(fetcher),
		syncer.WithConfig(cfg),
		syncer.WithLogger(discardLogger()),
		syncer.WithUpdate(true),
	)
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}

	// Run logs the error but doesn't return it
	if err := s.Run(); err != nil {
		t.Errorf("Run should not return error on dest fetch failure, got: %v", err)
	}
}

func TestSyncer_TransformWhenSummaryContains_NoMatch(t *testing.T) {
	// WhenSummaryContains doesn't match → transform is skipped, event is synced normally
	g := makeGroup("uid1", "Unrelated Event", "20240115T090000Z", "20240115T100000Z")

	dest := &mockCalDAVClient{
		calURL:  "/calendars/user/main/",
		objects: map[string]*caldav.SyncedObject{},
	}
	fetcher := &mockFetcher{
		groups: map[string]*ical.EventGroup{g.Key: g},
	}
	cfg := &config.Config{
		Sources: []config.SourceConfig{{
			Name: "src1",
			Type: "ical",
			URL:  "http://example.com/cal.ics",
			Transforms: []config.TransformConfig{{
				WhenSummaryContains: "nomatch",
				Skip:                true,
			}},
		}},
	}

	s, err := syncer.NewSyncer(
		syncer.WithCalDAVClient(dest),
		syncer.WithICalFetcher(fetcher),
		syncer.WithConfig(cfg),
		syncer.WithLogger(discardLogger()),
		syncer.WithUpdate(true),
	)
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}

	if err := s.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Transform didn't match, so event should be synced
	if len(dest.puts) != 1 {
		t.Errorf("expected 1 Put (transform didn't match), got %d", len(dest.puts))
	}
}

func TestSyncer_PutCreateError(t *testing.T) {
	// Put error on create should be logged but not fatal
	g := makeGroup("uid1", "New Event", "20240115T090000Z", "20240115T100000Z")
	dest := &mockCalDAVClient{
		calURL:   "/calendars/user/main/",
		objects:  map[string]*caldav.SyncedObject{},
		putError: fmt.Errorf("server error"),
	}
	fetcher := &mockFetcher{groups: map[string]*ical.EventGroup{g.Key: g}}
	cfg := &config.Config{
		Sources: []config.SourceConfig{{Name: "src1", Type: "ical", URL: "http://x"}},
	}
	s, _ := syncer.NewSyncer(
		syncer.WithCalDAVClient(dest), syncer.WithICalFetcher(fetcher),
		syncer.WithConfig(cfg), syncer.WithLogger(discardLogger()), syncer.WithUpdate(true),
	)
	if err := s.Run(); err != nil {
		t.Errorf("Run should not return error on Put failure: %v", err)
	}
}

func TestSyncer_PutUpdateError(t *testing.T) {
	// Put error on update should be logged but not fatal
	g := makeGroup("uid1", "Existing Event", "20240115T090000Z", "20240115T100000Z")
	key := "src1|" + g.Key
	dest := &mockCalDAVClient{
		calURL: "/calendars/user/main/",
		objects: map[string]*caldav.SyncedObject{
			key: {Path: "/cal/ev.ics", SourceName: "src1", OriginUID: g.Key, Hash: "oldhash"},
		},
		putError: fmt.Errorf("server error"),
	}
	fetcher := &mockFetcher{groups: map[string]*ical.EventGroup{g.Key: g}}
	cfg := &config.Config{
		Sources: []config.SourceConfig{{Name: "src1", Type: "ical", URL: "http://x"}},
	}
	s, _ := syncer.NewSyncer(
		syncer.WithCalDAVClient(dest), syncer.WithICalFetcher(fetcher),
		syncer.WithConfig(cfg), syncer.WithLogger(discardLogger()), syncer.WithUpdate(true),
	)
	if err := s.Run(); err != nil {
		t.Errorf("Run should not return error on Put update failure: %v", err)
	}
}

func TestSyncer_DeleteError(t *testing.T) {
	// Delete error should be logged but not fatal
	key := "src1|uid1:20240115T090000Z"
	dest := &mockCalDAVClient{
		calURL: "/calendars/user/main/",
		objects: map[string]*caldav.SyncedObject{
			key: {Path: "/cal/ev.ics", SourceName: "src1", OriginUID: "uid1:20240115T090000Z", Hash: "h"},
		},
		deleteError: fmt.Errorf("delete failed"),
	}
	fetcher := &mockFetcher{groups: map[string]*ical.EventGroup{}}
	cfg := &config.Config{
		Sources: []config.SourceConfig{{Name: "src1", Type: "ical", URL: "http://x"}},
	}
	s, _ := syncer.NewSyncer(
		syncer.WithCalDAVClient(dest), syncer.WithICalFetcher(fetcher),
		syncer.WithConfig(cfg), syncer.WithLogger(discardLogger()), syncer.WithUpdate(true),
	)
	if err := s.Run(); err != nil {
		t.Errorf("Run should not return error on Delete failure: %v", err)
	}
}

func TestSyncer_TransformWhenSummary_NoMatch(t *testing.T) {
	// WhenSummary set but doesn't match → transform skipped, event synced normally
	g := makeGroup("uid1", "Different Name", "20240115T090000Z", "20240115T100000Z")
	dest := &mockCalDAVClient{calURL: "/cal/", objects: map[string]*caldav.SyncedObject{}}
	fetcher := &mockFetcher{groups: map[string]*ical.EventGroup{g.Key: g}}
	cfg := &config.Config{
		Sources: []config.SourceConfig{{
			Name: "src1", Type: "ical", URL: "http://x",
			Transforms: []config.TransformConfig{{
				WhenSummary: "Specific Name", // doesn't match "Different Name"
				Skip:        true,
			}},
		}},
	}
	s, _ := syncer.NewSyncer(
		syncer.WithCalDAVClient(dest), syncer.WithICalFetcher(fetcher),
		syncer.WithConfig(cfg), syncer.WithLogger(discardLogger()), syncer.WithUpdate(true),
	)
	if err := s.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(dest.puts) != 1 {
		t.Errorf("expected 1 Put (transform didn't match), got %d", len(dest.puts))
	}
}

func TestSyncer_MultiDaySplit(t *testing.T) {
	// Mon 09:00 → Wed 17:00 UTC — should produce 3 groups (timed, allday, timed)
	start := time.Date(2024, 1, 15, 9, 0, 0, 0, time.UTC)
	end := time.Date(2024, 1, 17, 17, 0, 0, 0, time.UTC)
	g := makeTimedGroup("uid1", "Three Day Event", start, end)

	dest := &mockCalDAVClient{
		calURL:  "/calendars/user/main/",
		objects: map[string]*caldav.SyncedObject{},
	}
	fetcher := &mockFetcher{
		groups: map[string]*ical.EventGroup{g.Key: g},
	}
	cfg := &config.Config{
		Sources: []config.SourceConfig{{Name: "src1", Type: "ical", URL: "http://example.com/cal.ics"}},
	}

	s, err := syncer.NewSyncer(
		syncer.WithCalDAVClient(dest),
		syncer.WithICalFetcher(fetcher),
		syncer.WithConfig(cfg),
		syncer.WithLogger(discardLogger()),
		syncer.WithUpdate(true),
	)
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}

	if err := s.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(dest.puts) != 3 {
		t.Errorf("three-day split: expected 3 Puts, got %d", len(dest.puts))
	}
}
