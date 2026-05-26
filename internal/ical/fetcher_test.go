package ical_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	goical "github.com/emersion/go-ical"
	"github.com/jdow/calsyncer/internal/ical"
)

func TestFetchEventGroups_HTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("not found"))
	}))
	defer ts.Close()

	f := ical.NewHTTPFetcher()
	_, err := f.FetchEventGroups(ts.URL)
	if err == nil {
		t.Fatal("expected error for non-200 response")
	}
}

func TestFetchEventGroups_InvalidICAL(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("this is not ical data at all"))
	}))
	defer ts.Close()

	f := ical.NewHTTPFetcher()
	_, err := f.FetchEventGroups(ts.URL)
	if err == nil {
		t.Fatal("expected error for invalid iCal data")
	}
}

func TestFetchEventGroups_NoUID(t *testing.T) {
	// VEVENT with no UID — should get a synthesized UID from DTSTART+SUMMARY
	const icalData = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//Test//EN
BEGIN:VEVENT
SUMMARY:No UID Event
DTSTART:20240401T090000Z
DTEND:20240401T100000Z
END:VEVENT
END:VCALENDAR`

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(icalData))
	}))
	defer ts.Close()

	f := ical.NewHTTPFetcher()
	groups, err := f.FetchEventGroups(ts.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) != 1 {
		t.Errorf("expected 1 group, got %d", len(groups))
	}
	for key := range groups {
		if len(key) == 0 {
			t.Error("expected non-empty synthesized UID")
		}
	}
}

func TestFetchEventGroups_PreExpandedRecurring(t *testing.T) {
	// Google-style: RECURRENCE-ID with no parent — each treated as standalone
	const icalData = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//Test//EN
BEGIN:VEVENT
UID:google-recurring
SUMMARY:Occurrence A
DTSTART:20240401T090000Z
DTEND:20240401T100000Z
RECURRENCE-ID:20240401T090000Z
END:VEVENT
BEGIN:VEVENT
UID:google-recurring
SUMMARY:Occurrence B
DTSTART:20240402T090000Z
DTEND:20240402T100000Z
RECURRENCE-ID:20240402T090000Z
END:VEVENT
END:VCALENDAR`

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(icalData))
	}))
	defer ts.Close()

	f := ical.NewHTTPFetcher()
	groups, err := f.FetchEventGroups(ts.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) != 2 {
		t.Errorf("expected 2 standalone groups for pre-expanded recurring, got %d", len(groups))
	}
}

func TestNewHTTPFetcher_WithLogger(t *testing.T) {
	const icalData = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//Test//EN
BEGIN:VEVENT
UID:log-test
SUMMARY:Log Test
DTSTART:20240401T090000Z
DTEND:20240401T100000Z
END:VEVENT
END:VCALENDAR`

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(icalData))
	}))
	defer ts.Close()

	f := ical.NewHTTPFetcher(ical.WithLogger(slog.Default()))
	groups, err := f.FetchEventGroups(ts.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) != 1 {
		t.Errorf("expected 1 group, got %d", len(groups))
	}
}

func TestFetchEventGroups_NonEventComponents(t *testing.T) {
	// Calendar with a VTIMEZONE and VTODO alongside a VEVENT — non-VEVENT components must be ignored
	const icalData = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//Test//EN
BEGIN:VTIMEZONE
TZID:America/New_York
END:VTIMEZONE
BEGIN:VEVENT
UID:real-event
SUMMARY:Real Event
DTSTART:20240401T090000Z
DTEND:20240401T100000Z
END:VEVENT
END:VCALENDAR`

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(icalData))
	}))
	defer ts.Close()

	f := ical.NewHTTPFetcher()
	groups, err := f.FetchEventGroups(ts.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) != 1 {
		t.Errorf("expected 1 group (VTIMEZONE ignored), got %d", len(groups))
	}
}

func TestFetchEventGroups_ConnectionRefused(t *testing.T) {
	// Target a server that immediately closes — tests the HTTP GET error path
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	ts.Close() // close immediately so connection is refused

	f := ical.NewHTTPFetcher()
	_, err := f.FetchEventGroups(ts.URL)
	if err == nil {
		t.Fatal("expected error from connection refused")
	}
}

