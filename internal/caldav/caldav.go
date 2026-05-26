// Package caldav wraps go-webdav's CalDAV client with the operations calsyncer needs.
package caldav

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	ical "github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
	"github.com/jdow/calsyncer/internal/config"
	calsyncer "github.com/jdow/calsyncer/internal/ical"
)

// Client is the CalDAV client used throughout calsyncer.
type Client struct {
	client      *caldav.Client
	httpClient  webdav.HTTPClient
	rawClient   *http.Client // used for raw PROPFIND (basic auth, no go-webdav wrapping)
	baseURL     string
	calendarURL string // resolved path to the destination calendar collection
	logger      *slog.Logger
}

// Option is a functional option for Client.
type Option func(*Client) error

// WithLogger attaches a logger to the Client.
func WithLogger(logger *slog.Logger) Option {
	return func(c *Client) error {
		c.logger = logger
		return nil
	}
}

// NewCalDAVClient dials the CalDAV server described by cfg and resolves the named calendar.
func NewCalDAVClient(cfg config.DestinationConfig, opts ...Option) (*Client, error) {
	c := &Client{}
	for _, opt := range opts {
		if err := opt(c); err != nil {
			return nil, fmt.Errorf("applying option: %w", err)
		}
	}
	if c.client == nil {
		httpClient := webdav.HTTPClientWithBasicAuth(nil, cfg.Username, cfg.Password)
		c.httpClient = httpClient
		c.baseURL = cfg.URL
		c.rawClient = &http.Client{
			Transport: &basicAuthTransport{
				username: cfg.Username,
				password: cfg.Password,
				base:     http.DefaultTransport,
			},
		}
		var err error
		if c.client, err = caldav.NewClient(httpClient, cfg.URL); err != nil {
			return nil, fmt.Errorf("creating CalDAV client: %w", err)
		}
	}

	principal, err := c.client.FindCurrentUserPrincipal(context.Background())
	if err != nil {
		return nil, fmt.Errorf("finding principal: %w", err)
	}

	homeSet, err := c.client.FindCalendarHomeSet(context.Background(), principal)
	if err != nil {
		return nil, fmt.Errorf("finding calendar home set: %w", err)
	}

	calendars, err := c.client.FindCalendars(context.Background(), homeSet)
	if err != nil {
		return nil, fmt.Errorf("listing calendars: %w", err)
	}

	var calURL string
	for _, cal := range calendars {
		if strings.EqualFold(cal.Name, cfg.CalendarName) {
			calURL = cal.Path
			break
		}
	}
	if calURL == "" {
		var names []string
		for _, cal := range calendars {
			names = append(names, cal.Name)
		}
		return nil, fmt.Errorf("calendar %q not found; available: %v", cfg.CalendarName, names)
	}

	if c.logger != nil {
		c.logger.Debug("connected to CalDAV calendar", "url", calURL)
	}
	c.calendarURL = calURL
	return c, nil
}

// SyncedObject is a CalDAV object (.ics file) that calsyncer previously wrote.
// One object can hold multiple VEVENTs (parent + exceptions for a recurring series).
type SyncedObject struct {
	ETag       string // server ETag, used for conditional PUT/DELETE
	Path       string // URL path on the server
	SourceName string // value of X-CALSYNCER-SRC on the parent VEVENT
	OriginUID  string // value of X-CALSYNCER-ORIGIN-UID
	Hash       string // value of X-CALSYNCER-HASH, used for change detection
}

