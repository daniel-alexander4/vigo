
# vigocli tui

Interactive terminal dashboard. Full-screen TUI with fleet overview, envoy list, runs, risk, and audit views. Auto-refreshes on a configurable interval.

## Usage

```bash
vigocli tui [--refresh <seconds>]
```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--refresh` | `10` | Auto-refresh interval in seconds (0 to disable) |

## Views

| Key | View | Description |
|-----|------|-------------|
| `1` | Dashboard | Fleet stats, convergence, risk, recent runs |
| `2` | Envoys | Searchable fleet list with status |
| `3` | Runs | Fleet-wide run history |
| `4` | Risk | Fleet risk summary with distribution bars |
| `5` | Audit | Scrollable event log |

Drill-down views open when pressing Enter on a list item: Envoy Detail (from Envoys), Run Detail (from Runs or Envoy Detail).

## Keyboard Shortcuts

| Key | Action |
|-----|--------|
| `1`-`5` | Switch to view |
| `Tab` / `Shift+Tab` | Cycle views |
| `j` / `k` or arrows | Scroll list |
| `Enter` | Open detail |
| `Esc` | Go back |
| `/` | Search/filter (envoys, runs, audit) |
| `r` | Refresh now |
| `?` | Toggle help overlay |
| `q` | Quit |

## Dashboard

The dashboard shows four stat cards (Fleet, Convergence, Risk, Runs) with a fleet status table and recent runs table below. Stat cards adapt to terminal width: 4 columns above 100 chars, 2 columns above 60, single column on narrow terminals. The risk card includes a sparkline of recent failure counts.

## Search

Press `/` in the Envoys, Runs, or Audit views to activate inline search. Type to filter, press Enter or Esc to stop editing. The filter matches hostnames, status, event types, and summaries.

## Example

```bash
vigocli tui                 # default 10s refresh
vigocli tui --refresh 30    # 30s refresh
vigocli tui --refresh 0     # manual refresh only
```
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
