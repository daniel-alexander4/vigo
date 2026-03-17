# Tamper-Evident Audit Trail

Hobnob maintains a tamper-evident audit trail using a SHA-256 hash chain. Every recorded event includes the hash of the previous entry, making modifications, deletions, or out-of-order insertions detectable by anyone with read access to the database.

## How It Works

Each audit entry stores:

| Field | Description |
|-------|-------------|
| `id` | Auto-incrementing row ID |
| `prev_hash` | SHA-256 hash of the previous entry (empty string for the first entry) |
| `hash` | SHA-256 hash of this entry |
| `timestamp` | UTC timestamp (second precision) |
| `event_type` | Category of the event |
| `actor` | Who triggered the event (username, "system", or "peer") |
| `summary` | Human-readable one-line description |
| `payload` | Optional JSON detail |

### Hash Computation

Each entry's hash is computed as:

```
SHA256(prev_hash || timestamp || event_type || actor || summary || payload)
```

The timestamp is formatted as RFC 3339 (e.g., `2026-03-16T14:30:00Z`) and truncated to second precision to avoid driver-dependent nanosecond differences.

Because each hash incorporates the previous entry's hash, modifying or deleting any row breaks the chain from that point forward. Inserting a row out of order is also detectable because the `prev_hash` linkage would not match.

## What Gets Logged

| Event Type | Trigger | Example Summary |
|------------|---------|-----------------|
| `config.reload` | Config publish or hot-reload | "config reloaded" |
| `config.approve` | Two-person approval accepted | "approved by admin2" |
| `config.reject` | Two-person approval rejected | "rejected by admin2" |
| `thrall.enroll` | New agent enrolls via bootstrap | "web1.example.com enrolled" |
| `thrall.revoke` | Admin revokes an agent | "web1.example.com revoked" |
| `task.dispatch` | Ad-hoc task dispatched | "uptime on *.web.*" |
| `peer.promote` | Peer promoted to primary | "peer2 promoted" |

The audit trail is always on. There is no configuration to disable it.

## Verifying the Chain

### CLI

```bash
# Verify the entire hash chain
hobnobcli audit verify
```

Output on success:

```
Chain valid (247 entries)
```

Output on failure:

```
CHAIN BROKEN at entry #142
hash mismatch at entry 142: expected a1b2c3d4e5f6... got 9f8e7d6c5b4a...
```

### REST API

```
GET /api/v1/audit/verify
```

Response (valid):

```json
{
  "valid": true,
  "entry_count": 247
}
```

Response (broken):

```json
{
  "valid": false,
  "entry_count": 247,
  "first_broken": 142,
  "error": "hash mismatch at entry 142: expected a1b2c3d4... got 9f8e7d6c..."
}
```

### Web UI

The Audit page (`/audit`) shows chain integrity at the top of the page:

- **Green badge**: "Chain valid (N entries)" -- all hashes check out
- **Red badge**: "CHAIN BROKEN at entry #N" -- the first mismatched entry ID

The status is computed on every page load. A "Verify Chain" button triggers an on-demand re-verification via the REST API.

## What a Broken Chain Means

A broken chain indicates that one or more entries in the `audit_entries` table have been modified, deleted, or inserted out of order after they were originally written. Possible causes:

1. **Direct database tampering** -- someone modified a row with raw SQL
2. **Database restore from backup** -- a partial restore replaced some entries but not others
3. **Bug in the audit writer** -- a code defect produced an incorrect hash (check the Hobnob version)

### What to Do

1. **Identify the break point.** Run `hobnobcli audit verify` to find the first broken entry ID.
2. **Inspect the entry.** Query the database directly: `SELECT * FROM audit_entries WHERE id = <N>`.
3. **Check surrounding entries.** Compare the `prev_hash` of entry N with the `hash` of entry N-1.
4. **Review access logs.** Determine who had write access to the database around the entry's timestamp.
5. **Document the finding.** For compliance purposes, record the investigation and outcome.

The chain before the break point remains valid. Entries after the break point may or may not have been tampered with -- verification cannot distinguish between "the tampered entry" and "entries that inherited a bad prev_hash."

## Retention

Audit entries are stored in the `audit_entries` table alongside all other data in the SQLite (or PostgreSQL) database. They follow the same retention policy as the rest of the database:

- **SQLite**: entries persist until the database file is deleted or the table is manually truncated
- **PostgreSQL**: entries persist according to normal table lifecycle

For compliance frameworks that require specific retention periods (e.g., PCI-DSS 10.7 requires 1 year, SOX requires 7 years), ensure your backup strategy covers the required duration. The `backup` section in `server.yaml` configures Litestream WAL replication for continuous backup.

If you need to prune old entries while preserving chain integrity, export the entries first, then truncate. The next entry written will start a new chain (with an empty `prev_hash`). Document the truncation event for auditors.

## Relationship to the Audit Log

The [Audit Log](audit.md) page provides a unified view of all auditable events (config publishes, task runs, workflow runs). The tamper-evident trail is a subset of these events -- specifically, the security-sensitive operations that are hash-chained in the `audit_entries` table. The unified audit view combines hash-chained entries with non-chained operational records (task runs, workflow runs) from their respective tables.