// FetchSyncedObjects returns all CalDAV objects in the destination that calsyncer owns,
// keyed by "sourceName|originUID". Pass an empty sourceName to return all sources.
//
// Uses a two-phase approach for broad server compatibility:
//  1. PROPFIND Depth:1 to list object paths (only requests getetag, which every server supports)
//  2. calendar-multiget to fetch full .ics content for our custom X- props
func (c *Client) FetchSyncedObjects(ctx context.Context, sourceName string) (map[string]*SyncedObject, error) {
	paths, err := c.propfindObjectPaths(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing calendar objects: %w", err)
	}
	if len(paths) == 0 {
		c.logger.Debug("no objects in destination calendar")
		return make(map[string]*SyncedObject), nil
	}

	objects, err := c.multiGetPaths(ctx, paths)
	if err != nil {
		return nil, fmt.Errorf("multiget calendar objects: %w", err)
	}

	result := make(map[string]*SyncedObject)
	for _, obj := range objects {
		source, originUID, hash := c.extractSyncerMeta(obj)
		if source == "" {
			continue // not owned by calsyncer
		}
		if sourceName != "" && source != sourceName {
			continue // filtering by source, and this isn't it
		}
		key := source + "|" + originUID
		result[key] = &SyncedObject{
			ETag:       obj.ETag,
			Path:       obj.Path,
			SourceName: source,
			OriginUID:  originUID,
			Hash:       hash,
		}
	}

	c.logger.Debug("fetched synced objects from destination", "count", len(result))
	return result, nil
}

// extractSyncerMeta reads calsyncer's tracking properties from the parent VEVENT.
// Returns empty strings if the object was not written by calsyncer.
// iCloud remaps X-CALSYNCER-SRC → SOURCE, so we fall back to the standard SOURCE prop.
func (c *Client) extractSyncerMeta(obj caldav.CalendarObject) (source, originUID, hash string) {
	for _, ev := range obj.Data.Children {
		if ev.Name != ical.CompEvent {
			continue
		}
		if ev.Props.Get(ical.PropRecurrenceID) != nil {
			continue // skip exceptions; metadata lives on the parent only
		}
		if p := ev.Props.Get(calsyncer.PropSource); p != nil {
			source = p.Value
		} else if p := ev.Props.Get(ical.PropSource); p != nil {
			source = p.Value
		}
		if p := ev.Props.Get(calsyncer.PropOriginUID); p != nil {
			originUID = p.Value
		}
		if p := ev.Props.Get(calsyncer.PropOriginHash); p != nil {
			hash = p.Value
		}
		return
	}
	return
}

// propfindObjectPaths lists the paths of all .ics objects in the calendar via PROPFIND Depth:1.
// Requesting only DAV:getetag keeps the response small and works on every server we've tested.
func (c *Client) propfindObjectPaths(ctx context.Context) ([]string, error) {
	fullURL, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, fmt.Errorf("parsing base URL: %w", err)
	}
	fullURL.Path = c.calendarURL

	body := `<?xml version="1.0" encoding="UTF-8"?>
<d:propfind xmlns:d="DAV:">
  <d:prop>
    <d:getetag/>
  </d:prop>
</d:propfind>`

	req, err := http.NewRequestWithContext(ctx, "PROPFIND", fullURL.String(), strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")
	req.Header.Set("Depth", "1")

	resp, err := c.rawClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("PROPFIND %q: %w", fullURL.String(), err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 207 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("PROPFIND returned %d: %s", resp.StatusCode, b)
	}

	var ms struct {
		XMLName   xml.Name `xml:"multistatus"`
		Responses []struct {
			Href string `xml:"href"`
		} `xml:"response"`
	}
	if err := xml.NewDecoder(resp.Body).Decode(&ms); err != nil {
		return nil, fmt.Errorf("parsing PROPFIND response: %w", err)
	}

	var paths []string
	for _, r := range ms.Responses {
		if strings.HasSuffix(r.Href, "/") {
			continue // skip the collection itself
		}
		paths = append(paths, r.Href)
	}
	return paths, nil
}

func (c *Client) multiGetPaths(ctx context.Context, paths []string) ([]caldav.CalendarObject, error) {
	multiget := &caldav.CalendarMultiGet{
		Paths: paths,
		CompRequest: caldav.CalendarCompRequest{
			Name:     "VCALENDAR",
			AllProps: true,
			Comps: []caldav.CalendarCompRequest{{
				Name:     "VEVENT",
				AllProps: true,
			}},
		},
	}
	return c.client.MultiGetCalendar(ctx, c.calendarURL, multiget)
}

