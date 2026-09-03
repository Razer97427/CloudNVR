package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"cloudnvr/internal/domain"
)

var ErrNotFound = errors.New("not found")

type Store struct{ db *sql.DB }

func New(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE cameras
		ADD COLUMN IF NOT EXISTS access_mode VARCHAR(16) NOT NULL DEFAULT 'agent' AFTER stream_url_encrypted`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE cameras
		ADD COLUMN IF NOT EXISTS manual_recording BOOLEAN NOT NULL DEFAULT FALSE AFTER enabled`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE cameras
		ADD COLUMN IF NOT EXISTS ptz_enabled BOOLEAN NOT NULL DEFAULT FALSE AFTER manual_recording,
		ADD COLUMN IF NOT EXISTS ptz_endpoint VARCHAR(1024) NOT NULL DEFAULT '' AFTER ptz_enabled,
		ADD COLUMN IF NOT EXISTS ptz_username VARCHAR(190) NOT NULL DEFAULT '' AFTER ptz_endpoint,
		ADD COLUMN IF NOT EXISTS ptz_password_encrypted TEXT NULL AFTER ptz_username`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS mobile_pairing_tokens (
		token_hash CHAR(64) PRIMARY KEY,
		expires_at DATETIME(6) NOT NULL,
		used_at DATETIME(6) NULL,
		created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
		INDEX idx_mobile_pairing_expiry (expires_at)
	)`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS device_sessions (
		id CHAR(36) PRIMARY KEY,
		name VARCHAR(190) NOT NULL,
		token_hash CHAR(64) NOT NULL UNIQUE,
		expires_at DATETIME(6) NOT NULL,
		last_seen_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
		revoked_at DATETIME(6) NULL,
		created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
		INDEX idx_device_session_token (token_hash),
		INDEX idx_device_session_expiry (expires_at)
	)`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE recordings
		ADD COLUMN IF NOT EXISTS inventory_token CHAR(36) NULL AFTER event_type`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE agents
		ADD COLUMN IF NOT EXISTS webrtc_enabled BOOLEAN NOT NULL DEFAULT FALSE AFTER version`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE agents
		ADD COLUMN IF NOT EXISTS storage_ok BOOLEAN NOT NULL DEFAULT FALSE AFTER webrtc_enabled,
		ADD COLUMN IF NOT EXISTS storage_total_bytes BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER storage_ok,
		ADD COLUMN IF NOT EXISTS storage_free_bytes BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER storage_total_bytes,
		ADD COLUMN IF NOT EXISTS recording_workers INT UNSIGNED NOT NULL DEFAULT 0 AFTER storage_free_bytes,
		ADD COLUMN IF NOT EXISTS relay_workers INT UNSIGNED NOT NULL DEFAULT 0 AFTER recording_workers,
		ADD COLUMN IF NOT EXISTS health_error VARCHAR(1000) NOT NULL DEFAULT '' AFTER relay_workers`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_recordings_inventory
		ON recordings (source, camera_id, inventory_token)`); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_recordings_source_storage ON recordings (source, storage_key(190))`)
	return err
}

