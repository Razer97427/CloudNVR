CREATE TABLE IF NOT EXISTS sites (
    id CHAR(36) PRIMARY KEY,
    name VARCHAR(190) NOT NULL,
    location VARCHAR(255) NOT NULL DEFAULT '',
    default_recording_mode VARCHAR(16) NOT NULL DEFAULT 'hybrid',
    enrollment_token_hash CHAR(64) NOT NULL,
    enrollment_used_at DATETIME(6) NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    CONSTRAINT chk_site_mode CHECK (default_recording_mode IN ('local', 'cloud', 'hybrid', 'manual', 'disabled'))
);

CREATE TABLE IF NOT EXISTS agents (
    id CHAR(36) PRIMARY KEY,
    site_id CHAR(36) NOT NULL UNIQUE,
    name VARCHAR(190) NOT NULL,
    token_hash CHAR(64) NOT NULL UNIQUE,
    status VARCHAR(16) NOT NULL DEFAULT 'offline',
    version VARCHAR(64) NOT NULL DEFAULT '',
    webrtc_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    storage_ok BOOLEAN NOT NULL DEFAULT FALSE,
    storage_total_bytes BIGINT UNSIGNED NOT NULL DEFAULT 0,
    storage_free_bytes BIGINT UNSIGNED NOT NULL DEFAULT 0,
    recording_workers INT UNSIGNED NOT NULL DEFAULT 0,
    relay_workers INT UNSIGNED NOT NULL DEFAULT 0,
    health_error VARCHAR(1000) NOT NULL DEFAULT '',
    last_seen_at DATETIME(6) NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    CONSTRAINT fk_agents_site FOREIGN KEY (site_id) REFERENCES sites(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS cameras (
    id CHAR(36) PRIMARY KEY,
    site_id CHAR(36) NOT NULL,
    name VARCHAR(190) NOT NULL,
    stream_url_encrypted TEXT NOT NULL,
    access_mode VARCHAR(16) NOT NULL DEFAULT 'agent',
    recording_mode VARCHAR(16) NOT NULL,
    local_retention_days INT UNSIGNED NOT NULL DEFAULT 7,
    cloud_retention_days INT UNSIGNED NOT NULL DEFAULT 30,
    transfer_policy VARCHAR(32) NOT NULL DEFAULT 'events_and_manual',
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    manual_recording BOOLEAN NOT NULL DEFAULT FALSE,
    ptz_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    ptz_endpoint VARCHAR(1024) NOT NULL DEFAULT '',
    ptz_username VARCHAR(190) NOT NULL DEFAULT '',
    ptz_password_encrypted TEXT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    INDEX idx_cameras_site (site_id),
    CONSTRAINT fk_cameras_site FOREIGN KEY (site_id) REFERENCES sites(id) ON DELETE CASCADE,
    CONSTRAINT chk_access_mode CHECK (access_mode IN ('agent', 'direct')),
    CONSTRAINT chk_camera_mode CHECK (recording_mode IN ('local', 'cloud', 'hybrid', 'manual', 'disabled')),
    CONSTRAINT chk_transfer_policy CHECK (transfer_policy IN ('all', 'events', 'manual', 'events_and_manual', 'none'))
);

CREATE TABLE IF NOT EXISTS recordings (
    id CHAR(36) PRIMARY KEY,
    camera_id CHAR(36) NOT NULL,
    source VARCHAR(16) NOT NULL,
    storage_key VARCHAR(1024) NOT NULL,
    checksum_sha256 CHAR(64) NULL,
    started_at DATETIME(6) NOT NULL,
    ended_at DATETIME(6) NULL,
    size_bytes BIGINT UNSIGNED NOT NULL DEFAULT 0,
    event_type VARCHAR(64) NOT NULL DEFAULT '',
    inventory_token CHAR(36) NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    INDEX idx_recordings_camera_started (camera_id, started_at),
    INDEX idx_recordings_inventory (source, camera_id, inventory_token),
    CONSTRAINT fk_recordings_camera FOREIGN KEY (camera_id) REFERENCES cameras(id) ON DELETE CASCADE
);
