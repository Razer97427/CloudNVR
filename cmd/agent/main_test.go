package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"cloudnvr/internal/domain"
)

func TestProxyLocalWebRTCForwardsWHEP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/camera-camera-1/whep" || r.URL.RawQuery != "token=value" {
			t.Fatalf("unexpected local request: %s %s", r.Method, r.URL.String())
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != "offer" || r.Header.Get("Content-Type") != "application/sdp" {
			t.Fatalf("unexpected local request payload: %q", body)
		}
		w.Header().Set("Content-Type", "application/sdp")
		w.Header().Set("Location", "/camera-camera-1/whep/session-1")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("answer"))
	}))
	defer server.Close()

	response := proxyLocalWebRTC(context.Background(), server.URL, domain.WebRTCRequest{
		ID: "request-1", CameraID: "camera-1", Method: http.MethodPost, Path: "/camera-camera-1/whep", Query: "token=value",
		Headers: map[string]string{"Content-Type": "application/sdp"}, Body: []byte("offer"),
	})
	if response.Status != http.StatusCreated || string(response.Body) != "answer" || response.Headers["Location"] == "" {
		t.Fatalf("unexpected proxy response: %#v", response)
	}
}

func TestProxyLocalWebRTCRejectsAnotherCameraPath(t *testing.T) {
	response := proxyLocalWebRTC(context.Background(), "http://127.0.0.1:8890", domain.WebRTCRequest{
		ID: "request-1", CameraID: "camera-1", Method: http.MethodGet, Path: "/camera-camera-2",
	})
	if response.Status != http.StatusBadRequest {
		t.Fatalf("unexpected status: %d", response.Status)
	}
}