func (s *Store) CreateSite(ctx context.Context, site domain.Site, enrollmentHash string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sites
        (id, name, location, default_recording_mode, enrollment_token_hash)
        VALUES (?, ?, ?, ?, ?)`, site.ID, site.Name, site.Location, site.DefaultRecordingMode, enrollmentHash)
	return err
}

func (s *Store) ListSites(ctx context.Context) ([]domain.Site, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT s.id, s.name, s.location, s.default_recording_mode, s.created_at,
		COUNT(c.id), COALESCE(a.status, 'not_enrolled'), a.last_seen_at,
		COALESCE(a.storage_ok, FALSE), COALESCE(a.storage_total_bytes, 0), COALESCE(a.storage_free_bytes, 0),
		COALESCE(a.recording_workers, 0), COALESCE(a.relay_workers, 0), COALESCE(a.health_error, '')
        FROM sites s
        LEFT JOIN cameras c ON c.site_id = s.id
        LEFT JOIN agents a ON a.site_id = s.id
		GROUP BY s.id, s.name, s.location, s.default_recording_mode, s.created_at, a.status, a.last_seen_at,
		a.storage_ok, a.storage_total_bytes, a.storage_free_bytes, a.recording_workers, a.relay_workers, a.health_error
        ORDER BY s.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.Site
	for rows.Next() {
		var v domain.Site
		if err := rows.Scan(&v.ID, &v.Name, &v.Location, &v.DefaultRecordingMode, &v.CreatedAt,
			&v.CameraCount, &v.AgentStatus, &v.AgentLastSeenAt, &v.StorageOK, &v.StorageTotalBytes, &v.StorageFreeBytes,
			&v.RecordingWorkers, &v.RelayWorkers, &v.AgentHealthError); err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}

func (s *Store) EnrollAgent(ctx context.Context, id, siteID, name, agentTokenHash, enrollmentTokenHash string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE sites SET enrollment_used_at = NOW(6)
        WHERE id = ? AND enrollment_token_hash = ? AND enrollment_used_at IS NULL`, siteID, enrollmentTokenHash)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agents (id, site_id, name, token_hash, status, last_seen_at)
		VALUES (?, ?, ?, ?, 'online', NOW(6))
		ON DUPLICATE KEY UPDATE id = VALUES(id), name = VALUES(name), token_hash = VALUES(token_hash),
		status = 'online', version = '', webrtc_enabled = FALSE, last_seen_at = NOW(6)`, id, siteID, name, agentTokenHash); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ResetEnrollmentToken(ctx context.Context, siteID, tokenHash string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE sites SET enrollment_token_hash = ?, enrollment_used_at = NULL WHERE id = ?`, tokenHash, siteID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) AgentByTokenHash(ctx context.Context, hash string) (agentID, siteID string, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT id, site_id FROM agents WHERE token_hash = ?`, hash).Scan(&agentID, &siteID)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return
}

func (s *Store) Heartbeat(ctx context.Context, agentID, version string, webRTCEnabled bool, health domain.AgentHealth) error {
	result, err := s.db.ExecContext(ctx, `UPDATE agents SET status = 'online', version = ?, webrtc_enabled = ?,
		storage_ok = ?, storage_total_bytes = ?, storage_free_bytes = ?, recording_workers = ?, relay_workers = ?, health_error = ?,
		last_seen_at = NOW(6) WHERE id = ?`, version, webRTCEnabled, health.StorageOK, health.StorageTotalBytes,
		health.StorageFreeBytes, health.RecordingWorkers, health.RelayWorkers, health.Error, agentID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) CameraLive(ctx context.Context, cameraID string) (camera domain.Camera, agentOnline, agentWebRTC bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT c.id, c.site_id, c.access_mode, c.enabled,
		COALESCE(a.status = 'online', FALSE), COALESCE(a.webrtc_enabled, FALSE)
		FROM cameras c LEFT JOIN agents a ON a.site_id = c.site_id WHERE c.id = ?`, cameraID).
		Scan(&camera.ID, &camera.SiteID, &camera.AccessMode, &camera.Enabled, &agentOnline, &agentWebRTC)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return
}

// TouchAgentPresence treats every authenticated agent request as proof of
// life. The timestamp write is throttled in SQL since long-polling endpoints
// can be called more than once per second.
func (s *Store) TouchAgentPresence(ctx context.Context, agentID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET status = 'online', last_seen_at = NOW(6)
		WHERE id = ? AND (status <> 'online' OR last_seen_at < DATE_SUB(NOW(6), INTERVAL 30 SECOND))`, agentID)
	return err
}

func (s *Store) CreateCamera(ctx context.Context, camera domain.Camera, encryptedURL, encryptedPTZPassword string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO cameras
		(id, site_id, name, stream_url_encrypted, access_mode, recording_mode, local_retention_days, cloud_retention_days, transfer_policy, enabled, manual_recording,
		 ptz_enabled, ptz_endpoint, ptz_username, ptz_password_encrypted)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''))`, camera.ID, camera.SiteID, camera.Name, encryptedURL, camera.AccessMode, camera.RecordingMode,
		camera.LocalRetentionDays, camera.CloudRetentionDays, camera.TransferPolicy, camera.Enabled, camera.ManualRecording,
		camera.PTZEnabled, camera.PTZEndpoint, camera.PTZUsername, encryptedPTZPassword)
	return err
}

