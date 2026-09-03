package cloud

import (
	"context"
	"testing"
	"time"

	"cloudnvr/internal/domain"
)

func TestWebRTCBrokerExchangesOnlyWithinSite(t *testing.T) {
	broker := newWebRTCBroker()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan domain.WebRTCResponse, 1)
	go func() {
		response, ok := broker.exchange(ctx, "site-a", domain.WebRTCRequest{ID: "request-1", CameraID: "camera-1"})
		if ok {
			done <- response
		}
	}()
	request, ok := broker.next(ctx, "site-a")
	if !ok || request.ID != "request-1" {
		t.Fatalf("unexpected request: %#v, %v", request, ok)
	}
	if broker.resolve("site-b", domain.WebRTCResponse{ID: request.ID, Status: 200}) {
		t.Fatal("response from another site was accepted")
	}
	if !broker.resolve("site-a", domain.WebRTCResponse{ID: request.ID, Status: 201, Body: []byte("answer")}) {
		t.Fatal("valid response was rejected")
	}
	select {
	case response := <-done:
		if response.Status != 201 || string(response.Body) != "answer" {
			t.Fatalf("unexpected response: %#v", response)
		}
	case <-ctx.Done():
		t.Fatal("exchange timed out")
	}
}
