package onvif

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1" // #nosec G505 -- SHA-1 is mandated by the ONVIF UsernameToken profile.
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	deviceAction     = "http://www.onvif.org/ver10/device/wsdl/GetCapabilities"
	profilesAction   = "http://www.onvif.org/ver10/media/wsdl/GetProfiles"
	moveAction       = "http://www.onvif.org/ver20/ptz/wsdl/ContinuousMove"
	stopAction       = "http://www.onvif.org/ver20/ptz/wsdl/Stop"
	setHomeAction    = "http://www.onvif.org/ver20/ptz/wsdl/SetHomePosition"
	gotoHomeAction   = "http://www.onvif.org/ver20/ptz/wsdl/GotoHomePosition"
	getPresetsAction = "http://www.onvif.org/ver20/ptz/wsdl/GetPresets"
	setPresetAction  = "http://www.onvif.org/ver20/ptz/wsdl/SetPreset"
	gotoPresetAction = "http://www.onvif.org/ver20/ptz/wsdl/GotoPreset"
	homePresetName   = "CloudNVR Home"
)

type capabilitiesEnvelope struct {
	Body struct {
		Response struct {
			Capabilities struct {
				Media struct {
					XAddr string `xml:"XAddr"`
				} `xml:"Media"`
				PTZ struct {
					XAddr string `xml:"XAddr"`
				} `xml:"PTZ"`
			} `xml:"Capabilities"`
		} `xml:"GetCapabilitiesResponse"`
	} `xml:"Body"`
}

type profilesEnvelope struct {
	Body struct {
		Response struct {
			Profiles []struct {
				Token string `xml:"token,attr"`
			} `xml:"Profiles"`
		} `xml:"GetProfilesResponse"`
	} `xml:"Body"`
}

type ptzService struct {
	URL          string
	ProfileToken string
}

type presetsEnvelope struct {
	Body struct {
		Response struct {
			Presets []struct {
				Token string `xml:"token,attr"`
				Name  string `xml:"Name"`
			} `xml:"Preset"`
		} `xml:"GetPresetsResponse"`
	} `xml:"Body"`
}

// Move performs a short ONVIF ContinuousMove followed by Stop. Short moves are
// deliberate: a lost browser pointer-up or network response cannot leave a
// camera moving indefinitely.
func Move(ctx context.Context, endpoint, username, password string, pan, tilt, zoom float64) error {
	if strings.TrimSpace(endpoint) == "" {
		return fmt.Errorf("ONVIF endpoint is empty")
	}
	if pan < -1 || pan > 1 || tilt < -1 || tilt > 1 || zoom < -1 || zoom > 1 {
		return fmt.Errorf("PTZ velocity must be between -1 and 1")
	}
	if pan == 0 && tilt == 0 && zoom == 0 {
		return fmt.Errorf("PTZ movement is empty")
	}
	service, err := discover(ctx, endpoint, username, password)
	if err != nil {
		return err
	}
	profile := escape(service.ProfileToken)
	velocity := ""
	if pan != 0 || tilt != 0 {
		velocity += fmt.Sprintf(`<tt:PanTilt x="%.3f" y="%.3f"/>`, pan, tilt)
	}
	if zoom != 0 {
		velocity += fmt.Sprintf(`<tt:Zoom x="%.3f"/>`, zoom)
	}
	// Several Profile S cameras, including some Tapo firmwares, reject an
	// unused Zoom vector and the optional Timeout instead of returning a SOAP
	// fault. Keep the movement request to the axes that are actually used and
	// always stop it explicitly below.
	body := fmt.Sprintf(`<tptz:ContinuousMove xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema"><tptz:ProfileToken>%s</tptz:ProfileToken><tptz:Velocity>%s</tptz:Velocity></tptz:ContinuousMove>`, profile, velocity)
	if _, err := call(ctx, service.URL, moveAction, body, username, password); err != nil {
		return fmt.Errorf("ONVIF move: %w", err)
	}
	timer := time.NewTimer(350 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}
	stopAxes := ""
	if pan != 0 || tilt != 0 {
		stopAxes += `<tptz:PanTilt>true</tptz:PanTilt>`
	}
	if zoom != 0 {
		stopAxes += `<tptz:Zoom>true</tptz:Zoom>`
	}
	stop := fmt.Sprintf(`<tptz:Stop xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl"><tptz:ProfileToken>%s</tptz:ProfileToken>%s</tptz:Stop>`, profile, stopAxes)
	if _, err := call(ctx, service.URL, stopAction, stop, username, password); err != nil {
		return fmt.Errorf("ONVIF stop: %w", err)
	}
	return nil
}