// Inspect prints a diagnostic view of the destination calendar: object count,
// raw component props on the first object, and ownership status of all objects.
func (c *Client) Inspect(logger *slog.Logger) error {
	if logger == nil {
		logger = c.logger
	}
	ctx := context.Background()

	paths, err := c.propfindObjectPaths(ctx)
	if err != nil {
		return fmt.Errorf("propfind: %w", err)
	}
	logger.Info("PROPFIND: total objects in calendar", "count", len(paths))

	if len(paths) == 0 {
		logger.Info("calendar is empty")
		return nil
	}

	objects, err := c.multiGetPaths(ctx, paths)
	if err != nil {
		return fmt.Errorf("multiget: %w", err)
	}
	logger.Info("multiget: returned objects", "count", len(objects))

	if len(objects) > 0 {
		obj := objects[0]
		logger.Info("first object", "path", obj.Path, "etag", obj.ETag, "component", obj.Data.Name, "children", len(obj.Data.Children))
		for _, child := range obj.Data.Children {
			logger.Info("  child component", "name", child.Name)
			for propName, props := range child.Props {
				for _, p := range props {
					logger.Info("    prop", "name", propName, "value", p.Value)
				}
			}
		}
	}

	owned := 0
	for _, obj := range objects {
		source, originUID, hash := c.extractSyncerMeta(obj)
		if source == "" {
			logger.Info("  NOT owned by calsyncer", "path", obj.Path)
		} else {
			owned++
			logger.Info("  owned by calsyncer", "path", obj.Path, "source", source, "originUID", originUID, "hash", hash)
		}
	}
	logger.Info("calsyncer-owned objects", "count", owned, "total", len(objects))
	return nil
}

// DeleteSynced deletes all calsyncer-managed events from the destination.
// Pass update=false for a dry run (logs what would be deleted without touching the server).
func (c *Client) DeleteSynced(update bool, singleCalendar string, logger *slog.Logger) error {
	if logger == nil {
		logger = c.logger
	}
	ctx := context.Background()

	existing, err := c.FetchSyncedObjects(ctx, singleCalendar)
	if err != nil {
		return fmt.Errorf("fetching synced objects: %w", err)
	}

	if len(existing) == 0 {
		logger.Info("no calsyncer-managed events found")
		return nil
	}

	logger.Info("found calsyncer-managed events", "count", len(existing))
	deleted := 0
	for _, obj := range existing {
		logger.Info("deleting event", "source", obj.SourceName, "originUID", obj.OriginUID, "path", obj.Path)
		if update {
			if err := c.DeleteCalendarObject(ctx, obj.Path); err != nil {
				logger.Error("failed to delete", "path", obj.Path, "err", err)
				continue
			}
		}
		deleted++
	}

	if update {
		logger.Info("delete complete", "deleted", deleted)
	} else {
		logger.Info("dry-run: would delete", "count", deleted)
	}
	return nil
}

// PutCalendarObject writes a CalDAV object (create or update).
func (c *Client) PutCalendarObject(ctx context.Context, path string, cal *ical.Calendar, etag string) error {
	if _, err := c.client.PutCalendarObject(ctx, path, cal); err != nil {
		return fmt.Errorf("PUT %s: %w", path, err)
	}
	return nil
}

// DeleteCalendarObject deletes a CalDAV object by path.
func (c *Client) DeleteCalendarObject(ctx context.Context, path string) error {
	if err := c.client.RemoveAll(ctx, path); err != nil {
		return fmt.Errorf("DELETE %s: %w", path, err)
	}
	return nil
}

// CalendarURL returns the path of the resolved destination calendar collection.
func (c *Client) CalendarURL() string {
	return c.calendarURL
}

type basicAuthTransport struct {
	username, password string
	base               http.RoundTripper
}

func (t *basicAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.SetBasicAuth(t.username, t.password)
	return t.base.RoundTrip(req)
}
