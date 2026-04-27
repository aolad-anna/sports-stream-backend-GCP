package main

import "testing"

func TestEventFingerprint_Deterministic(t *testing.T) {
	a := eventFingerprint("viewer_join", "stream_1", "user_1", "2026-04-27T10:00:00Z")
	b := eventFingerprint("viewer_join", "stream_1", "user_1", "2026-04-27T10:00:00Z")

	if a != b {
		t.Fatalf("expected deterministic fingerprint, got %q and %q", a, b)
	}
}

func TestEventFingerprint_ChangesWithInput(t *testing.T) {
	a := eventFingerprint("viewer_join", "stream_1", "user_1", "2026-04-27T10:00:00Z")
	b := eventFingerprint("viewer_join", "stream_1", "user_1", "2026-04-27T10:00:01Z")

	if a == b {
		t.Fatalf("expected different fingerprints for different inputs")
	}
}
