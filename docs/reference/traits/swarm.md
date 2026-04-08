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
| `chunk_sources` | map[string]map[string]int | Per-blob source breakdown: `{sha256: {source_address: chunk_count}}` |
| `chunk_details` | map[string]object[] | Per-blob, per-chunk download detail: `{sha256: [{chunk, source, ms}]}` |
| `download_ms` | map[string]int | Per-blob wall-clock download duration in milliseconds: `{sha256: ms}` |

`chunk_sources`, `chunk_details`, and `download_ms` are only present for blobs that have source data recorded (written after successful download).

## Example

```json
{
  "blob_count": 2,
  "blobs": ["a1b2c3...", "d4e5f6..."],
  "chunk_sources": {
    "a1b2c3...": {"10.0.1.2:1531": 3, "server": 2}
  },
  "chunk_details": {
    "a1b2c3...": [
      {"chunk": 0, "source": "10.0.1.2:1531", "ms": 45},
      {"chunk": 1, "source": "server", "ms": 120}
    ]
  },
  "download_ms": {
    "a1b2c3...": 850
  }
}
```

## Collection

The agent scans its blob cache directory (`.blobs/`) for complete blobs (64-character hex filenames). For each blob with a `.sources` sidecar file, it reads the chunk source counts, per-chunk timing, and total download duration.

## Notes

- `download_ms` is the agent-measured wall-clock time for the entire download. For P2P downloads with parallel chunk fetches, this is shorter than the sum of per-chunk durations.
- Agents prior to 0.18.18 do not report `download_ms`. The server falls back to summing per-chunk durations from `chunk_details`, which overstates parallel downloads but is better than showing nothing.
- The server uses `download_ms` to display distribution time on the swarm detail page.
