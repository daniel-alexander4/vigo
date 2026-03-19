# Vigo License Manager

Vendor-side tool for generating, signing, and tracking Vigo license files. Licenses are air-gapped, cryptographically signed with Ed25519, and verified locally by the Vigo server at runtime. No phone-home, no internet dependency.

## Directory Layout

```
/srv/vigo/
├── license-keys/
│   ├── vigo_private.key    # Ed25519 private key (immutable, never distribute)
│   ├── vigo_public.key     # Ed25519 public key (embedded in vigo binary)
│   └── licenses.db           # SQLite database tracking all issued licenses
└── README.md
```

## Quick Start

### 1. Install

From the vigo repo:

```bash
./scripts/install-license-manager.sh
```

This builds the binary, copies it to `/usr/local/sbin/vigo-license-manager`, and creates `/srv/vigo/license-keys/`.

### 2. Generate Keys (One-Time)

```bash
vigo-license-manager keys generate
```

Creates the Ed25519 keypair in `/srv/vigo/license-keys/`. Files are set to read-only and made immutable with `chattr +i`.

### 3. Inject Public Key into Source

```bash
vigo-license-manager keys inject
```

Writes the public key into `server/license/pubkey.go` in the vigo repo. Rebuild vigo after this step so the binary can verify license signatures.

### 4. Create a License

```bash
vigo-license-manager license create \
  --customer="Acme Corp" \
  --customer-id="acme-001" \
  --max-envoys=500 \
  --expires="2027-06-01" \
  --out=/tmp/acme-corp.lic
```

### 5. Install the License

Place any `.lic` file in `/srv/vigo/license/` on the server host:

```bash
sudo mkdir -p -m 700 /srv/vigo/license
sudo cp acme-corp.lic /srv/vigo/license/
sudo chmod 600 /srv/vigo/license/acme-corp.lic
```

On startup, `vigosrv` scans the directory for `.lic` files (lexicographic order) and uses the first one that parses and passes signature verification. The filename does not matter — any valid `.lic` file will work.

The Docker container mounts `/srv/vigo` from the host, so the server finds the license automatically. At least one valid `.lic` file must be present before starting the container.

The entrypoint enforces `700` on the license directory and `600` on all `.lic` files on every start.

If the server is already running, you can install without restarting:

```bash
vigogo license install --file=acme-corp.lic
```

This copies the file into place and sends SIGHUP to `vigosrv` to reload it.

---

## Commands

### Key Management

#### `keys generate`

Generate a new Ed25519 keypair. Saves to `/srv/vigo/license-keys/`. Refuses to overwrite existing keys — use `keys rotate` instead.

```bash
vigo-license-manager keys generate
```

#### `keys show-public`

Print the public key in hex format.

```bash
vigo-license-manager keys show-public
```

#### `keys inject`

Write the public key into the vigo source code at `server/license/pubkey.go`. Defaults to looking for the repo at `/home/dan/repos/vigo`, then auto-detects by walking up from the current directory. Override with `--repo=<path>`.

```bash
vigo-license-manager keys inject
vigo-license-manager keys inject --repo=/path/to/vigo
```

#### `keys rotate`

Replace the keypair. Removes the immutable flag on old keys, generates new ones, and re-applies immutability. All existing licenses become invalid and must be re-issued. Run `keys inject` afterward and rebuild.

```bash
vigo-license-manager keys rotate
```

### License Lifecycle

#### `license create`

Generate and sign a new license file. Records the license and customer in the local database.

```bash
vigo-license-manager license create \
  --customer="Acme Corp" \
  --customer-id="acme-001" \
  --max-envoys=500 \
  --expires="2027-06-01" \
  --out=acme-corp.lic
```

