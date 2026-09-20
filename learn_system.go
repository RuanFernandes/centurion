package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/RuanFernandes/centurion/internal/model"
	"github.com/RuanFernandes/centurion/internal/security"
	"github.com/google/uuid"
)

const (
	learnSystemAgentID = "centurion.system-learner"
	learnSystemTimeout = 20 * time.Minute
)

var learnSystemDocumentNames = []string{
	"centurion-system-overview.md",
	"centurion-architecture.md",
	"centurion-project-map.md",
	"centurion-operations.md",
	"centurion-risks.md",
}

type learnSystemOutput struct {
	Status   string   `json:"status"`
	Files    []string `json:"files"`
	Summary  string   `json:"summary"`
	Blockers []string `json:"blockers"`
}

// LearnProjectSystem performs one tool-first documentation pass. The initial
// request contains only a compact instruction and configured paths; source
// contents stay behind Codex tools on the local workspace boundary.
func (s *AppService) LearnProjectSystem(request model.LearnSystemRequest) (model.LearnSystemResult, error) {
	request.ProjectID = strings.TrimSpace(request.ProjectID)
	request.ModelID = strings.TrimSpace(request.ModelID)
	request.ReasoningEffort = strings.TrimSpace(request.ReasoningEffort)
	if len(request.ProjectID) > 200 || len(request.ModelID) > 200 || len(request.ReasoningEffort) > 80 {
		return model.LearnSystemResult{}, errors.New("system learning request is invalid")
	}

	s.learningMu.Lock()
	if s.learningBusy {
		s.learningMu.Unlock()
		return model.LearnSystemResult{}, errors.New("system learning is already running")
	}
	s.learningBusy = true
	s.learningMu.Unlock()
	defer func() {
		s.learningMu.Lock()
		s.learningBusy = false
		s.learningMu.Unlock()
	}()

	if err := s.ensureConnected(); err != nil {
		return model.LearnSystemResult{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), learnSystemTimeout)
	defer cancel()

	project, err := projectForID(ctx, s.store, request.ProjectID)
	if err != nil {
		return model.LearnSystemResult{}, fmt.Errorf("load learning project: %w", err)
	}
	roots, err := security.NormalizeRoots(project.Folders)
	if err != nil {
		return model.LearnSystemResult{}, fmt.Errorf("validate project folders: %w", err)
	}
	primaryFolder := roots[0]
	docsPath, err := learningDocsPath(primaryFolder)
	if err != nil {
		return model.LearnSystemResult{}, err
	}
	if err := os.MkdirAll(docsPath, 0o700); err != nil {
		return model.LearnSystemResult{}, fmt.Errorf("create project docs folder: %w", err)
	}
	if err := validateLearningTargets(docsPath); err != nil {
		return model.LearnSystemResult{}, err
	}

	modelID, effort, err := selectLearningModel(request, s.codex.Models())
	if err != nil {
		return model.LearnSystemResult{}, err
	}
	templates, err := s.store.SystemPromptTemplates(ctx)
	if err != nil {
		return model.LearnSystemResult{}, fmt.Errorf("load system learning prompt: %w", err)
	}
	prompt := buildLearningPrompt(templates["orchestrator.system_learning"], docsPath, roots)
	agent := model.AgentProfile{
		ID:                 learnSystemAgentID,
		Name:               "Centurion System Learner",
		Role:               "Project documentation analyst",
		Instructions:       "Inspect the configured project roots and write only the requested documentation files.",
		ModelID:            modelID,
		ReasoningEffort:    effort,
		WorkspaceRoots:     append([]string(nil), roots...),
		ToolAllowlist:      []string{"files.read", "files.write", "shell.search"},
		ApprovalProfile:    "on_request",
		VisualState:        model.AgentStateWorking,
		MaxDurationSeconds: int(learnSystemTimeout / time.Second),
		MaxTurns:           1,
		MaxAttempts:        1,
	}

	startedAt := time.Now()
	turn, err := s.codex.RunAgentTurnWithOutputSchema(ctx, agent, prompt, "", learningOutputSchema(), nil, nil)
	if err != nil && turn.ThreadID != "" && outputSchemaCompatibilityError(err) {
		turn, err = s.codex.RunAgentTurn(ctx, agent, prompt, turn.ThreadID, nil, nil)
	}
	if err != nil {
		return model.LearnSystemResult{ProjectID: project.ID, PrimaryFolder: primaryFolder, DocsPath: docsPath, ThreadID: turn.ThreadID, ModelID: modelID, ReasoningEffort: effort, DurationMS: time.Since(startedAt).Milliseconds()}, err
	}

	output := parseLearnSystemOutput(turn.Output)
	docsFiles, missingFiles := inspectLearningDocs(docsPath)
	status := "completed"
	if len(missingFiles) > 0 {
		status = "partial"
	}
	if len(docsFiles) == 0 {
		status = "blocked"
	}
	summary := strings.TrimSpace(output.Summary)
	if summary == "" {
		summary = "Documentation pass completed."
	}
	if status == "blocked" {
		return model.LearnSystemResult{
			ProjectID:       project.ID,
			Status:          status,
			PrimaryFolder:   primaryFolder,
			DocsPath:        docsPath,
			DocsFiles:       docsFiles,
			MissingFiles:    missingFiles,
			ModelID:         modelID,
			ReasoningEffort: effort,
			ThreadID:        turn.ThreadID,
			Summary:         summary,
			DurationMS:      time.Since(startedAt).Milliseconds(),
		}, errors.New("Codex finished without writing system documentation")
	}

	result := model.LearnSystemResult{
		ProjectID:       project.ID,
		Status:          status,
		PrimaryFolder:   primaryFolder,
		DocsPath:        docsPath,
		DocsFiles:       docsFiles,
		MissingFiles:    missingFiles,
		ModelID:         modelID,
		ReasoningEffort: effort,
		ThreadID:        turn.ThreadID,
		Summary:         summary,
		DurationMS:      time.Since(startedAt).Milliseconds(),
	}
	_ = s.store.AppendHistory(ctx, model.HistoryEntry{
		ID:        uuid.NewString(),
		ProjectID: project.ID,
		Kind:      "system_learning",
		Title:     "System documentation pass",
		Content:   summary,
		Metadata: map[string]any{
			"status":          result.Status,
			"docsPath":        result.DocsPath,
			"docsFiles":       result.DocsFiles,
			"missingFiles":    result.MissingFiles,
			"modelID":         result.ModelID,
			"reasoningEffort": result.ReasoningEffort,
			"threadID":        result.ThreadID,
			"durationMs":      result.DurationMS,
		},
		CreatedAt: now(),
	})
	_ = s.store.AppendAudit(ctx, model.AuditEntry{
		ID:        uuid.NewString(),
		ProjectID: project.ID,
		Kind:      "project.system_learning",
		Actor:     "user",
		Target:    docsPath,
		Decision:  result.Status,
		Detail:    "Codex documented the configured project folders",
		Metadata: map[string]any{
			"modelID":         result.ModelID,
			"reasoningEffort": result.ReasoningEffort,
			"docsFiles":       result.DocsFiles,
			"missingFiles":    result.MissingFiles,
		},
		CreatedAt: now(),
	})
	return result, nil
}