func (s *Store) ListCameras(ctx context.Context, siteID string, includeSecret bool, accessMode domain.AccessMode) ([]domain.Camera, map[string]string, error) {
	query := `SELECT id, site_id, name, stream_url_encrypted, access_mode, recording_mode,
		local_retention_days, cloud_retention_days, transfer_policy, enabled, manual_recording,
		ptz_enabled, ptz_endpoint, ptz_username, COALESCE(ptz_password_encrypted, ''), created_at
		FROM cameras WHERE site_id = ?`
	args := []any{siteID}
	if accessMode != "" {
		query += ` AND access_mode = ?`
		args = append(args, accessMode)
	}
	query += ` ORDER BY created_at`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var result []domain.Camera
	secrets := make(map[string]string)
	for rows.Next() {
		var v domain.Camera
		var encrypted, encryptedPTZPassword string
		if err := rows.Scan(&v.ID, &v.SiteID, &v.Name, &encrypted, &v.AccessMode, &v.RecordingMode, &v.LocalRetentionDays,
			&v.CloudRetentionDays, &v.TransferPolicy, &v.Enabled, &v.ManualRecording, &v.PTZEnabled, &v.PTZEndpoint,
			&v.PTZUsername, &encryptedPTZPassword, &v.CreatedAt); err != nil {
			return nil, nil, err
		}
		result = append(result, v)
		if includeSecret {
			secrets[v.ID] = encrypted
			secrets[v.ID+":ptz"] = encryptedPTZPassword
		}
	}
	return result, secrets, rows.Err()
}

func (s *Store) ListCamerasByAccessMode(ctx context.Context, accessMode domain.AccessMode) ([]domain.Camera, map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, site_id, name, stream_url_encrypted, access_mode, recording_mode,
		local_retention_days, cloud_retention_days, transfer_policy, enabled, manual_recording,
		ptz_enabled, ptz_endpoint, ptz_username, COALESCE(ptz_password_encrypted, ''), created_at
		FROM cameras WHERE access_mode = ? ORDER BY created_at`, accessMode)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var result []domain.Camera
	secrets := make(map[string]string)
	for rows.Next() {
		var v domain.Camera
		var encrypted, encryptedPTZPassword string
		if err := rows.Scan(&v.ID, &v.SiteID, &v.Name, &encrypted, &v.AccessMode, &v.RecordingMode, &v.LocalRetentionDays,
			&v.CloudRetentionDays, &v.TransferPolicy, &v.Enabled, &v.ManualRecording, &v.PTZEnabled, &v.PTZEndpoint,
			&v.PTZUsername, &encryptedPTZPassword, &v.CreatedAt); err != nil {
			return nil, nil, err
		}
		result = append(result, v)
		secrets[v.ID] = encrypted
		secrets[v.ID+":ptz"] = encryptedPTZPassword
	}
	return result, secrets, rows.Err()
}

func (s *Store) CameraExists(ctx context.Context, id string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM cameras WHERE id = ?)`, id).Scan(&exists)
	return exists, err
}

