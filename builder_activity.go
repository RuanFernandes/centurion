package main

import (
	"strings"
	"sync"
	"time"

	"github.com/RuanFernandes/centurion/internal/codex"
	"github.com/RuanFernandes/centurion/internal/model"
)

type builderActivityPhase struct {
	Activity string
	Detail   string
}

// builderActivityForCodexMethod turns protocol-level notifications into a
// small, user-facing activity vocabulary. The notification payload is never
// inspected or forwarded, which keeps private model output out of the UI.
func builderActivityForCodexMethod(method string) builderActivityPhase {
	method = strings.ToLower(strings.TrimSpace(method))
	switch {
	case method == "thread/started" || strings.Contains(method, "thread/start"):
		return builderActivityPhase{Activity: "Opening the conversation", Detail: "Preparing the planning context."}
	case strings.Contains(method, "reasoning"):
		return builderActivityPhase{Activity: "Reviewing the request", Detail: "Checking assumptions and constraints."}
	case strings.Contains(method, "command") || strings.Contains(method, "tool") || strings.Contains(method, "mcp"):
		return builderActivityPhase{Activity: "Checking available actions", Detail: "Evaluating the next safe step."}
	case strings.Contains(method, "file") || strings.Contains(method, "patch"):
		return builderActivityPhase{Activity: "Reviewing project context", Detail: "Keeping the response aligned with the workspace."}
	case strings.Contains(method, "agentmessage") || strings.Contains(method, "message"):
		return builderActivityPhase{Activity: "Drafting the response", Detail: "Turning the analysis into a concise result."}
	case strings.Contains(method, "turn/completed"):
		return builderActivityPhase{Activity: "Finishing the response", Detail: "Checking the result before displaying it."}
	case strings.Contains(method, "turn"):
		return builderActivityPhase{Activity: "Processing the request", Detail: "The Codex turn is active."}
	default:
		return builderActivityPhase{Activity: "Working through the request", Detail: "The Codex turn is active."}
	}
}

type builderActivityReporter struct {
	service *AppService
	scope   string
	jobID   string

	mu       sync.Mutex
	threadID string
	turnID   string
	lastKey  string
}

func newBuilderActivityReporter(service *AppService, scope, jobID string) *builderActivityReporter {
	return &builderActivityReporter{service: service, scope: scope, jobID: jobID}
}

func (r *builderActivityReporter) start() {
	r.emit("working", "Starting the Codex turn", "Preparing a focused response.")
}

func (r *builderActivityReporter) turnStarted(threadID, turnID string) {
	r.mu.Lock()
	r.threadID = threadID
	r.turnID = turnID
	r.mu.Unlock()
	r.emit("working", "Receiving Codex updates", "The model is working on the active request.")
}

func (r *builderActivityReporter) notification(notification codex.Notification) {
	phase := builderActivityForCodexMethod(notification.Method)
	r.emit("working", phase.Activity, phase.Detail)
}

func (r *builderActivityReporter) completed() {
	r.emit("completed", "Response ready", "The result is ready to review.")
}

func (r *builderActivityReporter) failed() {
	r.emit("error", "Response stopped", "Codex could not complete this request.")
}

func (r *builderActivityReporter) emit(state, activity, detail string) {
	if r == nil || r.service == nil || strings.TrimSpace(r.scope) == "" {
		return
	}
	key := state + "\x00" + activity + "\x00" + detail
	r.mu.Lock()
	if state == "working" && key == r.lastKey {
		r.mu.Unlock()
		return
	}
	r.lastKey = key
	threadID, turnID := r.threadID, r.turnID
	r.mu.Unlock()

	r.service.updateBuilderJobActivity(r.jobID, r.scope, state, activity, detail, threadID, turnID)
	r.service.emit("builder.activity", model.BuilderActivityEvent{
		SchemaVersion: 1,
		Timestamp:     time.Now().UTC().Format(time.RFC3339Nano),
		Sequence:      time.Now().UnixNano(),
		Source:        "codex",
		Scope:         r.scope,
		ThreadID:      threadID,
		TurnID:        turnID,
		State:         state,
		Activity:      activity,
		Detail:        detail,
	})
}
