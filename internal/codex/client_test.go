package codex

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

func TestClientCorrelatesOutOfOrderResponses(t *testing.T) {
	client := NewClient("codex")
	client.done = make(chan struct{})
	first := make(chan rpcResponse, 1)
	second := make(chan rpcResponse, 1)
	client.pending["1"] = first
	client.pending["2"] = second

	client.handleLine([]byte(`{"id":2,"result":{"value":"second"}}`))
	client.handleLine([]byte(`{"id":1,"result":{"value":"first"}}`))

	if got := <-first; string(got.Result) != `{"value":"first"}` {
		t.Fatalf("first response was correlated incorrectly: %s", got.Result)
	}
	if got := <-second; string(got.Result) != `{"value":"second"}` {
		t.Fatalf("second response was correlated incorrectly: %s", got.Result)
	}
}

func TestClientReadsFragmentedJSONL(t *testing.T) {
	client := NewClient("codex")
	client.done = make(chan struct{})
	response := make(chan rpcResponse, 1)
	client.pending["7"] = response
	reader, writer := io.Pipe()
	finished := make(chan struct{})
	go func() {
		client.readStdout(reader)
		close(finished)
	}()
	_, _ = writer.Write([]byte(`{"id":7,"res`))
	_, _ = writer.Write([]byte(`ult":{"ok":true}}` + "\n"))

	select {
	case got := <-response:
		var result struct {
			OK bool `json:"ok"`
		}
		if err := json.Unmarshal(got.Result, &result); err != nil {
			t.Fatal(err)
		}
		if !result.OK {
			t.Fatal("fragmented response did not decode")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for fragmented response")
	}
	_ = writer.Close()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("stdout reader did not stop")
	}
}

func TestClientPublishesNotificationsAndServerRequests(t *testing.T) {
	client := NewClient("codex")
	client.done = make(chan struct{})
	notifications, cancel := client.Subscribe()
	defer cancel()
	requestReceived := make(chan ServerRequest, 1)
	client.SetServerRequestHandler(func(request ServerRequest) { requestReceived <- request })

	client.handleLine([]byte(`{"method":"turn/started","params":{"turn":{"id":"turn-1"}}}`))
	client.handleLine([]byte(`{"method":"item/commandExecution/requestApproval","id":9,"params":{"command":"git status"}}`))

	select {
	case notification := <-notifications:
		if notification.Method != "turn/started" || !strings.Contains(string(notification.Params), "turn-1") {
			t.Fatalf("unexpected notification: %#v", notification)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for notification")
	}
	select {
	case request := <-requestReceived:
		if request.Method != "item/commandExecution/requestApproval" || string(request.ID) != "9" {
			t.Fatalf("unexpected server request: %#v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for server request")
	}
}

func TestRedactsDiagnostics(t *testing.T) {
	message := redactDiagnostic("Authorization: Bearer abc.def.ghi sk-12345678 access_token=secret")
	if strings.Contains(message, "abc.def.ghi") || strings.Contains(message, "sk-12345678") || strings.Contains(message, "secret") {
		t.Fatalf("diagnostic was not redacted: %s", message)
	}
}