func (s *Store) ListAllCameras(ctx context.Context) ([]domain.Camera, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, site_id, name, access_mode, recording_mode,
		local_retention_days, cloud_retention_days, transfer_policy, enabled, manual_recording,
		ptz_enabled, ptz_endpoint, ptz_username, created_at
		FROM cameras ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.Camera
	for rows.Next() {
		var camera domain.Camera
		if err := rows.Scan(&camera.ID, &camera.SiteID, &camera.Name, &camera.AccessMode, &camera.RecordingMode,
			&camera.LocalRetentionDays, &camera.CloudRetentionDays, &camera.TransferPolicy, &camera.Enabled, &camera.ManualRecording,
			&camera.PTZEnabled, &camera.PTZEndpoint, &camera.PTZUsername, &camera.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, camera)
	}
	return result, rows.Err()
}

func (s *Store) SetManualRecording(ctx context.Context, cameraID string, active bool) error {
	result, err := s.db.ExecContext(ctx, `UPDATE cameras SET manual_recording = ? WHERE id = ? AND recording_mode = 'manual'`, active, cameraID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err == nil && rows == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) UpsertRecording(ctx context.Context, recording domain.Recording) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO recordings
		(id, camera_id, source, storage_key, checksum_sha256, started_at, ended_at, size_bytes, event_type)
		SELECT ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, ? FROM DUAL
		WHERE NOT EXISTS (SELECT 1 FROM recordings WHERE source = ? AND storage_key = ?)
		ON DUPLICATE KEY UPDATE checksum_sha256 = COALESCE(VALUES(checksum_sha256), checksum_sha256), ended_at = VALUES(ended_at), size_bytes = VALUES(size_bytes), event_type = VALUES(event_type)`,
		recording.ID, recording.CameraID, recording.Source, recording.StorageKey, recording.ChecksumSHA256, recording.StartedAt, recording.EndedAt,
		recording.SizeBytes, recording.EventType, recording.Source, recording.StorageKey)
	return err
}

// UpsertRecordingInventory marks a recording as present in a complete storage
// inventory. Reconciliation is only run after every inventory page succeeds.
func (s *Store) UpsertRecordingInventory(ctx context.Context, recording domain.Recording, inventoryToken string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO recordings
		(id, camera_id, source, storage_key, checksum_sha256, started_at, ended_at, size_bytes, event_type, inventory_token)
		SELECT ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?, ? FROM DUAL
		WHERE NOT EXISTS (SELECT 1 FROM recordings WHERE source = ? AND storage_key = ?)`,
		recording.ID, recording.CameraID, recording.Source, recording.StorageKey, recording.ChecksumSHA256, recording.StartedAt, recording.EndedAt,
		recording.SizeBytes, recording.EventType, inventoryToken, recording.Source, recording.StorageKey)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE recordings
		SET checksum_sha256 = COALESCE(NULLIF(?, ''), checksum_sha256), ended_at = ?, size_bytes = ?, event_type = ?, inventory_token = ?
		WHERE source = ? AND storage_key = ?`, recording.ChecksumSHA256, recording.EndedAt, recording.SizeBytes, recording.EventType, inventoryToken,
		recording.Source, recording.StorageKey)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// ReconcileRecordingInventory removes database rows whose files were not seen
// in the completed inventory. The camera scope prevents one agent or scanner
// from deleting recordings owned by another source.
func (s *Store) ReconcileRecordingInventory(ctx context.Context, source string, cameraIDs []string, inventoryToken string) (int64, error) {
	if len(cameraIDs) == 0 || inventoryToken == "" {
		return 0, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(cameraIDs)), ",")
	query := `DELETE FROM recordings WHERE source = ? AND camera_id IN (` + placeholders + `)
		AND (inventory_token IS NULL OR inventory_token <> ?)`
	args := make([]any, 0, len(cameraIDs)+2)
	args = append(args, source)
	for _, cameraID := range cameraIDs {
		args = append(args, cameraID)
	}
	args = append(args, inventoryToken)
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) ListRecordings(ctx context.Context, siteID, cameraID string, limit int) ([]domain.Recording, error) {
	if limit <= 0 || limit > 5000 {
		limit = 200
	}
	query := `SELECT r.id, r.camera_id, c.name, c.site_id, s.name, r.source, r.storage_key,
		r.started_at, r.ended_at, r.size_bytes, COALESCE(r.checksum_sha256, ''), r.event_type
		FROM recordings r JOIN cameras c ON c.id = r.camera_id JOIN sites s ON s.id = c.site_id WHERE 1=1`
	args := []any{}
	if siteID != "" {
		query += ` AND c.site_id = ?`
		args = append(args, siteID)
	}
	if cameraID != "" {
		query += ` AND c.id = ?`
		args = append(args, cameraID)
	}
	query += ` ORDER BY r.started_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.Recording
	for rows.Next() {
		var recording domain.Recording
		if err := rows.Scan(&recording.ID, &recording.CameraID, &recording.CameraName, &recording.SiteID, &recording.SiteName,
			&recording.Source, &recording.StorageKey, &recording.StartedAt, &recording.EndedAt, &recording.SizeBytes, &recording.ChecksumSHA256, &recording.EventType); err != nil {
			return nil, err
		}
		result = append(result, recording)
	}
	return result, rows.Err()
}

