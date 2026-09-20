package main

import (
	"errors"
	"strings"
	"time"

	"github.com/RuanFernandes/centurion/internal/model"
	"github.com/google/uuid"
)

const (
	builderStateIdle      = "idle"
	builderStateWorking   = "working"
	builderStateCompleted = "completed"
	builderStateError     = "error"
)

// beginBuilderJob creates the single in-process Builder slot. The slot is
// deliberately shared by planning and building so a navigation/remount cannot
// start a second Codex turn while the first one is still active.
func (s *AppService) beginBuilderJob(scope, projectID string) (string, error) {
	scope = strings.TrimSpace(scope)
	if scope != "planning" && scope != "building" {
		return "", errors.New("invalid builder request scope")
	}

	nowValue := now()
	status := model.BuilderStatus{
		SchemaVersion: 1,
		Timestamp:     nowValue,
		Sequence:      time.Now().UnixNano(),
		Source:        "centurion",
		RequestID:     uuid.NewString(),
		ProjectID:     strings.TrimSpace(projectID),
		Scope:         scope,
		State:         builderStateWorking,
		Activity:      "Starting the Codex turn",
		Detail:        "Preparing a focused response.",
		StartedAt:     nowValue,
		UpdatedAt:     nowValue,
	}

	s.builderMu.Lock()
	if s.builderBusy {
		s.builderMu.Unlock()
		return "", errors.New("another Codex planning or builder request is already running")
	}
	s.builderBusy = true
	s.builderStatus = status
	s.builderMu.Unlock()

	s.emit("builder.status", status)
	return status.RequestID, nil
}

// GetBuilderStatus returns the current Builder job, including its last safe
// activity and completed response. It is intentionally in-memory: the Codex
// turn itself belongs to the current App Server process, while the UI can
// reconnect to this snapshot whenever its route is mounted again.
func (s *AppService) GetBuilderStatus() model.BuilderStatus {
	s.builderMu.Lock()
	status := s.builderStatus
	if status.State == "" {
		nowValue := now()
		status = model.BuilderStatus{
			SchemaVersion: 1,
			Timestamp:     nowValue,
			Sequence:      time.Now().UnixNano(),
			Source:        "centurion",
			Scope:         "planning",
			State:         builderStateIdle,
			Activity:      "No Builder request is running",
			Detail:        "Start a planning conversation or build request when ready.",
			StartedAt:     nowValue,
			UpdatedAt:     nowValue,
		}
	}
	s.builderMu.Unlock()
	return status
}

// ClearBuilderStatus dismisses a completed or failed result after the user
// has reviewed, discarded, or applied it. An active Codex turn cannot be
// dismissed, which prevents the UI from hiding a request that is still live.
func (s *AppService) ClearBuilderStatus() error {
	s.builderMu.Lock()
	if s.builderBusy || s.builderStatus.State == builderStateWorking {
		s.builderMu.Unlock()
		return errors.New("cannot clear the Builder status while a request is running")
	}
	s.builderStatus = model.BuilderStatus{}
	s.builderMu.Unlock()
	s.emit("builder.status", s.GetBuilderStatus())
	return nil
}

func (s *AppService) updateBuilderJobProject(requestID, projectID string) {
	if strings.TrimSpace(requestID) == "" {
		return
	}
	s.builderMu.Lock()
	if s.builderStatus.RequestID != requestID {
		s.builderMu.Unlock()
		return
	}
	s.builderStatus.ProjectID = strings.TrimSpace(projectID)
	s.builderStatus.Timestamp = now()
	s.builderStatus.UpdatedAt = s.builderStatus.Timestamp
	s.builderStatus.Sequence = time.Now().UnixNano()
	status := s.builderStatus
	s.builderMu.Unlock()
	s.emit("builder.status", status)
}

func (s *AppService) updateBuilderJobActivity(requestID, scope, state, activity, detail, threadID, turnID string) {
	if strings.TrimSpace(requestID) == "" {
		return
	}
	s.builderMu.Lock()
	if s.builderStatus.RequestID != requestID {
		s.builderMu.Unlock()
		return
	}
	if strings.TrimSpace(scope) != "" {
		s.builderStatus.Scope = strings.TrimSpace(scope)
	}
	if strings.TrimSpace(state) != "" {
		s.builderStatus.State = strings.TrimSpace(state)
	}
	if strings.TrimSpace(activity) != "" {
		s.builderStatus.Activity = strings.TrimSpace(activity)
	}
	if strings.TrimSpace(detail) != "" {
		s.builderStatus.Detail = strings.TrimSpace(detail)
	}
	if strings.TrimSpace(threadID) != "" {
		s.builderStatus.ThreadID = strings.TrimSpace(threadID)
	}
	if strings.TrimSpace(turnID) != "" {
		s.builderStatus.TurnID = strings.TrimSpace(turnID)
	}
	s.builderStatus.Timestamp = now()
	s.builderStatus.UpdatedAt = s.builderStatus.Timestamp
	s.builderStatus.Sequence = time.Now().UnixNano()
	status := s.builderStatus
	s.builderMu.Unlock()
	s.emit("builder.status", status)
}

func (s *AppService) setBuilderJobResult(requestID string, planning *model.PlanningResponse, building *model.BuilderResponse) {
	if strings.TrimSpace(requestID) == "" {
		return
	}
	s.builderMu.Lock()
	if s.builderStatus.RequestID != requestID {
		s.builderMu.Unlock()
		return
	}
	if planning != nil {
		result := *planning
		s.builderStatus.PlanningResponse = &result
		s.builderStatus.ThreadID = result.ThreadID
	}
	if building != nil {
		result := *building
		s.builderStatus.BuilderResponse = &result
		s.builderStatus.ThreadID = result.ThreadID
	}
	s.builderStatus.Timestamp = now()
	s.builderStatus.UpdatedAt = s.builderStatus.Timestamp
	s.builderStatus.Sequence = time.Now().UnixNano()
	status := s.builderStatus
	s.builderMu.Unlock()
	s.emit("builder.status", status)
}

func (s *AppService) finishBuilderJob(requestID string, requestErr error) {
	if strings.TrimSpace(requestID) == "" {
		return
	}
	s.builderMu.Lock()
	if s.builderStatus.RequestID != requestID {
		s.builderMu.Unlock()
		return
	}
	if s.builderStatus.State == builderStateWorking {
		if requestErr != nil {
			s.builderStatus.State = builderStateError
			s.builderStatus.Activity = "Request stopped"
			s.builderStatus.Detail = "Codex could not complete this request."
			s.builderStatus.Error = requestErr.Error()
		} else {
			s.builderStatus.State = builderStateCompleted
			s.builderStatus.Activity = "Response ready"
			s.builderStatus.Detail = "The result is ready to review."
		}
	}
	if requestErr != nil && strings.TrimSpace(s.builderStatus.Error) == "" {
		s.builderStatus.Error = requestErr.Error()
	}
	s.builderStatus.Timestamp = now()
	s.builderStatus.UpdatedAt = s.builderStatus.Timestamp
	s.builderStatus.Sequence = time.Now().UnixNano()
	s.builderBusy = false
	status := s.builderStatus
	s.builderMu.Unlock()
	s.emit("builder.status", status)
}
