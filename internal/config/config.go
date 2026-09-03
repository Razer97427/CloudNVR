package config

import (
	"errors"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Cloud struct {
	HTTPAddr              string
	DatabaseDSN           string
	AdminAPIKey           string
	CameraEncryptionKey   string
	PublicURL             string
	WebUpstream           string
	MediaWebRTCUpstream   string
	MediaHLSUpstream      string
	MediaAPIURL           string
	MediaInternalRTSPURL  string
	MediaPublicRTSPURL    string
	MediaPublishUser      string
	MediaPublishPassword  string
	RecordingsPath        string
	StorageIdentityFile   string
	RecordingSegmentTime  time.Duration
	PlaybackCacheTTL      time.Duration
	PlaybackCacheMaxBytes int64
	AgentUploadMaxBytes   int64
	MinimumFreeBytes      uint64
}

func CloudFromEnv() (Cloud, error) {
	c := Cloud{
		HTTPAddr:             value("HTTP_ADDR", ":8080"),
		DatabaseDSN:          os.Getenv("DATABASE_DSN"),
		AdminAPIKey:          os.Getenv("ADMIN_API_KEY"),
		CameraEncryptionKey:  os.Getenv("CAMERA_ENCRYPTION_KEY"),
		PublicURL:            value("PUBLIC_URL", "http://localhost:8080"),
		WebUpstream:          os.Getenv("WEB_UPSTREAM"),
		MediaWebRTCUpstream:  value("MEDIA_WEBRTC_UPSTREAM", "http://mediamtx:8889"),
		MediaHLSUpstream:     value("MEDIA_HLS_UPSTREAM", "http://mediamtx:8888"),
		MediaAPIURL:          value("MEDIA_API_URL", "http://mediamtx:9997"),
		MediaInternalRTSPURL: value("MEDIA_INTERNAL_RTSP_URL", "rtsp://mediamtx:8554"),
		MediaPublicRTSPURL:   os.Getenv("MEDIA_PUBLIC_RTSP_URL"),
		MediaPublishUser:     value("MEDIA_PUBLISH_USER", "cloudnvr-agent"),
		MediaPublishPassword: value("MEDIA_PUBLISH_PASSWORD", "change-media-password"),
		RecordingsPath:       value("RECORDINGS_PATH", "/var/lib/cloudnvr/recordings"),
		StorageIdentityFile:  value("CLOUD_STORAGE_ID_FILE", "/var/lib/cloudnvr-state/storage.id"),
	}
	segmentTime, err := time.ParseDuration(value("RECORDING_SEGMENT_TIME", "1m"))
	if err != nil || segmentTime < 10*time.Second {
		return Cloud{}, errors.New("RECORDING_SEGMENT_TIME must be at least 10s")
	}
	c.RecordingSegmentTime = segmentTime
	cacheTTL, err := time.ParseDuration(value("PLAYBACK_CACHE_TTL", "6h"))
	if err != nil || cacheTTL < time.Minute {
		return Cloud{}, errors.New("PLAYBACK_CACHE_TTL must be at least 1m")
	}
	c.PlaybackCacheTTL = cacheTTL
	c.PlaybackCacheMaxBytes, err = byteSizeFromEnv("PLAYBACK_CACHE_MAX_BYTES", 10<<30)
	if err != nil {
		return Cloud{}, err
	}
	c.AgentUploadMaxBytes, err = byteSizeFromEnv("AGENT_UPLOAD_MAX_BYTES", 2<<30)
	if err != nil {
		return Cloud{}, err
	}
	if c.PlaybackCacheMaxBytes < c.AgentUploadMaxBytes {
		return Cloud{}, errors.New("PLAYBACK_CACHE_MAX_BYTES must be at least AGENT_UPLOAD_MAX_BYTES")
	}
	c.MinimumFreeBytes, err = nonNegativeByteSizeFromEnv("CLOUD_MIN_FREE_BYTES", 5<<30)
	if err != nil {
		return Cloud{}, err
	}
	if c.DatabaseDSN == "" || c.AdminAPIKey == "" || c.CameraEncryptionKey == "" {
		return Cloud{}, errors.New("DATABASE_DSN, ADMIN_API_KEY and CAMERA_ENCRYPTION_KEY are required")
	}
	if c.MediaPublicRTSPURL == "" {
		c.MediaPublicRTSPURL = publicRTSPURL(c.PublicURL)
	}
	return c, nil
}

func publicRTSPURL(publicURL string) string {
	u, err := url.Parse(publicURL)
	if err != nil || u.Hostname() == "" {
		return "rtsp://localhost:8554"
	}
	host := u.Hostname()
	if strings.Contains(host, ":") {
		host = net.JoinHostPort(host, "8554")
	} else {
		host += ":8554"
	}
	return "rtsp://" + host
}

type Agent struct {
	CloudURL              string
	SiteID                string
	EnrollmentToken       string
	Name                  string
	StateFile             string
	ConfigCacheFile       string
	InventoryStateFile    string
	PollInterval          time.Duration
	InventoryInterval     time.Duration
	FullInventoryInterval time.Duration
	RecordingsPath        string
	StorageIdentityFile   string
	SegmentTime           time.Duration
	UploadMbps            float64
	UploadRetries         int
	AllowInsecureHTTP     bool
	WebRTCEnabled         bool
	MediaRTSPURL          string
	MediaWebRTCURL        string
	WebRTCWorkers         int
	MinimumFreeBytes      uint64
}

func AgentFromEnv() (Agent, error) {
	interval, err := time.ParseDuration(value("AGENT_POLL_INTERVAL", "15s"))
	if err != nil {
		return Agent{}, err
	}
	segmentTime, err := time.ParseDuration(value("RECORDING_SEGMENT_TIME", "1m"))
	if err != nil || segmentTime < 10*time.Second {
		return Agent{}, errors.New("RECORDING_SEGMENT_TIME must be at least 10s")
	}
	inventoryInterval, err := time.ParseDuration(value("AGENT_INVENTORY_INTERVAL", "1m"))
	if err != nil || inventoryInterval < 15*time.Second {
		return Agent{}, errors.New("AGENT_INVENTORY_INTERVAL must be at least 15s")
	}
	fullInventoryInterval, err := time.ParseDuration(value("AGENT_FULL_INVENTORY_INTERVAL", "6h"))
	if err != nil || fullInventoryInterval < inventoryInterval {
		return Agent{}, errors.New("AGENT_FULL_INVENTORY_INTERVAL must be at least AGENT_INVENTORY_INTERVAL")
	}
	uploadMbps, err := strconv.ParseFloat(value("AGENT_UPLOAD_MBPS", "8"), 64)
	if err != nil || uploadMbps < 0 {
		return Agent{}, errors.New("AGENT_UPLOAD_MBPS must be zero or greater")
	}
	uploadRetries, err := strconv.Atoi(value("AGENT_UPLOAD_RETRIES", "4"))
	if err != nil || uploadRetries < 1 || uploadRetries > 20 {
		return Agent{}, errors.New("AGENT_UPLOAD_RETRIES must be between 1 and 20")
	}
	allowInsecure, err := strconv.ParseBool(value("AGENT_ALLOW_INSECURE_HTTP", "false"))
	if err != nil {
		return Agent{}, errors.New("AGENT_ALLOW_INSECURE_HTTP must be true or false")
	}
	webRTCEnabled, err := strconv.ParseBool(value("AGENT_WEBRTC_ENABLED", "false"))
	if err != nil {
		return Agent{}, errors.New("AGENT_WEBRTC_ENABLED must be true or false")
	}
	webRTCWorkers, err := strconv.Atoi(value("AGENT_WEBRTC_WORKERS", "4"))
	if err != nil || webRTCWorkers < 1 || webRTCWorkers > 16 {
		return Agent{}, errors.New("AGENT_WEBRTC_WORKERS must be between 1 and 16")
	}
	c := Agent{
		CloudURL:              os.Getenv("CLOUD_URL"),
		SiteID:                os.Getenv("SITE_ID"),
		EnrollmentToken:       os.Getenv("ENROLLMENT_TOKEN"),
		Name:                  value("AGENT_NAME", "site-agent"),
		StateFile:             value("AGENT_STATE_FILE", "/var/lib/cloudnvr/agent.json"),
		ConfigCacheFile:       value("AGENT_CONFIG_CACHE_FILE", "/var/lib/cloudnvr/config.json"),
		InventoryStateFile:    value("AGENT_INVENTORY_STATE_FILE", "/var/lib/cloudnvr/inventory.json"),
		PollInterval:          interval,
		InventoryInterval:     inventoryInterval,
		FullInventoryInterval: fullInventoryInterval,
		RecordingsPath:        value("RECORDINGS_PATH", "/var/lib/cloudnvr/recordings"),
		StorageIdentityFile:   value("AGENT_STORAGE_ID_FILE", "/var/lib/cloudnvr/storage.id"),
		SegmentTime:           segmentTime,
		UploadMbps:            uploadMbps,
		UploadRetries:         uploadRetries,
		AllowInsecureHTTP:     allowInsecure,
		WebRTCEnabled:         webRTCEnabled,
		MediaRTSPURL:          value("AGENT_MEDIA_RTSP_URL", "rtsp://127.0.0.1:8555"),
		MediaWebRTCURL:        value("AGENT_MEDIA_WEBRTC_URL", "http://127.0.0.1:8890"),
		WebRTCWorkers:         webRTCWorkers,
	}
	minimumFreeBytes, err := nonNegativeByteSizeFromEnv("AGENT_MIN_FREE_BYTES", 2<<30)
	if err != nil {
		return Agent{}, err
	}
	c.MinimumFreeBytes = minimumFreeBytes
	if c.CloudURL == "" || c.SiteID == "" {
		return Agent{}, errors.New("CLOUD_URL and SITE_ID are required")
	}
	cloudURL, err := url.Parse(c.CloudURL)
	if err != nil || cloudURL.Host == "" || (cloudURL.Scheme != "https" && cloudURL.Scheme != "http") {
		return Agent{}, errors.New("CLOUD_URL must be an http or https URL")
	}
	if cloudURL.Scheme != "https" && !c.AllowInsecureHTTP && !isPrivateHost(cloudURL.Hostname()) {
		return Agent{}, errors.New("public CLOUD_URL must use HTTPS (or explicitly set AGENT_ALLOW_INSECURE_HTTP=true)")
	}
	if c.WebRTCEnabled {
		if err := validateServiceURL(c.MediaRTSPURL, "rtsp", "rtsps"); err != nil {
			return Agent{}, errors.New("AGENT_MEDIA_RTSP_URL must be an rtsp or rtsps URL")
		}
		if err := validateServiceURL(c.MediaWebRTCURL, "http", "https"); err != nil {
			return Agent{}, errors.New("AGENT_MEDIA_WEBRTC_URL must be an http or https URL")
		}
	}
	return c, nil
}

func validateServiceURL(value string, schemes ...string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" {
		return errors.New("invalid service URL")
	}
	for _, scheme := range schemes {
		if parsed.Scheme == scheme {
			return nil
		}
	}
	return errors.New("invalid service URL scheme")
}

func byteSizeFromEnv(name string, fallback int64) (int64, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || parsed < 1<<20 {
		return 0, errors.New(name + " must be an integer of at least 1048576 bytes")
	}
	return parsed, nil
}

func nonNegativeByteSizeFromEnv(name string, fallback uint64) (uint64, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, errors.New(name + " must be a non-negative integer")
	}
	return parsed, nil
}

func isPrivateHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

func value(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
