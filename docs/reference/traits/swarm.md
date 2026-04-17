---
title: swarm
---

# swarm

Reports cached swarm blobs and download metadata. The server uses this trait to track distribution progress across the fleet.

## Fields

| Field | Type | Description |
|---|---|---|
| `blob_count` | int | Number of complete blobs in the local cache |
| `blobs` | string[] | SHA256 hashes of cached blobs |
| `chunk_sources` | map[string]map[string]int | Per-blob source breakdown: `{sha256: {source_address: chunk_count}}`. The synthetic source `"cached"` accounts for chunks that were already on disk when the download run started (e.g. on a partial reseed). |
| `chunk_details` | map[string]object[] | Per-blob, per-chunk download detail: `{sha256: [{chunk, source, ms}]}`. Pre-cached chunks appear with `source: "cached"` and `ms: 0`. |
| `active_download_ms` | map[string]int | Per-blob scheduler wall-clock duration in milliseconds: `{sha256: ms}`. Measures the active download run only — chunks already cached before the run started are not counted. |
| `downloading` | map[string]int | In-progress downloads: `{sha256: chunks_have}`. Only present when the agent has active partial downloads. |

`chunk_sources`, `chunk_details`, and `active_download_ms` are present for any blob that has a `.sources` sidecar on disk. The scheduler writes this sidecar incrementally during a download (every 32 chunks) and again on completion, so in-progress blobs are reported with partial source attribution. `downloading` is only present when the agent has active partial downloads — it disappears once all downloads complete.

## Example

```json
{
  "blob_count": 2,
  "blobs": ["a1b2c3...", "d4e5f6..."],
  "chunk_sources": {
    "a1b2c3...": {"10.0.1.2:1531": 3, "server": 2, "cached": 7365}
  },
  "chunk_details": {
    "a1b2c3...": [
      {"chunk": 0, "source": "cached", "ms": 0},
      {"chunk": 7365, "source": "10.0.1.2:1531", "ms": 45},
      {"chunk": 7366, "source": "server", "ms": 120}
    ]
  },
  "active_download_ms": {
    "a1b2c3...": 850
  },
  "downloading": {
    "f7a8b9...": 1562
  }
}
```

## Collection

The agent scans its blob cache directory (`.blobs/`) for complete blobs (64-character hex filenames that resolve to real files — dangling symlinks are skipped) and for `.partial` directories representing in-progress downloads. For every blob in either set that has a `.sources` sidecar, it reads the chunk source counts, per-chunk timing, and active download duration. The sidecar is written atomically (`tmp` → `rename`) during a download every 32 chunks and again on completion, so reports for in-progress blobs track current progress.

## Notes

- `active_download_ms` is the scheduler's wall-clock time for the current download run. It excludes any time spent before the run started, including chunks that were already cached from a previous partial run. A reseed where most of the blob was already on disk will show a very small `active_download_ms` even if the original download took much longer.
- The synthetic source `"cached"` has no corresponding peer — it tells downstream consumers that those chunks predate the current run. The web UI renders it with a distinct color in the chunk-sources strip and skips it when building the chunk-flow matrix.
- Agents that pre-date `active_download_ms` reporting fall back to summing per-chunk durations from `chunk_details`, which overstates parallel downloads but is better than showing nothing.
- The server uses `active_download_ms` to display distribution time on the swarm detail page.
