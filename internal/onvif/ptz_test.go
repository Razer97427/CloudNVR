package onvif

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMoveDiscoversProfileMovesAndStops(t *testing.T) {
	var calls atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, _ := io.ReadAll(r.Body)
		body := string(payload)
		if !strings.Contains(body, "UsernameToken") {
			t.Error("authenticated request has no WS-Security UsernameToken")
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/soap+xml")
		switch {
		case strings.Contains(body, "GetCapabilities"):
			_, _ = io.WriteString(w, `<Envelope><Body><GetCapabilitiesResponse><Capabilities><Media><XAddr>`+server.URL+`</XAddr></Media><PTZ><XAddr>`+server.URL+`</XAddr></PTZ></Capabilities></GetCapabilitiesResponse></Body></Envelope>`)
		case strings.Contains(body, "GetProfiles"):
			_, _ = io.WriteString(w, `<Envelope><Body><GetProfilesResponse><Profiles token="profile-main"/></GetProfilesResponse></Body></Envelope>`)
		case strings.Contains(body, "ContinuousMove"):
			if !strings.Contains(body, `x="0.500"`) || !strings.Contains(body, "profile-main") {
				t.Errorf("unexpected move payload: %s", body)
			}
			if strings.Contains(body, "<tt:Zoom") || strings.Contains(body, "<tptz:Timeout") {
				t.Errorf("unused Tapo-incompatible fields found: %s", body)
			}
			_, _ = io.WriteString(w, `<Envelope><Body><ContinuousMoveResponse/></Body></Envelope>`)
		case strings.Contains(body, "<tptz:Stop"):
			if strings.Contains(body, "<tptz:Zoom") {
				t.Errorf("unused zoom stop found: %s", body)
			}
			_, _ = io.WriteString(w, `<Envelope><Body><StopResponse/></Body></Envelope>`)
		default:
			http.Error(w, "unexpected SOAP request", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	if err := Move(context.Background(), server.URL, "operator", "secret", .5, 0, 0); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 4 {
		t.Fatalf("got %d SOAP calls, want 4", calls.Load())
	}
}

func TestHomePositionCommands(t *testing.T) {
	var setHome, gotoHome atomic.Bool
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, _ := io.ReadAll(r.Body)
		body := string(payload)
		w.Header().Set("Content-Type", "application/soap+xml")
		switch {
		case strings.Contains(body, "GetCapabilities"):
			_, _ = io.WriteString(w, `<Envelope><Body><GetCapabilitiesResponse><Capabilities><Media><XAddr>`+server.URL+`</XAddr></Media><PTZ><XAddr>`+server.URL+`</XAddr></PTZ></Capabilities></GetCapabilitiesResponse></Body></Envelope>`)
		case strings.Contains(body, "GetProfiles"):
			_, _ = io.WriteString(w, `<Envelope><Body><GetProfilesResponse><Profiles token="profile-main"/></GetProfilesResponse></Body></Envelope>`)
		case strings.Contains(body, "SetHomePosition"):
			setHome.Store(true)
			_, _ = io.WriteString(w, `<Envelope><Body><SetHomePositionResponse/></Body></Envelope>`)
		case strings.Contains(body, "GotoHomePosition"):
			gotoHome.Store(true)
			_, _ = io.WriteString(w, `<Envelope><Body><GotoHomePositionResponse/></Body></Envelope>`)
		default:
			http.Error(w, "unexpected SOAP request", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	if err := SetHome(context.Background(), server.URL, "operator", "secret"); err != nil {
		t.Fatal(err)
	}
	if err := GotoHome(context.Background(), server.URL, "operator", "secret"); err != nil {
		t.Fatal(err)
	}
	if !setHome.Load() || !gotoHome.Load() {
		t.Fatalf("home calls missing: set=%v goto=%v", setHome.Load(), gotoHome.Load())
	}
}

func TestHomePositionFallsBackToNamedPreset(t *testing.T) {
	var created, moved atomic.Bool
	var presetExists atomic.Bool
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, _ := io.ReadAll(r.Body)
		body := string(payload)
		w.Header().Set("Content-Type", "application/soap+xml")
		switch {
		case strings.Contains(body, "GetCapabilities"):
			_, _ = io.WriteString(w, `<Envelope><Body><GetCapabilitiesResponse><Capabilities><Media><XAddr>`+server.URL+`</XAddr></Media><PTZ><XAddr>`+server.URL+`</XAddr></PTZ></Capabilities></GetCapabilitiesResponse></Body></Envelope>`)
		case strings.Contains(body, "GetProfiles"):
			_, _ = io.WriteString(w, `<Envelope><Body><GetProfilesResponse><Profiles token="profile-main"/></GetProfilesResponse></Body></Envelope>`)
		case strings.Contains(body, "SetHomePosition"), strings.Contains(body, "GotoHomePosition"):
			http.Error(w, "native home unsupported", http.StatusInternalServerError)
		case strings.Contains(body, "GetPresets"):
			presets := ""
			if presetExists.Load() {
				presets = `<Preset token="preset-7"><Name>CloudNVR Home</Name></Preset>`
			}
			_, _ = io.WriteString(w, `<Envelope><Body><GetPresetsResponse>`+presets+`</GetPresetsResponse></Body></Envelope>`)
		case strings.Contains(body, "SetPreset"):
			if !strings.Contains(body, "CloudNVR Home") {
				t.Errorf("preset name missing: %s", body)
			}
			presetExists.Store(true)
			created.Store(true)
			_, _ = io.WriteString(w, `<Envelope><Body><SetPresetResponse><PresetToken>preset-7</PresetToken></SetPresetResponse></Body></Envelope>`)
		case strings.Contains(body, "GotoPreset"):
			if !strings.Contains(body, "preset-7") {
				t.Errorf("preset token missing: %s", body)
			}
			moved.Store(true)
			_, _ = io.WriteString(w, `<Envelope><Body><GotoPresetResponse/></Body></Envelope>`)
		default:
			http.Error(w, "unexpected SOAP request", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	if err := SetHome(context.Background(), server.URL, "operator", "secret"); err != nil {
		t.Fatal(err)
	}
	if err := GotoHome(context.Background(), server.URL, "operator", "secret"); err != nil {
		t.Fatal(err)
	}
	if !created.Load() || !moved.Load() {
		t.Fatalf("preset fallback calls missing: created=%v moved=%v", created.Load(), moved.Load())
	}
}
