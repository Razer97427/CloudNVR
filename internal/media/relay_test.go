package media

import "testing"

func TestPublishURL(t *testing.T) {
	got, err := PublishURL("rtsp://media.example:8554", "agent", "p@ss/word", "abc-123")
	if err != nil {
		t.Fatal(err)
	}
	want := "rtsp://agent:p%40ss%2Fword@media.example:8554/camera-abc-123"
	if got != want {
		t.Fatalf("PublishURL() = %q, want %q", got, want)
	}
}

func TestPublishURLWithoutCredentials(t *testing.T) {
	got, err := PublishURL("rtsp://127.0.0.1:8555", "", "", "local")
	if err != nil {
		t.Fatal(err)
	}
	if got != "rtsp://127.0.0.1:8555/camera-local" {
		t.Fatalf("PublishURL() = %q", got)
	}
}

func TestValidatePublishBase(t *testing.T) {
	for _, value := range []string{"", "https://example.com", "rtsp:///missing-host"} {
		if err := ValidatePublishBase(value); err == nil {
			t.Fatalf("ValidatePublishBase(%q) unexpectedly succeeded", value)
		}
	}
	if err := ValidatePublishBase("rtsps://media.example:8322"); err != nil {
		t.Fatal(err)
	}
}