func (s *Store) RecordingByID(ctx context.Context, recordingID string) (domain.Recording, error) {
	var recording domain.Recording
	err := s.db.QueryRowContext(ctx, `SELECT r.id, r.camera_id, c.name, c.site_id, r.source, r.storage_key, r.started_at, r.ended_at, r.size_bytes, COALESCE(r.checksum_sha256, ''), r.event_type
		FROM recordings r JOIN cameras c ON c.id = r.camera_id WHERE r.id = ?`, recordingID).Scan(&recording.ID, &recording.CameraID,
		&recording.CameraName, &recording.SiteID, &recording.Source, &recording.StorageKey, &recording.StartedAt, &recording.EndedAt, &recording.SizeBytes, &recording.ChecksumSHA256, &recording.EventType)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Recording{}, ErrNotFound
	}
	return recording, err
}

func (s *Store) PruneCloudRecordings(ctx context.Context, cameraID string, cutoff time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM recordings WHERE camera_id = ? AND source = 'cloud' AND ended_at < ?`, cameraID, cutoff)
	return err
}

func (s *Store) DeleteAgentRecording(ctx context.Context, cameraID, storageKey string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM recordings WHERE source = 'agent' AND camera_id = ? AND storage_key = ?`, cameraID, storageKey)
	return err
}

func (s *Store) PruneAgentRecordings(ctx context.Context, cameraID string, cutoff time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM recordings WHERE camera_id = ? AND source = 'agent' AND ended_at < ?`, cameraID, cutoff)
	return err
}

func (s *Store) CreateMobilePairing(ctx context.Context, tokenHash string, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO mobile_pairing_tokens (token_hash, expires_at) VALUES (?, ?)`, tokenHash, expiresAt)
	return err
}

func (s *Store) ClaimMobilePairing(ctx context.Context, pairingHash string, session domain.DeviceSession, sessionTokenHash string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE mobile_pairing_tokens SET used_at = NOW(6)
		WHERE token_hash = ? AND used_at IS NULL AND expires_at > NOW(6)`, pairingHash)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO device_sessions
		(id, name, token_hash, expires_at, last_seen_at) VALUES (?, ?, ?, ?, NOW(6))`,
		session.ID, session.Name, sessionTokenHash, session.ExpiresAt); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeviceSessionByTokenHash(ctx context.Context, tokenHash string) (domain.DeviceSession, error) {
	var session domain.DeviceSession
	err := s.db.QueryRowContext(ctx, `SELECT id, name, last_seen_at, expires_at, created_at FROM device_sessions
		WHERE token_hash = ? AND revoked_at IS NULL AND expires_at > NOW(6)`, tokenHash).
		Scan(&session.ID, &session.Name, &session.LastSeenAt, &session.ExpiresAt, &session.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.DeviceSession{}, ErrNotFound
	}
	if err == nil {
		_, _ = s.db.ExecContext(ctx, `UPDATE device_sessions SET last_seen_at = NOW(6)
			WHERE id = ? AND last_seen_at < DATE_SUB(NOW(6), INTERVAL 5 MINUTE)`, session.ID)
	}
	return session, err
}

