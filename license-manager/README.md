# Hobnob License Manager

Vendor-side tool for generating, signing, and tracking Hobnob license files. Licenses are air-gapped, cryptographically signed with Ed25519, and verified locally by the Hobnob server at runtime. No phone-home, no internet dependency.

## Directory Layout

```
/srv/hobnob/
├── license-keys/
│   ├── hobnob_private.key    # Ed25519 private key (immutable, never distribute)
│   ├── hobnob_public.key     # Ed25519 public key (embedded in hobnob binary)
│   └── licenses.db           # SQLite database tracking all issued licenses
└── README.md
```

## Quick Start

### 1. Install

From the hobnob repo:

```bash
./scripts/install-license-manager.sh
```

This builds the binary, copies it to `/usr/local/bin/hobnob-license-manager`, and creates `/srv/hobnob/license-keys/`.

### 2. Generate Keys (One-Time)

```bash
hobnob-license-manager keys generate
```

Creates the Ed25519 keypair in `/srv/hobnob/license-keys/`. Files are set to read-only and made immutable with `chattr +i`.

### 3. Inject Public Key into Source

```bash
hobnob-license-manager keys inject
```

Writes the public key into `server/license/pubkey.go` in the hobnob repo. Rebuild hobnob after this step so the binary can verify license signatures.

### 4. Create a License

```bash
hobnob-license-manager license create \
  --customer="Acme Corp" \
  --customer-id="acme-001" \
  --max-thralls=500 \
  --expires="2027-06-01" \
  --out=/tmp/acme-corp.lic
```

### 5. Install the License

Place any `.lic` file in `/srv/hobnob/license/` on the server host:

```bash
sudo mkdir -p -m 700 /srv/hobnob/license
sudo cp acme-corp.lic /srv/hobnob/license/
sudo chmod 600 /srv/hobnob/license/acme-corp.lic
```

On startup, `hobd` scans the directory for `.lic` files (lexicographic order) and uses the first one that parses and passes signature verification. The filename does not matter — any valid `.lic` file will work.

The Docker container mounts `/srv/hobnob` from the host, so the server finds the license automatically. At least one valid `.lic` file must be present before starting the container.

The entrypoint enforces `700` on the license directory and `600` on all `.lic` files on every start.

If the server is already running, you can install without restarting:

```bash
hobnobcli license install --file=acme-corp.lic
```

This copies the file into place and sends SIGHUP to `hobd` to reload it.

---

## Commands

### Key Management

#### `keys generate`

Generate a new Ed25519 keypair. Saves to `/srv/hobnob/license-keys/`. Refuses to overwrite existing keys — use `keys rotate` instead.

```bash
hobnob-license-manager keys generate
```

#### `keys show-public`

Print the public key in hex format.

```bash
hobnob-license-manager keys show-public
```

#### `keys inject`

Write the public key into the hobnob source code at `server/license/pubkey.go`. Defaults to looking for the repo at `/home/dan/repos/hobnob`, then auto-detects by walking up from the current directory. Override with `--repo=<path>`.

```bash
hobnob-license-manager keys inject
hobnob-license-manager keys inject --repo=/path/to/hobnob
```

#### `keys rotate`

Replace the keypair. Removes the immutable flag on old keys, generates new ones, and re-applies immutability. All existing licenses become invalid and must be re-issued. Run `keys inject` afterward and rebuild.

```bash
hobnob-license-manager keys rotate
```

### License Lifecycle

#### `license create`

Generate and sign a new license file. Records the license and customer in the local database.

```bash
hobnob-license-manager license create \
  --customer="Acme Corp" \
  --customer-id="acme-001" \
  --max-thralls=500 \
  --expires="2027-06-01" \
  --out=acme-corp.lic
```

| Flag | Description |
|------|-------------|
| `--customer` | Customer display name (required) |
| `--customer-id` | Unique customer identifier (required) |
| `--max-thralls` | Maximum number of managed machines (required) |
| `--expires` | License expiry date, YYYY-MM-DD (required) |
| `--out` | Output file path (required) |

#### `license renew`

Issue a new license with an updated expiry date. The original license is marked as superseded in the database. The customer installs the new file.

```bash
hobnob-license-manager license renew \
  --license-id="lic-20260310-acme-001" \
  --expires="2028-06-01" \
  --out=acme-renewed.lic
```

| Flag | Description |
|------|-------------|
| `--license-id` | ID of the license to renew (required) |
| `--expires` | New expiry date, YYYY-MM-DD (required) |
| `--out` | Output file path (required) |

#### `license update-thralls`

Issue a new license with an updated seat count. Same expiry as the original. The original is marked as superseded.

```bash
hobnob-license-manager license update-thralls \
  --license-id="lic-20260310-acme-001" \
  --max-thralls=1000 \
  --out=acme-expanded.lic
```

| Flag | Description |
|------|-------------|
| `--license-id` | ID of the license to update (required) |
| `--max-thralls` | New maximum thrall count (required) |
| `--out` | Output file path (required) |

#### `license inspect`

Decode and display the contents of a license file. Verifies the signature if the public key is available.

```bash
hobnob-license-manager license inspect acme-corp.lic
```

#### `license verify`

Signature check only — confirms the file is valid and untampered without displaying full contents.

```bash
hobnob-license-manager license verify acme-corp.lic
```

#### `license list`

List all issued licenses from the database. Optionally filter to licenses expiring soon.

```bash
hobnob-license-manager license list
hobnob-license-manager license list --expiring-within=60
```

| Flag | Description |
|------|-------------|
| `--expiring-within` | Only show licenses expiring within N days |

### Customer Management

#### `customer list`

List all customers in the database.

```bash
hobnob-license-manager customer list
```

#### `customer show`

Show a specific customer's full license history.

```bash
hobnob-license-manager customer show acme-001
```

---

## Global Flags

| Flag | Description | Default |
|------|-------------|---------|
| `--key-dir` | Directory for keys and license database | `/srv/hobnob/license-keys` |

Also configurable via the `HOBNOB_KEY_DIR` environment variable.

---

## Enforcement Stages

When a customer installs the license, the Hobnob server enforces it through four stages:

| Stage | Condition | Behavior |
|-------|-----------|----------|
| 0 — Compliant | Valid license, thralls within limit | Normal operation |
| 1 — Grace Period | License expired or thrall count exceeded (days 1-15) | Full operation, WARN on every check-in |
| 2 — Enrollment Block | 15 days past violation (days 16-30) | Existing thralls work, new enrollments rejected |
| 3 — Service Stop | 30 days past violation (day 31+) | Server shuts down |

Installing a valid license at any stage returns to Compliant immediately.

---

## Security Notes

- The private key never leaves `/srv/hobnob/license-keys/` and is never committed to source control
- The public key is safe to embed in the binary — it can only verify, not create signatures
- License files are not secret but cannot be forged without the private key
- The grace period start timestamp is persisted to disk and cannot be reset by restarting the server