| Flag | Description |
|------|-------------|
| `--customer` | Customer display name (required) |
| `--customer-id` | Unique customer identifier (required) |
| `--max-envoys` | Maximum number of managed machines (required) |
| `--expires` | License expiry date, YYYY-MM-DD (required) |
| `--fingerprint` | Machine fingerprint to bind the license to (optional — omit for unbound) |
| `--out` | Output file path (required) |

#### Machine-Bound Licenses

Paid licenses can be bound to a specific server via the `--fingerprint` flag. The customer provides their machine fingerprint, and the license will only be valid on that machine.

```bash
# Customer sends their fingerprint:
vigogo license fingerprint
# → a1b2c3d4e5f6...

# You create a bound license:
vigo-license-manager license create \
  --fingerprint a1b2c3d4e5f6... \
  --customer="Acme Corp" \
  --customer-id="acme-001" \
  --max-envoys=500 \
  --expires="2027-06-01" \
  --out=acme-corp.lic
```

If the customer migrates to new hardware or regenerates their CA certificate, the fingerprint changes. They must run `vigogo license fingerprint` again and request a new license. The old license enters the grace period (15 days) on the new machine.

#### `license renew`

Issue a new license with an updated expiry date. The original license is marked as superseded in the database. The customer installs the new file.

```bash
vigo-license-manager license renew \
  --license-id="lic-20260310-acme-001" \
  --expires="2028-06-01" \
  --out=acme-renewed.lic
```

| Flag | Description |
|------|-------------|
| `--license-id` | ID of the license to renew (required) |
| `--expires` | New expiry date, YYYY-MM-DD (required) |
| `--out` | Output file path (required) |

#### `license update-envoys`

Issue a new license with an updated seat count. Same expiry as the original. The original is marked as superseded.

```bash
vigo-license-manager license update-envoys \
  --license-id="lic-20260310-acme-001" \
  --max-envoys=1000 \
  --out=acme-expanded.lic
```

| Flag | Description |
|------|-------------|
| `--license-id` | ID of the license to update (required) |
| `--max-envoys` | New maximum envoy count (required) |
| `--out` | Output file path (required) |

#### `license inspect`

Decode and display the contents of a license file. Verifies the signature if the public key is available.

```bash
vigo-license-manager license inspect acme-corp.lic
```

#### `license verify`

Signature check only — confirms the file is valid and untampered without displaying full contents.

```bash
vigo-license-manager license verify acme-corp.lic
```

#### `license list`

List all issued licenses from the database. Optionally filter to licenses expiring soon.

```bash
vigo-license-manager license list
vigo-license-manager license list --expiring-within=60
```

| Flag | Description |
|------|-------------|
| `--expiring-within` | Only show licenses expiring within N days |

### Customer Management

#### `customer list`

List all customers in the database.

```bash
vigo-license-manager customer list
```

#### `customer show`

Show a specific customer's full license history.

```bash
vigo-license-manager customer show acme-001
```

---

## Global Flags

| Flag | Description | Default |
|------|-------------|---------|
| `--key-dir` | Directory for keys and license database | `/srv/vigo/license-keys` |

Also configurable via the `VIGO_KEY_DIR` environment variable.

---

## Enforcement Stages

When a customer installs the license, the Vigo server enforces it through four stages:

| Stage | Condition | Behavior |
|-------|-----------|----------|
| 0 — Compliant | Valid license, envoys within limit | Normal operation |
| 1 — Grace Period | License expired or envoy count exceeded (days 1-15) | Full operation, WARN on every check-in |
| 2 — Enrollment Block | 15 days past violation (days 16-30) | Existing envoys work, new enrollments rejected |
| 3 — Service Stop | 30 days past violation (day 31+) | Server shuts down |

Installing a valid license at any stage returns to Compliant immediately.

---

## Security Notes

- The private key never leaves `/srv/vigo/license-keys/` and is never committed to source control
- The public key is safe to embed in the binary — it can only verify, not create signatures
- License files are not secret but cannot be forged without the private key
- The grace period start timestamp is persisted to disk and cannot be reset by restarting the server