// SetHome stores the current position as the camera's native ONVIF home
// position. The position is persisted by the camera rather than CloudNVR.
func SetHome(ctx context.Context, endpoint, username, password string) error {
	service, err := discover(ctx, endpoint, username, password)
	if err != nil {
		return err
	}
	body := fmt.Sprintf(`<tptz:SetHomePosition xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl"><tptz:ProfileToken>%s</tptz:ProfileToken></tptz:SetHomePosition>`, escape(service.ProfileToken))
	if _, err := call(ctx, service.URL, setHomeAction, body, username, password); err == nil {
		return nil
	}
	// Some Profile S cameras (notably Tapo models) expose PTZ but reject the
	// optional native Home operation. A named preset provides the same stable
	// user-facing behaviour and remains stored in the camera.
	return setNamedHomePreset(ctx, service, username, password)
}

// GotoHome moves the camera to its native ONVIF home position.
func GotoHome(ctx context.Context, endpoint, username, password string) error {
	service, err := discover(ctx, endpoint, username, password)
	if err != nil {
		return err
	}
	body := fmt.Sprintf(`<tptz:GotoHomePosition xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl"><tptz:ProfileToken>%s</tptz:ProfileToken></tptz:GotoHomePosition>`, escape(service.ProfileToken))
	if _, err := call(ctx, service.URL, gotoHomeAction, body, username, password); err == nil {
		return nil
	}
	return gotoNamedHomePreset(ctx, service, username, password)
}

func setNamedHomePreset(ctx context.Context, service ptzService, username, password string) error {
	presets, err := getPresets(ctx, service, username, password)
	if err != nil {
		return fmt.Errorf("ONVIF set home fallback: %w", err)
	}
	token := namedPresetToken(presets)
	tokenXML := ""
	if token != "" {
		tokenXML = `<tptz:PresetToken>` + escape(token) + `</tptz:PresetToken>`
	}
	body := fmt.Sprintf(`<tptz:SetPreset xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl"><tptz:ProfileToken>%s</tptz:ProfileToken><tptz:PresetName>%s</tptz:PresetName>%s</tptz:SetPreset>`, escape(service.ProfileToken), escape(homePresetName), tokenXML)
	if _, err := call(ctx, service.URL, setPresetAction, body, username, password); err != nil {
		return fmt.Errorf("ONVIF set home fallback: %w", err)
	}
	return nil
}

func gotoNamedHomePreset(ctx context.Context, service ptzService, username, password string) error {
	presets, err := getPresets(ctx, service, username, password)
	if err != nil {
		return fmt.Errorf("ONVIF go home fallback: %w", err)
	}
	token := namedPresetToken(presets)
	if token == "" {
		return fmt.Errorf("ONVIF home position is not configured; use Set Home first")
	}
	body := fmt.Sprintf(`<tptz:GotoPreset xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl"><tptz:ProfileToken>%s</tptz:ProfileToken><tptz:PresetToken>%s</tptz:PresetToken></tptz:GotoPreset>`, escape(service.ProfileToken), escape(token))
	if _, err := call(ctx, service.URL, gotoPresetAction, body, username, password); err != nil {
		return fmt.Errorf("ONVIF go home fallback: %w", err)
	}
	return nil
}

func getPresets(ctx context.Context, service ptzService, username, password string) (presetsEnvelope, error) {
	body := fmt.Sprintf(`<tptz:GetPresets xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl"><tptz:ProfileToken>%s</tptz:ProfileToken></tptz:GetPresets>`, escape(service.ProfileToken))
	data, err := call(ctx, service.URL, getPresetsAction, body, username, password)
	if err != nil {
		return presetsEnvelope{}, err
	}
	var presets presetsEnvelope
	if err := xml.Unmarshal(data, &presets); err != nil {
		return presetsEnvelope{}, err
	}
	return presets, nil
}