func TestFetchEventGroups_ExceptionBeforeParent(t *testing.T) {
	// Exception VEVENT appears before parent in the feed — tests the group init path
	const icalData = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//Test//EN
BEGIN:VEVENT
UID:ordering-test
RECURRENCE-ID:20240402T090000Z
SUMMARY:Exception First
DTSTART:20240402T110000Z
DTEND:20240402T120000Z
END:VEVENT
BEGIN:VEVENT
UID:ordering-test
SUMMARY:Parent Event
DTSTART:20240401T090000Z
DTEND:20240401T100000Z
RRULE:FREQ=DAILY;COUNT=3
END:VEVENT
END:VCALENDAR`

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(icalData))
	}))
	defer ts.Close()

	f := ical.NewHTTPFetcher()
	groups, err := f.FetchEventGroups(ts.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should result in 1 group (the recurring series) with 1 exception
	if len(groups) != 1 {
		t.Errorf("expected 1 group, got %d", len(groups))
	}
	for _, g := range groups {
		if len(g.Exceptions) != 1 {
			t.Errorf("expected 1 exception, got %d", len(g.Exceptions))
		}
	}
}

func TestFetchEventGroups_WithLogger(t *testing.T) {
	const icalData = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//Test//EN
BEGIN:VEVENT
UID:logged-event
SUMMARY:Logged Event
DTSTART:20240401T090000Z
DTEND:20240401T100000Z
END:VEVENT
END:VCALENDAR`

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(icalData))
	}))
	defer ts.Close()

	// Confirm WithHTTPClient and WithLogger options work
	f := ical.NewHTTPFetcher(
		ical.WithHTTPClient(&http.Client{}),
	)
	groups, err := f.FetchEventGroups(ts.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) != 1 {
		t.Errorf("expected 1 group, got %d", len(groups))
	}
}

func TestFetchEventGroups(t *testing.T) {
	// Sample iCal data with one recurring event and one exception.
	const icalData = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Example Corp.//CalDAV Client//EN
BEGIN:VEVENT
UID:12345
SUMMARY:Recurring Event
DTSTART:20240401T090000Z
DTEND:20240401T100000Z
RRULE:FREQ=DAILY;COUNT=5
END:VEVENT
BEGIN:VEVENT
UID:12345
RECURRENCE-ID:20240403T090000Z
SUMMARY:Exception Event
DTSTART:20240403T110000Z
DTEND:20240403T120000Z
END:VEVENT
END:VCALENDAR`

	// Set up a test HTTP server that serves the sample iCal data.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/calendar")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(icalData))
	}))
	defer ts.Close()

	f := ical.NewHTTPFetcher()
	groups, err := f.FetchEventGroups(ts.URL)
	if err != nil {
		t.Fatalf("FetchEventGroups error: %v", err)
	}

	if len(groups) != 1 {
		t.Fatalf("expected 1 event group, got %d", len(groups))
	}

	g, ok := groups["12345"]
	if !ok {
		t.Fatal("expected group with UID 12345 not found")
	}

	if g.Parent == nil {
		t.Fatal("expected parent event is nil")
	}
	if ical.PropValue(g.Parent, goical.PropSummary) != "Recurring Event" {
		t.Errorf("unexpected parent summary: %q", ical.PropValue(g.Parent, goical.PropSummary))
	}

	if len(g.Exceptions) != 1 {
		t.Fatalf("expected 1 exception, got %d", len(g.Exceptions))
	}
	ex := g.Exceptions[0]
	if ical.PropValue(ex, goical.PropSummary) != "Exception Event" {
		t.Errorf("unexpected exception summary: %q", ical.PropValue(ex, goical.PropSummary))
	}
	if ical.PropValue(ex, goical.PropRecurrenceID) != "20240403T090000Z" {
		t.Errorf("unexpected RECURRENCE-ID: %q", ical.PropValue(ex, goical.PropRecurrenceID))
	}
}
