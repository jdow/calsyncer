package main

import (
	"flag"
	"log/slog"
	"os"

	"github.com/jdow/calsyncer/internal/caldav"
	"github.com/jdow/calsyncer/internal/config"
	"github.com/jdow/calsyncer/internal/ical"
	"github.com/jdow/calsyncer/internal/syncer"
)

func main() {
	configPath := flag.String("config", "config.json", "Path to JSON config file")
	update := flag.Bool("update", false, "Make changes. Without this, no changes will be made as default is dry-run mode.")
	verbose := flag.Bool("verbose", false, "Enable verbose logging")
	deleteAll := flag.Bool("delete", false, "Delete all events previously synced by calsyncer. WARNING: This is irreversible and will delete events from the destination calendar. Use with --update.")
	inspect := flag.Bool("inspect", false, "Inspect destination calendar and show what calsyncer can see.")
	calendar := flag.String("calendar", "", "only operate on a specific calendar from the config. use the calendar name as defined in the config file")
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	cfg, configErr := config.LoadConfig(*configPath)
	if configErr != nil {
		logger.Error("loading config", "err", configErr)
		os.Exit(1)
	}

	caldavClient, clientErr := caldav.NewCalDAVClient(cfg.Destination, caldav.WithLogger(logger))
	if clientErr != nil {
		logger.Error("connecting to CalDAV destination", "err", clientErr)
		os.Exit(1)
	}
	if *inspect {
		if err := caldavClient.Inspect(logger); err != nil {
			logger.Error("inspecting CalDAV destination", "err", err)
			os.Exit(1)
		}
		return
	}

	if *deleteAll {
		if err := caldavClient.DeleteSynced(*update, *calendar, logger); err != nil {
			logger.Error("deleting synced events", "err", err)
			os.Exit(1)
		}
		return
	}

	icalFetcher := ical.NewHTTPFetcher(
		ical.WithLogger(logger),
	)

	syncer, err := syncer.NewSyncer(
		syncer.WithCalDAVClient(caldavClient),
		syncer.WithICalFetcher(icalFetcher),
		syncer.WithConfig(cfg),
		syncer.WithLogger(logger),
		syncer.WithUpdate(*update),
		syncer.WithSingleCalendar(*calendar),
	)
	if err != nil {
		logger.Error("creating syncer", "err", err)
		os.Exit(1)
	}

	if err := syncer.Run(); err != nil {
		logger.Error("running syncer", "err", err)
		os.Exit(1)
	}

	logger.Info("Sync complete.")
}
