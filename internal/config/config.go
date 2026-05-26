// Package config loads and validates the JSON configuration file.
package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// Config is the top-level structure of config.json.
type Config struct {
	Destination DestinationConfig `json:"destination"`
	Sources     []SourceConfig    `json:"sources"`
}

// DestinationConfig is the CalDAV calendar that events are written into.
type DestinationConfig struct {
	URL          string `json:"url"`
	Username     string `json:"username"`
	Password     string `json:"password"`
	CalendarName string `json:"calendarName"`
}

// SourceConfig describes a single iCal feed to sync.
type SourceConfig struct {
	// Name is a short stable identifier (e.g. "oncall"). It is stored on every
	// synced event as an ownership tag. Do not rename after the first sync.
	Name string `json:"name"`

	// Type must be "ical".
	Type string `json:"type"`

	// URL is the .ics subscription URL.
	URL string `json:"url"`

	// Emoji is an optional prefix prepended to every event title (e.g. "🔴").
	Emoji string `json:"emoji,omitempty"`

	// Transforms are applied in order before events are written to the destination.
	Transforms []TransformConfig `json:"transforms,omitempty"`
}

// TransformConfig is a rule applied to events from a source before syncing.
// whenSummary and whenSummaryContains are optional match conditions; if neither
// is set the rule applies to every event. Rules are evaluated in order.
type TransformConfig struct {
	WhenSummary         string `json:"whenSummary,omitempty"`         // exact title match
	WhenSummaryContains string `json:"whenSummaryContains,omitempty"` // case-insensitive substring match
	SetSummary          string `json:"setSummary,omitempty"`          // replace the event title
	Skip                bool   `json:"skip,omitempty"`                // drop the event entirely
}

// LoadConfig reads, parses, and validates the JSON config file at path.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &cfg, nil
}

func (c *Config) validate() error {
	d := c.Destination
	if d.URL == "" {
		return fmt.Errorf("destination.url is required")
	}
	if d.Username == "" {
		return fmt.Errorf("destination.username is required")
	}
	if d.Password == "" {
		return fmt.Errorf("destination.password is required")
	}
	if d.CalendarName == "" {
		return fmt.Errorf("destination.calendarName is required")
	}

	names := make(map[string]bool)
	for i, s := range c.Sources {
		if s.Name == "" {
			return fmt.Errorf("sources[%d].name is required", i)
		}
		if names[s.Name] {
			return fmt.Errorf("sources[%d].name %q is duplicated", i, s.Name)
		}
		names[s.Name] = true

		if s.Type != "ical" {
			return fmt.Errorf("sources[%d] (%s): unsupported type %q (only \"ical\" is supported)", i, s.Name, s.Type)
		}
		if s.URL == "" {
			return fmt.Errorf("sources[%d] (%s): url is required", i, s.Name)
		}
	}
	return nil
}