func (s *Store) ListDeviceSessions(ctx context.Context) ([]domain.DeviceSession, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, last_seen_at, expires_at, created_at FROM device_sessions
		WHERE revoked_at IS NULL AND expires_at > NOW(6) ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sessions []domain.DeviceSession
	for rows.Next() {
		var session domain.DeviceSession
		if err := rows.Scan(&session.ID, &session.Name, &session.LastSeenAt, &session.ExpiresAt, &session.CreatedAt); err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

func (s *Store) RevokeDeviceSession(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE device_sessions SET revoked_at = NOW(6) WHERE id = ? AND revoked_at IS NULL`, id)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err == nil && rows == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) RevokeDeviceSessionByTokenHash(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE device_sessions SET revoked_at = NOW(6) WHERE token_hash = ?`, tokenHash)
	return err
}

func (s *Store) UpdateCameraPolicy(ctx context.Context, id string, mode domain.RecordingMode, localDays, cloudDays uint, transfer domain.TransferPolicy, enabled bool) error {
	result, err := s.db.ExecContext(ctx, `UPDATE cameras SET recording_mode = ?, local_retention_days = ?,
        cloud_retention_days = ?, transfer_policy = ?, enabled = ? WHERE id = ?`, mode, localDays, cloudDays, transfer, enabled, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) UpdateCamera(ctx context.Context, id, name string, encryptedURL, encryptedPTZPassword *string, accessMode domain.AccessMode, mode domain.RecordingMode, localDays, cloudDays uint, transfer domain.TransferPolicy, enabled, ptzEnabled bool, ptzEndpoint, ptzUsername string) error {
	var streamURL any
	if encryptedURL != nil {
		streamURL = *encryptedURL
	}
	var ptzPassword any
	if encryptedPTZPassword != nil {
		ptzPassword = *encryptedPTZPassword
	}
	result, err := s.db.ExecContext(ctx, `UPDATE cameras SET name = ?,
		stream_url_encrypted = COALESCE(?, stream_url_encrypted), access_mode = ?, recording_mode = ?, local_retention_days = ?,
		cloud_retention_days = ?, transfer_policy = ?, enabled = ?, ptz_enabled = ?, ptz_endpoint = ?, ptz_username = ?,
		ptz_password_encrypted = COALESCE(?, ptz_password_encrypted) WHERE id = ?`,
		name, streamURL, accessMode, mode, localDays, cloudDays, transfer, enabled, ptzEnabled, ptzEndpoint, ptzUsername, ptzPassword, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) CameraPTZ(ctx context.Context, cameraID string) (domain.Camera, string, error) {
	var camera domain.Camera
	var encryptedPassword string
	err := s.db.QueryRowContext(ctx, `SELECT id, site_id, access_mode, enabled, ptz_enabled, ptz_endpoint, ptz_username,
		COALESCE(ptz_password_encrypted, '') FROM cameras WHERE id = ?`, cameraID).Scan(&camera.ID, &camera.SiteID,
		&camera.AccessMode, &camera.Enabled, &camera.PTZEnabled, &camera.PTZEndpoint, &camera.PTZUsername, &encryptedPassword)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Camera{}, "", ErrNotFound
	}
	return camera, encryptedPassword, err
}

func (s *Store) MarkStaleAgentsOffline(ctx context.Context) error {
	// Compare using the database clock. Supplying a Go time in the server's
	// local timezone can otherwise mark every agent offline when MariaDB runs
	// in UTC.
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET status = 'offline'
		WHERE last_seen_at < DATE_SUB(NOW(6), INTERVAL 90 SECOND)`)
	return err
}