func namedPresetToken(presets presetsEnvelope) string {
	for _, preset := range presets.Body.Response.Presets {
		if strings.EqualFold(strings.TrimSpace(preset.Name), homePresetName) {
			return strings.TrimSpace(preset.Token)
		}
	}
	return ""
}

func discover(ctx context.Context, endpoint, username, password string) (ptzService, error) {
	capabilitiesXML, err := call(ctx, endpoint, deviceAction,
		`<tds:GetCapabilities xmlns:tds="http://www.onvif.org/ver10/device/wsdl"><tds:Category>All</tds:Category></tds:GetCapabilities>`, username, password)
	if err != nil {
		return ptzService{}, fmt.Errorf("ONVIF discovery: %w", err)
	}
	var capabilities capabilitiesEnvelope
	if err := xml.Unmarshal(capabilitiesXML, &capabilities); err != nil {
		return ptzService{}, fmt.Errorf("ONVIF capabilities: %w", err)
	}
	mediaURL := strings.TrimSpace(capabilities.Body.Response.Capabilities.Media.XAddr)
	ptzURL := strings.TrimSpace(capabilities.Body.Response.Capabilities.PTZ.XAddr)
	if mediaURL == "" || ptzURL == "" {
		return ptzService{}, fmt.Errorf("camera does not advertise ONVIF media and PTZ services")
	}
	profilesXML, err := call(ctx, mediaURL, profilesAction,
		`<trt:GetProfiles xmlns:trt="http://www.onvif.org/ver10/media/wsdl"/>`, username, password)
	if err != nil {
		return ptzService{}, fmt.Errorf("ONVIF profiles: %w", err)
	}
	var profiles profilesEnvelope
	if err := xml.Unmarshal(profilesXML, &profiles); err != nil || len(profiles.Body.Response.Profiles) == 0 || profiles.Body.Response.Profiles[0].Token == "" {
		return ptzService{}, fmt.Errorf("camera has no usable ONVIF media profile")
	}
	return ptzService{URL: ptzURL, ProfileToken: profiles.Body.Response.Profiles[0].Token}, nil
}

func call(ctx context.Context, endpoint, action, body, username, password string) ([]byte, error) {
	header, err := securityHeader(username, password)
	if err != nil {
		return nil, err
	}
	envelope := `<?xml version="1.0" encoding="UTF-8"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">` + header + `<s:Body>` + body + `</s:Body></s:Envelope>`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewBufferString(envelope))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", `application/soap+xml; charset=utf-8; action="`+action+`"`)
	req.Close = true
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if readErr != nil {
		return nil, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("camera returned %s", resp.Status)
	}
	if bytes.Contains(data, []byte(":Fault>")) || bytes.Contains(data, []byte("<Fault>")) {
		return nil, fmt.Errorf("camera returned a SOAP fault")
	}
	return data, nil
}

func securityHeader(username, password string) (string, error) {
	if username == "" {
		return "", nil
	}
	nonce := make([]byte, 20)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	created := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	digestInput := append(append(append([]byte{}, nonce...), []byte(created)...), []byte(password)...)
	digest := sha1.Sum(digestInput) // #nosec G401 -- required by ONVIF WS-Security.
	return fmt.Sprintf(`<s:Header><wsse:Security s:mustUnderstand="1" xmlns:wsse="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd" xmlns:wsu="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-utility-1.0.xsd"><wsse:UsernameToken><wsse:Username>%s</wsse:Username><wsse:Password Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordDigest">%s</wsse:Password><wsse:Nonce EncodingType="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-soap-message-security-1.0#Base64Binary">%s</wsse:Nonce><wsu:Created>%s</wsu:Created></wsse:UsernameToken></wsse:Security></s:Header>`,
		escape(username), base64.StdEncoding.EncodeToString(digest[:]), base64.StdEncoding.EncodeToString(nonce), created), nil
}

func escape(value string) string {
	var result strings.Builder
	_ = xml.EscapeText(&result, []byte(value))
	return result.String()
}
