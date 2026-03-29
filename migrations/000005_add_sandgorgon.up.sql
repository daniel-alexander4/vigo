-- Sandgorgon: bare metal lifecycle management

CREATE TABLE IF NOT EXISTS sandgorgon_assets (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    envoy_id TEXT REFERENCES envoys(id) ON DELETE SET NULL,
    hostname TEXT NOT NULL DEFAULT '',
    bmc_address TEXT NOT NULL,
    bmc_username TEXT NOT NULL DEFAULT '',
    bmc_password TEXT NOT NULL DEFAULT '',
    serial_number TEXT NOT NULL DEFAULT '',
    manufacturer TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    bios_version TEXT NOT NULL DEFAULT '',
    asset_tag TEXT NOT NULL DEFAULT '',
    rack_location TEXT NOT NULL DEFAULT '',
    lifecycle_state TEXT NOT NULL DEFAULT 'available',
    commissioned_at DATETIME,
    provisioned_at DATETIME,
    decommissioned_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_sandgorgon_assets_envoy ON sandgorgon_assets(envoy_id);
CREATE INDEX IF NOT EXISTS idx_sandgorgon_assets_hostname ON sandgorgon_assets(hostname);
CREATE INDEX IF NOT EXISTS idx_sandgorgon_assets_state ON sandgorgon_assets(lifecycle_state);
CREATE UNIQUE INDEX IF NOT EXISTS idx_sandgorgon_assets_bmc ON sandgorgon_assets(bmc_address);

CREATE TABLE IF NOT EXISTS sandgorgon_certificates (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    asset_id TEXT NOT NULL REFERENCES sandgorgon_assets(id) ON DELETE CASCADE,
    envoy_id TEXT NOT NULL DEFAULT '',
    hostname TEXT NOT NULL DEFAULT '',
    serial_number TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    asset_tag TEXT NOT NULL DEFAULT '',
    sanitization_method TEXT NOT NULL DEFAULT '',
    media_type TEXT NOT NULL DEFAULT '',
    verification_method TEXT NOT NULL DEFAULT '',
    verification_result TEXT NOT NULL DEFAULT '',
    verification_limitations TEXT NOT NULL DEFAULT '',
    technician TEXT NOT NULL DEFAULT '',
    started_at DATETIME NOT NULL DEFAULT (datetime('now')),
    completed_at DATETIME,
    lifecycle_summary TEXT NOT NULL DEFAULT '{}',
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_sandgorgon_certificates_asset ON sandgorgon_certificates(asset_id);

CREATE TABLE IF NOT EXISTS sandgorgon_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    asset_id TEXT NOT NULL REFERENCES sandgorgon_assets(id) ON DELETE CASCADE,
    event_type TEXT NOT NULL,
    detail TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_sandgorgon_events_asset ON sandgorgon_events(asset_id);
CREATE INDEX IF NOT EXISTS idx_sandgorgon_events_type ON sandgorgon_events(event_type);

CREATE TABLE IF NOT EXISTS sandgorgon_images (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    name TEXT NOT NULL,
    os_family TEXT NOT NULL DEFAULT '',
    os_version TEXT NOT NULL DEFAULT '',
    arch TEXT NOT NULL DEFAULT 'amd64',
    image_type TEXT NOT NULL DEFAULT 'iso',
    path TEXT NOT NULL,
    kernel_path TEXT NOT NULL DEFAULT '',
    initrd_path TEXT NOT NULL DEFAULT '',
    boot_params TEXT NOT NULL DEFAULT '',
    preseed_type TEXT NOT NULL DEFAULT '',
    sha256 TEXT NOT NULL DEFAULT '',
    size_bytes INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_sandgorgon_images_name ON sandgorgon_images(name);
