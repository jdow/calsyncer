# calsyncer

A Go tool that reads multiple iCal subscription URLs (`.ics` feeds) and syncs them into a single shared CalDAV calendar. Designed to run as a scheduled job on a home server or NAS.

**Use case:** You have several calendars (work, on-call, kids' sports, etc.) that family members would have to subscribe to directly, in order to see the entire family schedule. calsyncer copies them all into one shared family calendar — with optional emoji prefixes so everyone knows what's what. It is designed to run regularly and keep things up to date.

## How it works

On each run, calsyncer:

1. Connects to the destination CalDAV calendar
2. Fetches each configured iCal feed in full
3. Applies any configured transforms (skip events, rename them, filter by title)
4. Splits multi-day timed events into per-day segments (see below)
5. Diffs each event against what was previously synced, using a content hash
6. Creates, updates, or deletes destination objects to match the source
7. Logs a summary of what changed

calsyncer **only ever touches events it previously created**. Manually created events on the destination calendar are never modified or deleted.

## Multi-day event splitting

When a non-recurring timed event spans more than one calendar day, calsyncer automatically splits it so the destination calendar is easy to read at a glance:

- **First day:** timed event from the original start time until midnight
- **Middle days (if any):** all-day events
- **Last day:** timed event from midnight until the original end time

For example, a work trip from Monday 9 AM to Wednesday 5 PM becomes three events: `Mon 9:00–midnight`, `Tue (all day)`, `Wed midnight–17:00`.

Recurring events and events that are already all-day (`VALUE=DATE`) are never split.

## Recurring events

Recurring events are synced as complete series. The parent VEVENT (with its `RRULE`, `EXDATE`, etc.) and all exception VEVENTs are written as a single CalDAV object. This means:

- An infinitely recurring weekly standup syncs as one object, not hundreds of expanded copies
- If someone moves a single occurrence, that `RECURRENCE-ID` exception is detected and synced
- If the recurrence rule itself changes, the whole object is re-written

### Pre-expanded feeds (Google free/busy)

Some feeds — particularly Google Calendar public/free-busy exports — don't emit a parent VEVENT with an `RRULE`. Instead every occurrence is emitted as a separate VEVENT with a `RECURRENCE-ID` but no parent. calsyncer detects this and treats each occurrence as an independent event, keyed by `UID + DTSTART`.

## Change detection

Each synced CalDAV object carries three custom iCal properties on its parent VEVENT:

| Property | Purpose |
| --- | --- |
| `X-CALSYNCER-SRC` | `name` from config (e.g. `"oncall"`) — ownership tag |
| `X-CALSYNCER-ORIGIN-UID` | UID (and DTSTART for flat events) from the source feed |
| `X-CALSYNCER-HASH` | SHA-256 of all relevant fields; changes trigger a re-sync |

If an event's hash is unchanged from the last run, it is skipped entirely — no CalDAV write occurs.

## Configuration

Copy `config.json.example` → `config.json` and fill in your values:

```json
{
  "destination": {
    "url": "https://caldav.icloud.com",
    "username": "you@icloud.com",
    "password": "xxxx-xxxx-xxxx-xxxx",
    "calendarName": "Family Calendar"
  },
  "sources": [
    {
      "name": "oncall",
      "type": "ical",
      "url": "https://your-pagerduty-ical-url.ics",
      "emoji": "🚨"
    }
  ]
}
```

### Destination

| Field | Description |
| --- | --- |
| `url` | Base URL of the CalDAV server |
| `username` | CalDAV username |
| `password` | CalDAV password (use an app-specific password for iCloud) |
| `calendarName` | Display name of the calendar to write into (case-insensitive) |

#### iCloud CalDAV

- URL: `https://caldav.icloud.com`
- Username: your Apple ID email
- Password: an [app-specific password](https://support.apple.com/en-us/102654) — **not** your main Apple ID password

#### Google CalDAV

- URL: `https://apidata.googleusercontent.com/caldav/v2`
- Username: your Google account email
- Password: an [app password](https://support.google.com/accounts/answer/185833) (requires 2FA to be enabled)

#### Nextcloud / Radicale / Fastmail

Use the CalDAV URL from your server's settings. For Nextcloud it is typically `https://your-nextcloud.example.com/remote.php/dav`.

### Sources

Each source is an iCal subscription URL. All sources sync into the same destination calendar.

| Field | Description |
| --- | --- |
| `name` | Short stable identifier (e.g. `"oncall"`). Written to every synced event. **Do not rename after first sync** — see below. |
| `type` | Must be `"ical"` |
| `url` | The `.ics` subscription URL |
| `emoji` | Optional prefix prepended to every event title (e.g. `"🔴"`) |
| `transforms` | Optional list of transform rules applied in order (see below) |

### Transforms

Transforms let you filter or rename events from a source before they reach the destination.

```json
"transforms": [
  {
    "whenSummary": "Exact Title Match",
    "skip": true
  },
  {
    "whenSummaryContains": "standup",
    "skip": true
  },
  {
    "whenSummaryContains": "busy",
    "setSummary": "Busy"
  }
]
```

| Field | Description |
| --- | --- |
| `whenSummary` | Only apply if the event title exactly matches this string |
| `whenSummaryContains` | Only apply if the event title contains this substring (case-insensitive) |
| `skip` | Drop the event entirely — it won't be synced |
| `setSummary` | Replace the event title with this string |

Both `whenSummary` and `whenSummaryContains` are optional match conditions. If neither is set, the transform applies to every event from that source. Multiple transforms are applied in order — the first matching `skip: true` wins.

### Source name stability

The `name` field is baked into every synced event as its ownership tag. **Do not rename a source** after the first sync — calsyncer won't be able to match old events and will create duplicates. If you must rename, run once with an empty or unreachable feed URL for that source (to delete all its events), then rename.

## CLI flags

```text
-config string     Path to JSON config file (default "config.json")
-update            Make changes. Without this flag, no changes are written (dry-run mode).
-verbose           Enable debug logging
-calendar string   Only operate on one source by name (useful for testing a single feed)
-inspect           Show what calsyncer can see in the destination calendar, then exit
-delete            Delete all events previously synced by calsyncer. Use with -update.
```

**Default is dry-run.** Nothing is written to CalDAV unless `-update` is passed.

## Running with Docker

```bash
docker run --rm \
  -v /path/to/your/config.json:/config/config.json:ro \
  ghcr.io/jdow/calsyncer:latest \
  -config /config/config.json -update
```

For scheduled runs on a Synology or any Docker host, mount your config read-only and add `-update` to actually write changes.

## Building from source

```bash
git clone https://github.com/jdow/calsyncer
cd calsyncer/cmd
go build -o calsyncer .
./calsyncer -config config.json -update
```

## Cron / systemd

### crontab (every 15 minutes)

```crontab
*/15 * * * * docker run --rm -v /volume1/docker/calsyncer/config.json:/config/config.json:ro ghcr.io/jdow/calsyncer:latest -config /config/config.json -update >> /var/log/calsyncer.log 2>&1
```

### systemd timer

`/etc/systemd/system/calsyncer.service`:

```ini
[Unit]
Description=Calendar Syncer
After=network-online.target

[Service]
Type=oneshot
ExecStart=docker run --rm \
  -v /path/to/config.json:/config/config.json:ro \
  ghcr.io/jdow/calsyncer:latest \
  -config /config/config.json -update
```

`/etc/systemd/system/calsyncer.timer`:

```ini
[Unit]
Description=Run calsyncer every 15 minutes

[Timer]
OnBootSec=2min
OnUnitActiveSec=15min

[Install]
WantedBy=timers.target
```

```bash
sudo systemctl enable --now calsyncer.timer
sudo journalctl -u calsyncer.service -f
```

## Synology Task Scheduler

In DSM: **Control Panel → Task Scheduler → Create → Scheduled Task → User-defined script**

- Schedule: every 15 or 30 minutes
- Script:

  ```bash
  docker run --rm \
    -v /volume1/docker/calsyncer/config.json:/config/config.json:ro \
    ghcr.io/jdow/calsyncer:latest \
    -config /config/config.json -update
  ```
