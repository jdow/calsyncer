// Package syncer fetches source events, diffs them against the destination, and
// creates, updates, or deletes CalDAV objects to bring the destination in sync.
package syncer

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	goical "github.com/emersion/go-ical"
	"github.com/jdow/calsyncer/internal/caldav"
	"github.com/jdow/calsyncer/internal/config"
	"github.com/jdow/calsyncer/internal/ical"
)

// CalDAVClient is the interface the syncer uses to read from and write to the destination calendar.
type CalDAVClient interface {
	CalendarURL() string
	FetchSyncedObjects(ctx context.Context, sourceName string) (map[string]*caldav.SyncedObject, error)
	PutCalendarObject(ctx context.Context, objPath string, cal *goical.Calendar, etag string) error
	DeleteCalendarObject(ctx context.Context, objPath string) error
}

// ICalFetcher is the interface the syncer uses to fetch events from source feeds.
type ICalFetcher interface {
	FetchEventGroups(url string) (map[string]*ical.EventGroup, error)
}

type sourceStats struct {
	Fetched        int
	Created        int
	Updated        int
	Skipped        int // hash matched — no write needed
	Deleted        int
	TransformSkip  int // event dropped by a skip transform
	TransformEdit  int // event summary rewritten by a transform
	AllDayBusySkip int // all-day "Busy" placeholder dropped by ignoreAllDayBusy
}

// Syncer runs the sync for all configured sources.
type Syncer struct {
	cfg            *config.Config
	singleCalendar string
	dest           CalDAVClient
	fetcher        ICalFetcher
	logger         *slog.Logger
	dryRun         bool

	// Totals across all sources, accumulated after each source completes.
	Deleted        int
	Updated        int
	Created        int
	Skipped        int
	TransformSkip  int
	TransformEdit  int
	AllDayBusySkip int
}

// Option is a functional option for Syncer.
type Option func(*Syncer)

// WithUpdate enables writes. Without this, the syncer logs what it would do but makes no changes.
func WithUpdate(update bool) Option {
	return func(s *Syncer) {
		s.dryRun = !update
	}
}

// WithConfig sets the configuration.
func WithConfig(cfg *config.Config) Option {
	return func(s *Syncer) {
		s.cfg = cfg
	}
}

// WithLogger sets the logger.
func WithLogger(logger *slog.Logger) Option {
	return func(s *Syncer) {
		s.logger = logger
	}
}

// WithCalDAVClient sets the CalDAV destination.
func WithCalDAVClient(dest CalDAVClient) Option {
	return func(s *Syncer) {
		s.dest = dest
	}
}

// WithICalFetcher sets the iCal feed fetcher.
func WithICalFetcher(fetcher ICalFetcher) Option {
	return func(s *Syncer) {
		s.fetcher = fetcher
	}
}

// WithSingleCalendar restricts the run to a single named source.
func WithSingleCalendar(cal string) Option {
	return func(s *Syncer) {
		s.singleCalendar = cal
	}
}

