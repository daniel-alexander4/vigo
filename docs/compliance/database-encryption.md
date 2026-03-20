
---
title: Database Encryption at Rest
---

# Database Encryption at Rest

HIPAA 164.312(a)(2)(iv) requires encryption of ePHI at rest. Vigo already encrypts secrets (AES-256-GCM) and backup archives (AES-256-GCM with Argon2id), but the database file itself requires additional measures depending on your database driver.

## What's Already Encrypted

| Component | Encryption | Details |
|-----------|-----------|---------|
| Secrets files | AES-256-GCM | All values in `/srv/vigo/secrets/` encrypted with master key |
| Backup archives | AES-256-GCM | Optional via `vigogo backup create --encrypt` |
| Transport | mTLS (TLS 1.3) | All gRPC and REST traffic |
| Agent communication | ED25519 + mTLS | Signed payloads over encrypted transport |
| API tokens | bcrypt | Hashed at rest, never stored in plaintext |
| Web passwords | bcrypt + AES-256-GCM | Bcrypt hash stored as encrypted secret |

## What Needs Encryption

The database file contains:

- Envoy enrollment records (hostname, IP, public key)
- Trait data (OS, hardware, network, packages)
- Run results (convergence outcomes, resource states)
- Audit trail entries (tamper-evident hash chain)
- User accounts (username, email, role — no passwords)
- Session data
- File snapshots (previous content of managed files)
- Compliance history

No raw secrets are stored in the database — all secret values live in the secrets provider. However, if your environment processes ePHI, the database may contain system information about hosts that handle ePHI, which may itself be considered protected under HIPAA.

## SQLite: LUKS/dm-crypt (Recommended)

For SQLite deployments, we recommend encrypting the filesystem that contains the database using Linux LUKS (dm-crypt). This provides transparent encryption with no application changes and no performance overhead from application-layer encryption.

### Setup

#### 1. Create an encrypted volume

```bash
# Create a LUKS-encrypted partition (or loopback device for testing)
sudo cryptsetup luksFormat /dev/sdX

# Open the encrypted volume
sudo cryptsetup open /dev/sdX vigo-data

# Create a filesystem
sudo mkfs.ext4 /dev/mapper/vigo-data

# Mount it
sudo mkdir -p /srv/vigo
sudo mount /dev/mapper/vigo-data /srv/vigo
```

#### 2. Auto-unlock on boot

For production servers, configure automatic unlocking using a key file or TPM:

**Key file (simpler, suitable for most deployments):**

```bash
# Generate a key file
sudo dd if=/dev/urandom of=/root/.vigo-luks-key bs=512 count=1
sudo chmod 600 /root/.vigo-luks-key

# Add the key file to the LUKS volume
sudo cryptsetup luksAddKey /dev/sdX /root/.vigo-luks-key

# Add to /etc/crypttab for auto-unlock
echo "vigo-data /dev/sdX /root/.vigo-luks-key luks" | sudo tee -a /etc/crypttab

# Add to /etc/fstab for auto-mount
echo "/dev/mapper/vigo-data /srv/vigo ext4 defaults 0 2" | sudo tee -a /etc/fstab
```

**TPM 2.0 (higher security, binds decryption to hardware):**

```bash
# Requires systemd-cryptenroll (systemd 248+)
sudo systemd-cryptenroll /dev/sdX --tpm2-device=auto

# Update /etc/crypttab
echo "vigo-data /dev/sdX - tpm2-device=auto" | sudo tee -a /etc/crypttab
```

#### 3. Verify encryption

```bash
# Confirm the volume is encrypted
sudo cryptsetup status vigo-data

# Should show:
#   type:    LUKS2
#   cipher:  aes-xts-plain64
#   keysize: 512 bits
```

#### 4. Docker deployments

For Docker, mount the encrypted volume as a bind mount:

```yaml
# docker-compose.yml
services:
  vigosrv:
    image: ghcr.io/daniel-alexander4/vigo:latest
    volumes:
      - /srv/vigo:/srv/vigo    # LUKS-encrypted mount point
```

The container accesses the decrypted filesystem transparently. Ensure the LUKS volume is unlocked before starting the container.

### Loopback device (testing/development)

For testing without a dedicated partition:

```bash
# Create a 1 GB file-backed encrypted volume
dd if=/dev/zero of=/var/lib/vigo-encrypted.img bs=1M count=1024
sudo cryptsetup luksFormat /var/lib/vigo-encrypted.img
sudo cryptsetup open /var/lib/vigo-encrypted.img vigo-data
sudo mkfs.ext4 /dev/mapper/vigo-data
sudo mount /dev/mapper/vigo-data /srv/vigo
```

