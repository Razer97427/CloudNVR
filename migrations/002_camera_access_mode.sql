ALTER TABLE cameras
    ADD COLUMN IF NOT EXISTS access_mode VARCHAR(16) NOT NULL DEFAULT 'agent' AFTER stream_url_encrypted;
