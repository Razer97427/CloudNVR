CREATE TABLE IF NOT EXISTS mobile_pairing_tokens (
    token_hash CHAR(64) PRIMARY KEY,
    expires_at DATETIME(6) NOT NULL,
    used_at DATETIME(6) NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    INDEX idx_mobile_pairing_expiry (expires_at)
);

CREATE TABLE IF NOT EXISTS device_sessions (
    id CHAR(36) PRIMARY KEY,
    name VARCHAR(190) NOT NULL,
    token_hash CHAR(64) NOT NULL UNIQUE,
    expires_at DATETIME(6) NOT NULL,
    last_seen_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    revoked_at DATETIME(6) NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    INDEX idx_device_session_token (token_hash),
    INDEX idx_device_session_expiry (expires_at)
);