### Performance

LUKS/dm-crypt uses AES-NI hardware acceleration on modern CPUs. Performance impact is typically < 5% for database workloads. Verify with:

```bash
cryptsetup benchmark
```

Expected output on modern hardware:

```
#     Algorithm |       Key |      Encryption |      Decryption
        aes-xts        256b       3000.0 MiB/s       3000.0 MiB/s
```

## PostgreSQL: Transparent Data Encryption

For environments that require database-level encryption or cannot use filesystem encryption, switch to PostgreSQL with Transparent Data Encryption (TDE).

### Option A: pg_tde Extension

PostgreSQL 16+ supports TDE via the `pg_tde` extension (Percona):

```sql
-- Install and enable
CREATE EXTENSION pg_tde;

-- Set the master key (store the key in a secure location)
SELECT pg_tde_set_key('vigo_master_key', 'your-256-bit-key-hex');

-- Convert existing tables
ALTER TABLE envoys SET ACCESS METHOD tde_heap;
ALTER TABLE envoy_traits SET ACCESS METHOD tde_heap;
ALTER TABLE runs SET ACCESS METHOD tde_heap;
ALTER TABLE run_results SET ACCESS METHOD tde_heap;
ALTER TABLE audit_entries SET ACCESS METHOD tde_heap;
ALTER TABLE users SET ACCESS METHOD tde_heap;
ALTER TABLE api_tokens SET ACCESS METHOD tde_heap;
```

### Option B: Full Disk Encryption + PostgreSQL

Combine LUKS with PostgreSQL for defense in depth:

```bash
# Encrypt the PostgreSQL data directory
sudo cryptsetup luksFormat /dev/sdY
sudo cryptsetup open /dev/sdY pg-data
sudo mkfs.ext4 /dev/mapper/pg-data
sudo mount /dev/mapper/pg-data /var/lib/postgresql
```

### Option C: Cloud-Managed PostgreSQL

All major cloud providers offer encryption at rest by default:

| Provider | Service | Encryption |
|----------|---------|-----------|
| AWS | RDS PostgreSQL | AES-256, KMS keys |
| GCP | Cloud SQL | AES-256, customer-managed keys optional |
| Azure | Azure Database for PostgreSQL | AES-256, service-managed keys |

### Vigo PostgreSQL Configuration

```yaml
# server.yaml
database:
  driver: postgres
  dsn: "secret:vigo/db/dsn"
```

```bash
# Store the PostgreSQL DSN as an encrypted secret
vigogo secrets set vigo/db/dsn "host=db.example.com port=5432 user=vigo password=secret dbname=vigo sslmode=verify-full"
```

## Verification

### Confirm SQLite file is on an encrypted volume

```bash
# Find which mount point contains the database
df /srv/vigo/db/vigo.db

# Check if the underlying device is LUKS-encrypted
lsblk -o NAME,TYPE,FSTYPE,MOUNTPOINT | grep crypt
```

### Confirm PostgreSQL TDE is active

```sql
SELECT pg_tde_is_encrypted('envoys');
-- Should return true
```

### Confirm cloud encryption

Check your cloud provider's console or CLI for encryption status on the managed database instance.

## HIPAA Mapping

| Requirement | Standard | Implementation |
|-------------|----------|----------------|
| Encryption at rest | 164.312(a)(2)(iv) | LUKS for SQLite, TDE or cloud encryption for PostgreSQL |
| Access control | 164.312(a)(1) | LUKS key file permissions (0600), TPM binding |
| Integrity | 164.312(c)(1) | LUKS AEAD mode (aes-xts-plain64), PostgreSQL checksums |
| Audit | 164.312(b) | LUKS operations logged in system journal |

## Recommendations

| Deployment | Recommended Approach |
|------------|---------------------|
| Single server, SQLite | LUKS/dm-crypt on `/srv/vigo` |
| Docker, SQLite | LUKS on host, bind mount into container |
| Cloud VM, SQLite | Cloud-provided disk encryption (e.g., AWS EBS encryption) |
| Self-hosted PostgreSQL | LUKS on data directory + `pg_tde` extension |
| Cloud PostgreSQL | Cloud-managed encryption (enabled by default) |

For most deployments, **LUKS/dm-crypt with SQLite** provides the best balance of simplicity, performance, and compliance. Switch to PostgreSQL only if you need database-level encryption controls, multi-server access, or your compliance auditor requires it.
