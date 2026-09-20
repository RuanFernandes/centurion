package main

import (
	"testing"

	"github.com/RuanFernandes/centurion/internal/model"
)

func TestBuilderJobRetainsCompletedResponseAfterTheCallerReturns(t *testing.T) {
	service := &AppService{emitter: func(string, any) {}}
	requestID, err := service.beginBuilderJob("building", "project-1")
	if err != nil {
		t.Fatalf("beginBuilderJob() error = %v", err)
	}

	response := model.BuilderResponse{
		ThreadID: "thread-1",
		Reply:    "Proposal ready",
		Proposal: model.BuilderProposal{Summary: "A recoverable proposal", Agents: []model.BuilderAgentDraft{}},
	}
	service.updateBuilderJobActivity(requestID, "building", "working", "Drafting the response", "The model is working.", "thread-1", "turn-1")
	service.setBuilderJobResult(requestID, nil, &response)
	service.finishBuilderJob(requestID, nil)

	status := service.GetBuilderStatus()
	if status.State != builderStateCompleted {
		t.Fatalf("status.State = %q, want %q", status.State, builderStateCompleted)
	}
	if status.RequestID != requestID {
		t.Fatalf("status.RequestID = %q, want %q", status.RequestID, requestID)
	}
	if status.BuilderResponse == nil || status.BuilderResponse.Proposal.Summary != response.Proposal.Summary {
		t.Fatalf("completed Builder response was not retained: %#v", status.BuilderResponse)
	}
	if status.ThreadID != "thread-1" || status.TurnID != "turn-1" {
		t.Fatalf("Codex identifiers were not retained: thread=%q turn=%q", status.ThreadID, status.TurnID)
	}

	if _, err := service.beginBuilderJob("planning", "project-1"); err != nil {
		t.Fatalf("completed job did not release the Builder slot: %v", err)
	}
}

func TestBuilderJobRejectsConcurrentRequest(t *testing.T) {
	service := &AppService{emitter: func(string, any) {}}
	requestID, err := service.beginBuilderJob("planning", "project-1")
	if err != nil {
		t.Fatalf("beginBuilderJob() error = %v", err)
	}
	defer service.finishBuilderJob(requestID, nil)

	if _, err := service.beginBuilderJob("building", "project-1"); err == nil {
		t.Fatal("second Builder request was accepted while the first was active")
	}
	if status := service.GetBuilderStatus(); status.State != builderStateWorking {
		t.Fatalf("active status.State = %q, want %q", status.State, builderStateWorking)
	}
}
