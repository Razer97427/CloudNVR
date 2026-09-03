package security

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestCipherRoundTrip(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	c, err := NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	want := "rtsp://user:secret@192.168.1.20:554/stream"
	gotEncrypted, err := c.Encrypt(want)
	if err != nil {
		t.Fatal(err)
	}
	if gotEncrypted == want || strings.Contains(gotEncrypted, "secret") {
		t.Fatal("ciphertext leaks plaintext")
	}
	got, err := c.Decrypt(gotEncrypted)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