// NewSyncer constructs a Syncer from the given options.
func NewSyncer(opts ...Option) (*Syncer, error) {
	s := &Syncer{}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// Run processes all configured sources in order.
func (s *Syncer) Run() error {
	ctx := context.Background()

	for _, src := range s.cfg.Sources {
		if s.singleCalendar != "" && src.Name != s.singleCalendar {
			continue
		}
		if err := s.syncSource(ctx, src); err != nil {
			s.logger.Error("failed to sync source", "source", src.Name, "err", err)
		}
	}

	s.logger.Info("sync finished",
		"created", s.Created,
		"updated", s.Updated,
		"deleted", s.Deleted,
		"skipped", s.Skipped,
		"transform_skipped", s.TransformSkip,
		"transform_edited", s.TransformEdit,
		"all_day_busy_skipped", s.AllDayBusySkip,
	)
	return nil
}

func (s *Syncer) syncSource(ctx context.Context, src config.SourceConfig) error {
	logger := s.logger.With("source", src.Name)
	logger.Info("fetching source", "url", src.URL)

	groups, err := s.fetcher.FetchEventGroups(src.URL)
	if err != nil {
		return fmt.Errorf("fetching source: %w", err)
	}

	var stats sourceStats
	stats.Fetched = len(groups)
	logger.Info("fetched event groups", "count", stats.Fetched)

	loc, err := s.cfg.Location()
	if err != nil {
		return fmt.Errorf("resolving timezone: %w", err)
	}

	// Apply transforms, then split multi-day events into per-day segments.
	type keptGroup struct {
		key      string
		group    *ical.EventGroup
		modified bool
	}
	var kept []keptGroup

	for _, group := range groups {
		if src.IgnoreAllDayBusy && ical.IsAllDayBusy(group) {
			stats.AllDayBusySkip++
			continue
		}

		transformed, skip, modified := applyTransforms(group, src.Transforms)
		if skip {
			stats.TransformSkip++
			continue
		}

		splitGroups := ical.SplitMultiDayGroup(transformed, loc)
		for _, sg := range splitGroups {
			kept = append(kept, keptGroup{
				key:      src.Name + "|" + sg.Key,
				group:    sg,
				modified: modified,
			})
		}
	}

	existing, err := s.dest.FetchSyncedObjects(ctx, src.Name)
	if err != nil {
		return fmt.Errorf("fetching existing synced objects: %w", err)
	}
	logger.Info("existing synced objects", "count", len(existing))

	seen := make(map[string]bool)

	for _, kg := range kept {
		if kg.modified {
			stats.TransformEdit++
		}

		seen[kg.key] = true

		hash := ical.HashGroup(kg.group)
		obj, exists := existing[kg.key]

		if exists && obj.Hash == hash {
			stats.Skipped++
			continue
		}

		destCal, objPath := BuildDestObject(src, kg.group, hash, obj, s.dest.CalendarURL())

		if exists {
			logger.Info("updating event group",
				"summary", kg.group.Summary(),
				"uid", kg.group.UID,
				"exceptions", len(kg.group.Exceptions),
			)
			if !s.dryRun {
				if err := s.dest.PutCalendarObject(ctx, objPath, destCal, obj.ETag); err != nil {
					logger.Error("failed to update object", "err", err, "path", objPath)
				}
			}
			stats.Updated++
		} else {
			logger.Info("creating event group",
				"summary", kg.group.Summary(),
				"uid", kg.group.UID,
				"exceptions", len(kg.group.Exceptions),
			)
			if !s.dryRun {
				if err := s.dest.PutCalendarObject(ctx, objPath, destCal, ""); err != nil {
					logger.Error("failed to create object", "err", err, "path", objPath)
				}
			}
			stats.Created++
		}
	}

	// Delete destination objects that are no longer in the source feed.
	for key, obj := range existing {
		if seen[key] {
			continue
		}
		logger.Info("deleting removed event",
			"originUID", obj.OriginUID,
			"path", obj.Path,
		)
		if !s.dryRun {
			if err := s.dest.DeleteCalendarObject(ctx, obj.Path); err != nil {
				logger.Error("failed to delete object", "path", obj.Path, "err", err)
			}
		}
		stats.Deleted++
	}

	logger.Info("source sync complete",
		"fetched", stats.Fetched,
		"created", stats.Created,
		"updated", stats.Updated,
		"deleted", stats.Deleted,
		"skipped", stats.Skipped,
		"transform_skipped", stats.TransformSkip,
		"transform_edited", stats.TransformEdit,
		"all_day_busy_skipped", stats.AllDayBusySkip,
	)

	s.Created += stats.Created
	s.Updated += stats.Updated
	s.Deleted += stats.Deleted
	s.Skipped += stats.Skipped
	s.TransformSkip += stats.TransformSkip
	s.TransformEdit += stats.TransformEdit
	s.AllDayBusySkip += stats.AllDayBusySkip

	return nil
}

// applyTransforms runs the source's transforms against group in order.
// Returns (nil, true, false) if the event should be dropped, or (group, false, modified) otherwise.
func applyTransforms(group *ical.EventGroup, transforms []config.TransformConfig) (result *ical.EventGroup, skip bool, modified bool) {
	summary := ical.PropValue(group.Parent, goical.PropSummary)
	for _, t := range transforms {
		if t.WhenSummary != "" && t.WhenSummary != summary {
			continue
		}
		if t.WhenSummaryContains != "" && !strings.Contains(strings.ToLower(summary), strings.ToLower(t.WhenSummaryContains)) {
			continue
		}
		if t.Skip {
			return nil, true, false
		}
		if t.SetSummary != "" {
			p := goical.NewProp(goical.PropSummary)
			p.Value = t.SetSummary
			group.Parent.Props[goical.PropSummary] = []goical.Prop{*p}
			summary = t.SetSummary
			modified = true
		}
	}
	return group, false, modified
}
