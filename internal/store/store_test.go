package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/RuanFernandes/centurion/internal/model"
)

func TestStorePersistsAgentsRunsAndOrderedEvents(t *testing.T) {
	dataStore, err := Open(filepath.Join(t.TempDir(), "centurion.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()
	ctx := context.Background()
	agent := model.AgentProfile{ID: "test-agent", Name: "Test", Role: "Verifier", Instructions: "Verify", WorkspaceRoots: []string{"C:\\workspace"}, ToolAllowlist: []string{"files.read"}, ApprovalProfile: "on_request", RoomID: "library", AvatarID: "operator", VisualState: model.AgentStateIdle, MaxDurationSeconds: 60, MaxTurns: 2, MaxAttempts: 1}
	if err := dataStore.SaveAgent(ctx, agent); err != nil {
		t.Fatal(err)
	}
	loadedAgent, err := dataStore.GetAgent(ctx, agent.ID)
	if err != nil || loadedAgent.Name != agent.Name || len(loadedAgent.WorkspaceRoots) != 1 {
		t.Fatalf("agent round-trip failed: %#v, %v", loadedAgent, err)
	}

	run := model.Run{ID: "run-test", WorkflowID: "workflow-studio-brief", Status: model.RunStatusRunning, Input: map[string]any{"goal": "test"}, Output: map[string]any{}, StartedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z"}
	if err := dataStore.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	first, err := dataStore.AppendRunEvent(ctx, model.RunEvent{RunID: run.ID, Type: "one", Source: "test", Level: "info", Message: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := dataStore.AppendRunEvent(ctx, model.RunEvent{RunID: run.ID, Type: "two", Source: "test", Level: "info", Message: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Sequence != 1 || second.Sequence != 2 {
		t.Fatalf("unexpected event sequences: %d, %d", first.Sequence, second.Sequence)
	}
	events, err := dataStore.ListRunEvents(ctx, run.ID, 0)
	if err != nil || len(events) != 2 || events[1].Message != "second" {
		t.Fatalf("event round-trip failed: %#v, %v", events, err)
	}
}

func TestStoreDeletesWorkflow(t *testing.T) {
	dataStore, err := Open(filepath.Join(t.TempDir(), "centurion.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()
	ctx := context.Background()
	workflow, err := dataStore.GetWorkflow(ctx, "workflow-studio-brief")
	if err != nil {
		t.Fatal(err)
	}
	workflow.ID = "workflow-delete-test"
	if err := dataStore.SaveWorkflow(ctx, workflow); err != nil {
		t.Fatal(err)
	}
	if err := dataStore.DeleteWorkflow(ctx, workflow.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := dataStore.GetWorkflow(ctx, workflow.ID); err == nil {
		t.Fatal("deleted workflow is still available")
	}
}

func TestStoreTranslatesLegacyPortugueseDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "centurion.db")
	dataStore, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := dataStore.db.ExecContext(ctx, `UPDATE agents SET role = ?, instructions = ? WHERE id = ?`, "Arquiteta de sistemas", "Defina uma abordagem pequena, segura e verificável. Entregue decisões e riscos em formato objetivo.", "agent-architect"); err != nil {
		dataStore.Close()
		t.Fatal(err)
	}
	workflow, err := dataStore.GetWorkflow(ctx, "workflow-studio-brief")
	if err != nil {
		dataStore.Close()
		t.Fatal(err)
	}
	workflow.Name = "Brief de produto"
	workflow.Description = "Exemplo de supervisor com execução paralela, condição, loop limitado e aprovação."
	legacyLabels := map[string]string{"brief": "Definir direção", "parallel": "Abrir frentes", "build": "Construir", "review": "Revisar", "join": "Consolidar", "quality": "Qualidade aprovada", "loop": "Iterar correções", "approval": "Aprovar publicação", "artifact": "Registrar resultado"}
	for index := range workflow.Nodes {
		workflow.Nodes[index].Label = legacyLabels[workflow.Nodes[index].ID]
	}
	definition, err := json.Marshal(workflow)
	if err != nil {
		dataStore.Close()
		t.Fatal(err)
	}
	if _, err := dataStore.db.ExecContext(ctx, `UPDATE workflows SET name = ?, definition_json = ? WHERE id = ?`, workflow.Name, string(definition), workflow.ID); err != nil {
		dataStore.Close()
		t.Fatal(err)
	}
	if err := dataStore.Close(); err != nil {
		t.Fatal(err)
	}

	dataStore, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()
	agent, err := dataStore.GetAgent(ctx, "agent-architect")
	if err != nil {
		t.Fatal(err)
	}
	if agent.Role != "Systems architect" || agent.Instructions == "" || agent.Instructions == "Defina uma abordagem pequena, segura e verificável. Entregue decisões e riscos em formato objetivo." {
		t.Fatalf("legacy agent was not translated: %#v", agent)
	}
	workflow, err = dataStore.GetWorkflow(ctx, "workflow-studio-brief")
	if err != nil {
		t.Fatal(err)
	}
	if workflow.Name != "Product brief" || workflow.Description == "Exemplo de supervisor com execução paralela, condição, loop limitado e aprovação." || workflow.Nodes[0].Label != "Define direction" {
		t.Fatalf("legacy workflow was not translated: %#v", workflow)
	}
}

func TestStorePersistsAndResetsSystemPrompt(t *testing.T) {
	dataStore, err := Open(filepath.Join(t.TempDir(), "centurion.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()
	ctx := context.Background()

	prompts, err := dataStore.ListSystemPrompts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var memoryPrompt model.SystemPrompt
	for _, prompt := range prompts {
		if prompt.ID == "agent.persistent_memory" {
			memoryPrompt = prompt
			break
		}
	}
	if memoryPrompt.ID == "" || memoryPrompt.IsCustomized || memoryPrompt.Template != memoryPrompt.DefaultTemplate {
		t.Fatalf("expected default persistent memory prompt: %#v", memoryPrompt)
	}

	custom := "Private memory for this agent:\n{{memory_summary}}\nKeep it concise."
	saved, err := dataStore.SaveSystemPrompt(ctx, memoryPrompt.ID, custom)
	if err != nil {
		t.Fatal(err)
	}
	if !saved.IsCustomized || saved.Template != custom {
		t.Fatalf("custom prompt was not persisted: %#v", saved)
	}
	templates, err := dataStore.SystemPromptTemplates(ctx)
	if err != nil || templates[memoryPrompt.ID] != custom {
		t.Fatalf("custom prompt was not loaded by the executor: %#v, %v", templates, err)
	}

	reset, err := dataStore.ResetSystemPrompt(ctx, memoryPrompt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reset.IsCustomized || reset.Template != reset.DefaultTemplate {
		t.Fatalf("prompt did not reset to default: %#v", reset)
	}
	if _, err := dataStore.SaveSystemPrompt(ctx, memoryPrompt.ID, "{{agent_name}}"); err == nil {
		t.Fatal("expected unsupported placeholder validation to fail")
	}
}

func TestStorePersistsMultiFolderProjectsAndHistory(t *testing.T) {
	dataStore, err := Open(filepath.Join(t.TempDir(), "centurion.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()
	ctx := context.Background()
	firstFolder := filepath.Join(t.TempDir(), "service-a")
	secondFolder := filepath.Join(t.TempDir(), "service-b")
	project := model.Project{ID: "project-multi", Name: "Multi service", Folders: []string{firstFolder, secondFolder}, Description: "Two repositories"}
	if err := dataStore.SaveProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := dataStore.SetActiveProject(ctx, project.ID); err != nil {
		t.Fatal(err)
	}
	loaded, err := dataStore.GetActiveProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != project.ID || len(loaded.Folders) != 2 || loaded.Folders[0] != firstFolder || loaded.Folders[1] != secondFolder {
		t.Fatalf("project round-trip failed: %#v", loaded)
	}
	if err := dataStore.AppendHistory(ctx, model.HistoryEntry{ID: "history-test", ProjectID: project.ID, Kind: "prompt", Title: "Prompt", Content: "keep this", Metadata: map[string]any{"source": "test"}}); err != nil {
		t.Fatal(err)
	}
	entries, err := dataStore.ListHistory(ctx, model.HistoryFilter{ProjectID: project.ID, Kind: "prompt"})
	if err != nil || len(entries) != 1 || entries[0].Content != "keep this" || entries[0].Metadata["source"] != "test" {
		t.Fatalf("history round-trip failed: %#v, %v", entries, err)
	}
}

func TestStoreScopesProjectCatalogs(t *testing.T) {
	dataStore, err := Open(filepath.Join(t.TempDir(), "centurion.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()
	ctx := context.Background()
	for _, project := range []model.Project{
		{ID: "project-a", Name: "Project A", Folders: []string{t.TempDir()}},
		{ID: "project-b", Name: "Project B", Folders: []string{t.TempDir()}},
	} {
		if err := dataStore.SaveProject(ctx, project); err != nil {
			t.Fatal(err)
		}
	}
	for _, agent := range []model.AgentProfile{
		{ID: "agent-a", ProjectID: "project-a", Name: "Agent A", Role: "Builder"},
		{ID: "agent-b", ProjectID: "project-b", Name: "Agent B", Role: "Reviewer"},
	} {
		if err := dataStore.SaveAgent(ctx, agent); err != nil {
			t.Fatal(err)
		}
	}
	baseWorkflow, err := dataStore.GetWorkflow(ctx, "workflow-studio-brief")
	if err != nil {
		t.Fatal(err)
	}
	for _, projectID := range []string{"project-a", "project-b"} {
		workflow := baseWorkflow
		workflow.ID = "workflow-" + projectID
		workflow.ProjectID = projectID
		if err := dataStore.SaveWorkflow(ctx, workflow); err != nil {
			t.Fatal(err)
		}
	}
	agents, err := dataStore.ListAgentsForProject(ctx, "project-a")
	if err != nil || len(agents) != 1 || agents[0].ID != "agent-a" {
		t.Fatalf("project A agent catalog leaked: %#v, %v", agents, err)
	}
	workflows, err := dataStore.ListWorkflowsForProject(ctx, "project-b")
	if err != nil || len(workflows) != 1 || workflows[0].ID != "workflow-project-b" {
		t.Fatalf("project B workflow catalog leaked: %#v, %v", workflows, err)
	}
}

func TestStorePersistsRedactedAuditEntries(t *testing.T) {
	dataStore, err := Open(filepath.Join(t.TempDir(), "centurion.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()
	ctx := context.Background()
	if err := dataStore.AppendAudit(ctx, model.AuditEntry{
		ProjectID: "project-local",
		RunID:     "run-audit",
		Kind:      "approval",
		Actor:     "user",
		Target:    "command",
		Decision:  "approve",
		Detail:    "Authorization: Bearer secret-value",
		Metadata:  map[string]any{"token": "access_token=do-not-store"},
	}); err != nil {
		t.Fatal(err)
	}
	entries, err := dataStore.ListAudit(ctx, model.AuditFilter{RunID: "run-audit"})
	if err != nil || len(entries) != 1 {
		t.Fatalf("audit entry was not persisted: %#v, %v", entries, err)
	}
	if entries[0].Detail == "" || entries[0].Detail == "Authorization: Bearer secret-value" || entries[0].Metadata["token"] == "access_token=do-not-store" {
		t.Fatalf("audit entry was not redacted: %#v", entries[0])
	}
}
