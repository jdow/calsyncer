package ical

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	goical "github.com/emersion/go-ical"
)

// Fetcher fetches iCal event groups from a URL.
type Fetcher interface {
	FetchEventGroups(url string) (map[string]*EventGroup, error)
}

// HTTPFetcher fetches iCal feeds over HTTP.
type HTTPFetcher struct {
	client *http.Client
	logger *slog.Logger
}

// Option is a functional option for HTTPFetcher.
type Option func(*HTTPFetcher)

// WithHTTPClient overrides the default HTTP client.
func WithHTTPClient(client *http.Client) Option {
	return func(f *HTTPFetcher) {
		f.client = client
	}
}

// WithLogger attaches a logger. Without this, debug output is discarded.
func WithLogger(logger *slog.Logger) Option {
	return func(f *HTTPFetcher) {
		f.logger = logger
	}
}

// NewHTTPFetcher returns an HTTPFetcher. The default HTTP client has a 30s timeout.
func NewHTTPFetcher(opts ...Option) *HTTPFetcher {
	f := &HTTPFetcher{}
	for _, opt := range opts {
		opt(f)
	}
	if f.client == nil {
		f.client = &http.Client{Timeout: 30 * time.Second}
	}
	if f.logger == nil {
		f.logger = slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return f
}

// FetchEventGroups downloads the feed at url and returns all event groups.
//
// Three feed styles are handled:
//  1. Standard recurring series: parent VEVENT with RRULE, plus exception VEVENTs with RECURRENCE-ID
//  2. Pre-expanded feeds (e.g. Google free/busy): every occurrence has a RECURRENCE-ID but there
//     is no parent VEVENT — each occurrence is treated as a standalone event
//  3. One-off events: a plain VEVENT with no RRULE or RECURRENCE-ID
func (f *HTTPFetcher) FetchEventGroups(url string) (map[string]*EventGroup, error) {
	resp, err := f.client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("HTTP GET %s: %w", url, err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			f.logger.Error("failed to close response body", "error", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("HTTP GET %s: status %d: %s", url, resp.StatusCode, body)
	}

	dec := goical.NewDecoder(resp.Body)
	cal, err := dec.Decode()
	if err != nil {
		return nil, fmt.Errorf("decoding iCal from %s: %w", url, err)
	}

	// First pass: identify which UIDs have a parent event.
	// This lets us distinguish true recurring-series exceptions from
	// Google-style pre-expanded occurrences that have no parent.
	parents := make(map[string]bool)
	for _, child := range cal.Children {
		if child.Name != goical.CompEvent {
			continue
		}
		if child.Props.Get(goical.PropRecurrenceID) == nil {
			parents[componentUID(child)] = true
		}
	}

	groups := make(map[string]*EventGroup)

	// Second pass: build event groups.
	for _, child := range cal.Children {
		if child.Name != goical.CompEvent {
			continue
		}

		uid := componentUID(child)
		hasRRule := child.Props.Get(goical.PropRecurrenceRule) != nil
		recurrenceID := child.Props.Get(goical.PropRecurrenceID)

		if recurrenceID != nil {
			if parents[uid] {
				// Exception for a known recurring series — attach to the parent's group.
				g, ok := groups[uid]
				if !ok {
					g = &EventGroup{UID: uid, Key: uid}
					groups[uid] = g
				}
				g.Exceptions = append(g.Exceptions, child)
			} else {
				// No parent for this UID — Google-style pre-expanded occurrence.
				// Strip RECURRENCE-ID and treat it as an independent event.
				dtstart := PropValue(child, goical.PropDateTimeStart)
				key := uid + ":" + dtstart
				if _, ok := groups[key]; !ok {
					groups[key] = &EventGroup{
						UID:    uid,
						Key:    key,
						Parent: cloneWithoutProp(child, goical.PropRecurrenceID),
					}
				}
			}
			continue
		}

		// No RECURRENCE-ID: either a recurring series parent (keyed by UID alone)
		// or a one-off event (keyed by UID+DTSTART so distinct events don't collide).
		var key string
		if hasRRule {
			key = uid
		} else {
			dtstart := PropValue(child, goical.PropDateTimeStart)
			key = uid + ":" + dtstart
		}

		g, ok := groups[key]
		if !ok {
			g = &EventGroup{UID: uid, Key: key}
			groups[key] = g
		}
		g.Parent = child
	}

	// Drop any groups without a parent (malformed: exceptions with no parent).
	for key, g := range groups {
		if g.Parent == nil {
			delete(groups, key)
		}
	}

	f.logger.Debug("fetched event groups",
		"total", len(groups),
	)

	return groups, nil
}

func cloneWithoutProp(ev *goical.Component, propName string) *goical.Component {
	dst := goical.NewComponent(ev.Name)
	for name, props := range ev.Props {
		if name == propName {
			continue
		}
		dst.Props[name] = append([]goical.Prop(nil), props...)
	}
	return dst
}

// componentUID returns the UID of a VEVENT, synthesizing one from DTSTART+SUMMARY
// if the UID property is missing or empty.
func componentUID(ev *goical.Component) string {
	p := ev.Props.Get(goical.PropUID)
	if p != nil && p.Value != "" {
		return p.Value
	}
	var s string
	if p := ev.Props.Get(goical.PropDateTimeStart); p != nil {
		s += p.Value
	}
	if p := ev.Props.Get(goical.PropSummary); p != nil {
		s += p.Value
	}
	h := simpleHash(s)
	return fmt.Sprintf("generated-%x", h)
}

func simpleHash(s string) uint64 {
	var h uint64 = 14695981039346656037
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}
