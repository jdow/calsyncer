package config_test

import (
	"os"
	"testing"

	"github.com/jdow/calsyncer/internal/config"
)

func writeConfigFile(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "config-*.json")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("writing temp file: %v", err)
	}
	_ = f.Close()
	return f.Name()
}

func TestLoadConfig_Valid(t *testing.T) {
	path := writeConfigFile(t, `{
		"destination": {
			"url": "https://caldav.example.com",
			"username": "user",
			"password": "pass",
			"calendarName": "MyCalendar"
		},
		"sources": [
			{"name": "work", "type": "ical", "url": "https://example.com/work.ics"}
		]
	}`)

	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Destination.URL != "https://caldav.example.com" {
		t.Errorf("destination URL: got %q", cfg.Destination.URL)
	}
	if len(cfg.Sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(cfg.Sources))
	}
	if cfg.Sources[0].Name != "work" {
		t.Errorf("source name: got %q", cfg.Sources[0].Name)
	}
}

func TestLoadConfig_FileNotFound(t *testing.T) {
	_, err := config.LoadConfig("/nonexistent/path/config.json")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadConfig_InvalidJSON(t *testing.T) {
	path := writeConfigFile(t, `not json`)
	_, err := config.LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestValidate_MissingDestURL(t *testing.T) {
	path := writeConfigFile(t, `{
		"destination": {"username":"u","password":"p","calendarName":"c"},
		"sources": [{"name":"s","type":"ical","url":"http://x"}]
	}`)
	_, err := config.LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for missing destination.url")
	}
}

func TestValidate_MissingDestUsername(t *testing.T) {
	path := writeConfigFile(t, `{
		"destination": {"url":"http://x","password":"p","calendarName":"c"},
		"sources": [{"name":"s","type":"ical","url":"http://x"}]
	}`)
	_, err := config.LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for missing destination.username")
	}
}

func TestValidate_MissingDestPassword(t *testing.T) {
	path := writeConfigFile(t, `{
		"destination": {"url":"http://x","username":"u","calendarName":"c"},
		"sources": [{"name":"s","type":"ical","url":"http://x"}]
	}`)
	_, err := config.LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for missing destination.password")
	}
}

func TestValidate_MissingDestCalendarName(t *testing.T) {
	path := writeConfigFile(t, `{
		"destination": {"url":"http://x","username":"u","password":"p"},
		"sources": [{"name":"s","type":"ical","url":"http://x"}]
	}`)
	_, err := config.LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for missing destination.calendarName")
	}
}

func TestValidate_MissingSourceName(t *testing.T) {
	path := writeConfigFile(t, `{
		"destination": {"url":"http://x","username":"u","password":"p","calendarName":"c"},
		"sources": [{"type":"ical","url":"http://x"}]
	}`)
	_, err := config.LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for missing source name")
	}
}

func TestValidate_DuplicateSourceName(t *testing.T) {
	path := writeConfigFile(t, `{
		"destination": {"url":"http://x","username":"u","password":"p","calendarName":"c"},
		"sources": [
			{"name":"dup","type":"ical","url":"http://x"},
			{"name":"dup","type":"ical","url":"http://y"}
		]
	}`)
	_, err := config.LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for duplicate source name")
	}
}

func TestValidate_UnsupportedSourceType(t *testing.T) {
	path := writeConfigFile(t, `{
		"destination": {"url":"http://x","username":"u","password":"p","calendarName":"c"},
		"sources": [{"name":"s","type":"caldav","url":"http://x"}]
	}`)
	_, err := config.LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for unsupported source type")
	}
}

func TestValidate_MissingSourceURL(t *testing.T) {
	path := writeConfigFile(t, `{
		"destination": {"url":"http://x","username":"u","password":"p","calendarName":"c"},
		"sources": [{"name":"s","type":"ical"}]
	}`)
	_, err := config.LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for missing source URL")
	}
}
