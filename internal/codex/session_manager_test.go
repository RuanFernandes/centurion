package codex

import "testing"

func TestSessionManagerLimitsLogicalAgentSessions(t *testing.T) {
	manager := NewSessionManager(1)
	first, err := manager.Open("agent-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Open("agent-b", ""); err == nil {
		t.Fatal("expected the logical session limit to reject a second active session")
	}
	if manager.ActiveCount() != 1 || manager.MaxActive() != 1 {
		t.Fatalf("unexpected session counts: active=%d max=%d", manager.ActiveCount(), manager.MaxActive())
	}
	manager.Close(first.ID)
	if _, err := manager.Open("agent-b", ""); err != nil {
		t.Fatalf("expected a closed session slot to be reusable: %v", err)
	}
}
