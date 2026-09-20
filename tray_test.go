package main

import (
	"strings"
	"testing"
	"time"

	"github.com/RuanFernandes/centurion/internal/model"
)

func TestTrayTooltipShowsActiveWorkflowStatus(t *testing.T) {
	tooltip := trayTooltip([]trayRunState{{
		workflowName: "Release pipeline",
		status:       model.RunStatusWaitingApproval,
		lastSeen:     time.Now(),
	}}, model.AuthStatusLoggedIn)

	if want := "Centurion | Release pipeline | Approval needed"; tooltip != want {
		t.Fatalf("tooltip = %q, want %q", tooltip, want)
	}
}

func TestTrayTooltipShowsLatestCompletedWorkflow(t *testing.T) {
	now := time.Now()
	tooltip := trayTooltip([]trayRunState{
		{workflowName: "Old workflow", status: model.RunStatusFailed, lastSeen: now.Add(-time.Minute)},
		{workflowName: "Deploy", status: model.RunStatusCompleted, lastSeen: now},
	}, model.AuthStatusLoggedIn)

	if want := "Centurion | Deploy | Completed"; tooltip != want {
		t.Fatalf("tooltip = %q, want %q", tooltip, want)
	}
}

func TestTrayTooltipLimitsNativeTooltipLength(t *testing.T) {
	longName := strings.Repeat("workflow-", 40)
	tooltip := trayTooltip([]trayRunState{{
		workflowName: longName,
		status:       model.RunStatusRunning,
		lastSeen:     time.Now(),
	}}, model.AuthStatusLoggedIn)

	if got := len([]rune(tooltip)); got > trayTooltipLimit {
		t.Fatalf("tooltip has %d runes, want at most %d", got, trayTooltipLimit)
	}
	if !strings.HasSuffix(tooltip, "...") {
		t.Fatalf("tooltip = %q, want truncation suffix", tooltip)
	}
}

func TestTrayStatusFromRunEvent(t *testing.T) {
	tests := []struct {
		name   string
		event  model.RunEvent
		status string
	}{
		{name: "status data", event: model.RunEvent{Type: "run.status", Data: map[string]any{"status": model.RunStatusPaused}}, status: model.RunStatusPaused},
		{name: "completed", event: model.RunEvent{Type: "run.completed"}, status: model.RunStatusCompleted},
		{name: "blocked", event: model.RunEvent{Type: "run.blocked"}, status: model.RunStatusBlocked},
		{name: "unrelated step", event: model.RunEvent{Type: "step.completed"}, status: ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := trayStatusFromEvent(test.event); got != test.status {
				t.Fatalf("status = %q, want %q", got, test.status)
			}
		})
	}
}