func learningDocsPath(primaryFolder string) (string, error) {
	primary, err := security.NormalizeRoots([]string{primaryFolder})
	if err != nil {
		return "", fmt.Errorf("validate primary project folder: %w", err)
	}
	docsPath, err := security.NormalizePath(filepath.Join(primary[0], "docs"))
	if err != nil {
		return "", fmt.Errorf("resolve project docs folder: %w", err)
	}
	if !security.IsWithinRoots(docsPath, primary) {
		return "", errors.New("project docs folder is outside the primary project folder")
	}
	return docsPath, nil
}

func inspectLearningDocs(docsPath string) ([]string, []string) {
	written := make([]string, 0, len(learnSystemDocumentNames))
	missing := make([]string, 0, len(learnSystemDocumentNames))
	for _, name := range learnSystemDocumentNames {
		path := filepath.Join(docsPath, name)
		info, err := os.Lstat(path)
		if err == nil && info.Mode().IsRegular() {
			written = append(written, path)
			continue
		}
		missing = append(missing, filepath.Join("docs", name))
	}
	return written, missing
}

func validateLearningTargets(docsPath string) error {
	for _, name := range learnSystemDocumentNames {
		path := filepath.Join(docsPath, name)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect documentation target %q: %w", name, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("documentation target %q is a symbolic link; remove it before learning the system", name)
		}
		if info.IsDir() {
			return fmt.Errorf("documentation target %q is a directory; remove it before learning the system", name)
		}
	}
	return nil
}

