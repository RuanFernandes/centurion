package codex

import (
	"context"
	"testing"
	"time"

	"github.com/RuanFernandes/centurion/internal/model"
)

func TestAppServerLifecycleWithFakeCodexProcess(t *testing.T) {
	server := newAppServerWithClient(newRawClient("go", "run", "./testdata/fakeappserver"), nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Stop() })

	if state := server.AuthState(); state.Status != model.AuthStatusLoggedIn {
		t.Fatalf("fake server did not provide managed auth state: %#v", state)
	}
	models := server.Models()
	if len(models) != 1 || models[0].ID != "fake-model" {
		t.Fatalf("fake model catalog was not loaded: %#v", models)
	}
	result, err := server.RunAgentTurn(ctx, model.AgentProfile{ID: "fake-agent", Name: "Fake", Role: "Tester", ModelID: "fake-model", ApprovalProfile: "on_request"}, "Say hello", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.ThreadID != "fake-thread-1" || result.Output != "fake response" || result.Status != "completed" {
		t.Fatalf("unexpected fake turn result: %#v", result)
	}
}
