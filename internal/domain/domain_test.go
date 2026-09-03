package domain

import "testing"

func TestRecordingModes(t *testing.T) {
	valid := []RecordingMode{ModeLocal, ModeCloud, ModeHybrid, ModeManual, ModeDisabled}
	for _, mode := range valid {
		if !mode.Valid() {
			t.Fatalf("expected %q to be valid", mode)
		}
	}
	if RecordingMode("continuous").Valid() {
		t.Fatal("unexpected valid mode")
	}
}

func TestTransferPolicies(t *testing.T) {
	valid := []TransferPolicy{TransferAll, TransferEvents, TransferManual, TransferEventsAndManual, TransferNone}
	for _, policy := range valid {
		if !policy.Valid() {
			t.Fatalf("expected %q to be valid", policy)
		}
	}
	if TransferPolicy("sometimes").Valid() {
		t.Fatal("unexpected valid policy")
	}
}

func TestAccessModes(t *testing.T) {
	if !AccessAgent.Valid() || !AccessDirect.Valid() {
		t.Fatal("expected access modes to be valid")
	}
	if AccessMode("browser").Valid() {
		t.Fatal("unexpected valid access mode")
	}
}