func selectLearningModel(request model.LearnSystemRequest, models []model.ModelInfo) (string, string, error) {
	if len(models) == 0 {
		if request.ModelID != "" || request.ReasoningEffort != "" {
			return "", "", errors.New("the Codex model catalog is unavailable")
		}
		return "", "", nil
	}
	selected := models[0]
	if request.ModelID == "" {
		for _, entry := range models {
			if entry.IsDefault {
				selected = entry
				break
			}
		}
	} else {
		found := false
		for _, entry := range models {
			if strings.EqualFold(entry.ID, request.ModelID) || strings.EqualFold(entry.DisplayName, request.ModelID) {
				selected = entry
				found = true
				break
			}
		}
		if !found {
			return "", "", fmt.Errorf("model %q is not available for this Codex account", request.ModelID)
		}
	}

	if request.ReasoningEffort != "" {
		for _, supported := range selected.SupportedReasoningEfforts {
			if strings.EqualFold(supported.ReasoningEffort, request.ReasoningEffort) {
				return selected.ID, supported.ReasoningEffort, nil
			}
		}
		return "", "", fmt.Errorf("thinking effort %q is not supported by model %q", request.ReasoningEffort, selected.DisplayName)
	}
	for _, preferred := range []string{"low", "minimal"} {
		for _, supported := range selected.SupportedReasoningEfforts {
			if strings.EqualFold(supported.ReasoningEffort, preferred) {
				return selected.ID, supported.ReasoningEffort, nil
			}
		}
	}
	return selected.ID, "", nil
}

func buildLearningPrompt(template, docsPath string, roots []string) string {
	if strings.TrimSpace(template) == "" {
		template = model.DefaultSystemPromptTemplates()["orchestrator.system_learning"]
	}
	template = strings.ReplaceAll(template, "{{docs_folder}}", docsPath)
	encodedRoots, err := json.Marshal(roots)
	if err != nil {
		encodedRoots = []byte("[]")
	}
	return strings.TrimSpace(template) + `

Scope data: primary_root=` + filepath.Dir(docsPath) + `; docs_folder=` + docsPath + `; configured_roots=` + string(encodedRoots) + `
Immutable contract: inspect every configured root recursively, but skip dependencies, generated output, binaries, caches, secrets, and the docs folder itself. Modify no path except the five named Markdown files inside the primary docs folder. Do not delete files, expose secrets, or echo source contents. Final response must be compact JSON.`
}

func learningOutputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"status", "files", "summary", "blockers"},
		"properties": map[string]any{
			"status":   map[string]any{"type": "string"},
			"files":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": len(learnSystemDocumentNames)},
			"summary":  map[string]any{"type": "string"},
			"blockers": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 12},
		},
	}
}

func parseLearnSystemOutput(raw string) learnSystemOutput {
	cleaned := strings.TrimSpace(raw)
	if start := strings.IndexByte(cleaned, '{'); start >= 0 {
		if end := strings.LastIndexByte(cleaned, '}'); end >= start {
			cleaned = cleaned[start : end+1]
		}
	}
	var output learnSystemOutput
	if json.Unmarshal([]byte(cleaned), &output) != nil {
		return learnSystemOutput{}
	}
	output.Summary = truncateLearningText(output.Summary, 1200)
	return output
}

func truncateLearningText(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "…"
}
