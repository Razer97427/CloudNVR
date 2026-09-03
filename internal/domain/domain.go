package domain

import "time"

type RecordingMode string

const (
	ModeLocal    RecordingMode = "local"
	ModeCloud    RecordingMode = "cloud"
	ModeHybrid   RecordingMode = "hybrid"
	ModeManual   RecordingMode = "manual"
	ModeDisabled RecordingMode = "disabled"
)

func (m RecordingMode) Valid() bool {
	switch m {
	case ModeLocal, ModeCloud, ModeHybrid, ModeManual, ModeDisabled:
		return true
	default:
		return false
	}
}

type TransferPolicy string

const (
	TransferAll             TransferPolicy = "all"
	TransferEvents          TransferPolicy = "events"
	TransferManual          TransferPolicy = "manual"
	TransferEventsAndManual TransferPolicy = "events_and_manual"
	TransferNone            TransferPolicy = "none"
)

func (p TransferPolicy) Valid() bool {
	switch p {
	case TransferAll, TransferEvents, TransferManual, TransferEventsAndManual, TransferNone:
		return true
	default:
		return false
	}
}

type AccessMode string

const (
	AccessAgent  AccessMode = "agent"
	AccessDirect AccessMode = "direct"
)

func (m AccessMode) Valid() bool { return m == AccessAgent || m == AccessDirect }

type Site struct {
	ID                   string        `json:"id"`
	Name                 string        `json:"name"`
	Location             string        `json:"location"`
	DefaultRecordingMode RecordingMode `json:"default_recording_mode"`
	CameraCount          int           `json:"camera_count"`
	AgentStatus          string        `json:"agent_status"`
	AgentLastSeenAt      *time.Time    `json:"agent_last_seen_at,omitempty"`
	StorageOK            bool          `json:"storage_ok"`
	StorageTotalBytes    uint64        `json:"storage_total_bytes"`
	StorageFreeBytes     uint64        `json:"storage_free_bytes"`
	RecordingWorkers     int           `json:"recording_workers"`
	RelayWorkers         int           `json:"relay_workers"`
	AgentHealthError     string        `json:"agent_health_error,omitempty"`
	CreatedAt            time.Time     `json:"created_at"`
}

type AgentHealth struct {
	StorageOK         bool   `json:"storage_ok"`
	StorageTotalBytes uint64 `json:"storage_total_bytes"`
	StorageFreeBytes  uint64 `json:"storage_free_bytes"`
	RecordingWorkers  int    `json:"recording_workers"`
	RelayWorkers      int    `json:"relay_workers"`
	Error             string `json:"error,omitempty"`
}

type Camera struct {
	ID                 string         `json:"id"`
	SiteID             string         `json:"site_id"`
	Name               string         `json:"name"`
	StreamURL          string         `json:"stream_url,omitempty"`
	PublishURL         string         `json:"publish_url,omitempty"`
	AccessMode         AccessMode     `json:"access_mode"`
	RecordingMode      RecordingMode  `json:"recording_mode"`
	LocalRetentionDays uint           `json:"local_retention_days"`
	CloudRetentionDays uint           `json:"cloud_retention_days"`
	TransferPolicy     TransferPolicy `json:"transfer_policy"`
	Enabled            bool           `json:"enabled"`
	ManualRecording    bool           `json:"manual_recording"`
	PTZEnabled         bool           `json:"ptz_enabled"`
	PTZEndpoint        string         `json:"ptz_endpoint,omitempty"`
	PTZUsername        string         `json:"ptz_username,omitempty"`
	PTZPassword        string         `json:"ptz_password,omitempty"`
	CreatedAt          time.Time      `json:"created_at"`
}

type PTZCommand struct {
	ID       string  `json:"id"`
	CameraID string  `json:"camera_id"`
	Action   string  `json:"action"`
	Endpoint string  `json:"endpoint"`
	Username string  `json:"username"`
	Password string  `json:"password"`
	Pan      float64 `json:"pan"`
	Tilt     float64 `json:"tilt"`
	Zoom     float64 `json:"zoom"`
}

type PTZResult struct {
	ID      string `json:"id"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// WebRTCRequest and WebRTCResponse carry WHEP signaling through the agent's
// outbound authenticated connection. The SDP passes through the VPS, while
// ICE media packets travel directly between the viewer and the local agent.
type WebRTCRequest struct {
	ID       string            `json:"id"`
	CameraID string            `json:"camera_id"`
	Method   string            `json:"method"`
	Path     string            `json:"path"`
	Query    string            `json:"query,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Body     []byte            `json:"body,omitempty"`
}

type WebRTCResponse struct {
	ID      string            `json:"id"`
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    []byte            `json:"body,omitempty"`
}

type DeviceSession struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	LastSeenAt time.Time `json:"last_seen_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	CreatedAt  time.Time `json:"created_at"`
}

type Recording struct {
	ID             string     `json:"id"`
	CameraID       string     `json:"camera_id"`
	CameraName     string     `json:"camera_name,omitempty"`
	SiteID         string     `json:"site_id,omitempty"`
	SiteName       string     `json:"site_name,omitempty"`
	Source         string     `json:"source"`
	StorageKey     string     `json:"storage_key,omitempty"`
	StartedAt      time.Time  `json:"started_at"`
	EndedAt        *time.Time `json:"ended_at,omitempty"`
	SizeBytes      uint64     `json:"size_bytes"`
	ChecksumSHA256 string     `json:"checksum_sha256,omitempty"`
	EventType      string     `json:"event_type"`
	PlaybackURL    string     `json:"playback_url,omitempty"`
}
