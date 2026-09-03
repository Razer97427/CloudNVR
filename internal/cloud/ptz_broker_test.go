package cloud

import (
	"context"
	"testing"

	"cloudnvr/internal/domain"
)

func TestPTZBrokerDeliversAgentResult(t *testing.T) {
	broker := newPTZBroker()
	results := broker.enqueue("site-1", domain.PTZCommand{ID: "command-1"})
	command, ok := broker.next(context.Background(), "site-1")
	if !ok || command.ID != "command-1" {
		t.Fatalf("unexpected command: %#v", command)
	}
	if !broker.resolve("site-1", domain.PTZResult{ID: command.ID, Success: false, Error: "camera unavailable"}) {
		t.Fatal("result was not accepted")
	}
	result := <-results
	if result.Success || result.Error != "camera unavailable" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestPTZBrokerRejectsResultFromAnotherSite(t *testing.T) {
	broker := newPTZBroker()
	broker.enqueue("site-1", domain.PTZCommand{ID: "command-1"})
	if broker.resolve("site-2", domain.PTZResult{ID: "command-1", Success: true}) {
		t.Fatal("cross-site result was accepted")
	}
}
