package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

type message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
}

var turnNumber int

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024), 1024*1024)
	for scanner.Scan() {
		var request message
		if json.Unmarshal(scanner.Bytes(), &request) != nil || request.Method == "" {
			continue
		}
		switch request.Method {
		case "initialize":
			respond(request.ID, map[string]any{"methods": []string{"initialize", "account/read", "model/list", "mcpServerStatus/list", "thread/start", "thread/resume", "turn/start", "turn/interrupt"}})
		case "initialized":
			continue
		case "account/read":
			respond(request.ID, map[string]any{"account": map[string]any{"type": "chatgpt", "email": "fake@example.test", "planType": "pro"}})
		case "account/rateLimits/read":
			respond(request.ID, map[string]any{"rateLimits": map[string]any{"limitId": "fake", "limitName": "Fake Codex", "primary": map[string]any{"usedPercent": 1, "windowDurationMins": 300, "resetsAt": 4102444800}}})
		case "model/list":
			respond(request.ID, map[string]any{"data": []any{map[string]any{"id": "fake-model", "displayName": "Fake model", "defaultReasoningEffort": "low", "supportedReasoningEfforts": []any{"low"}, "inputModalities": []string{"text"}, "isDefault": true}}})
		case "mcpServerStatus/list":
			respond(request.ID, map[string]any{"data": []any{}})
		case "thread/start":
			respond(request.ID, map[string]any{"thread": map[string]any{"id": "fake-thread-1"}})
		case "thread/resume", "turn/interrupt":
			respond(request.ID, map[string]any{})
		case "turn/start":
			turnNumber++
			turnID := fmt.Sprintf("fake-turn-%d", turnNumber)
			respond(request.ID, map[string]any{"turn": map[string]any{"id": turnID, "status": "inProgress"}})
			notify("item/agentMessage/delta", map[string]any{"turnId": turnID, "delta": "fake response"})
			notify("turn/completed", map[string]any{"turn": map[string]any{"id": turnID, "status": "completed"}})
		default:
			respond(request.ID, map[string]any{})
		}
	}
}

func respond(id json.RawMessage, result any) {
	write(map[string]any{"id": id, "result": result})
}

func notify(method string, params any) {
	write(map[string]any{"method": method, "params": params})
}

func write(value any) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return
	}
	_, _ = os.Stdout.Write(append(encoded, '\n'))
}
