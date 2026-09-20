package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RuanFernandes/centurion/internal/model"
)

func TestLearningDocsPathStaysInsidePrimaryFolder(t *testing.T) {
	primary := t.TempDir()
	docsPath, err := learningDocsPath(primary)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(primary, "docs")
	if docsPath != want {
		t.Fatalf("learning docs path = %q, want %q", docsPath, want)
	}

	if !strings.HasPrefix(docsPath, primary+string(filepath.Separator)) {
		t.Fatalf("learning docs path escaped primary folder: %q", docsPath)
	}
}

func TestSelectLearningModelPrefersEconomicalEffort(t *testing.T) {
	models := []model.ModelInfo{
		{
			ID:          "balanced",
			DisplayName: "Balanced",
			SupportedReasoningEfforts: []model.ReasoningEffort{
				{ReasoningEffort: "high"},
				{ReasoningEffort: "low"},
			},
		},
		{
			ID:        "default",
			IsDefault: true,
			SupportedReasoningEfforts: []model.ReasoningEffort{
				{ReasoningEffort: "minimal"},
				{ReasoningEffort: "medium"},
			},
		},
	}

	modelID, effort, err := selectLearningModel(model.LearnSystemRequest{}, models)
	if err != nil {
		t.Fatal(err)
	}
	if modelID != "default" || effort != "minimal" {
		t.Fatalf("default learning selection = (%q, %q), want (default, minimal)", modelID, effort)
	}

	modelID, effort, err = selectLearningModel(model.LearnSystemRequest{ModelID: "balanced", ReasoningEffort: "HIGH"}, models)
	if err != nil {
		t.Fatal(err)
	}
	if modelID != "balanced" || effort != "high" {
		t.Fatalf("explicit learning selection = (%q, %q), want (balanced, high)", modelID, effort)
	}
}

func TestSelectLearningModelRejectsUnavailableOptions(t *testing.T) {
	models := []model.ModelInfo{{ID: "default", DisplayName: "Default"}}
	if _, _, err := selectLearningModel(model.LearnSystemRequest{ModelID: "missing"}, models); err == nil {
		t.Fatal("unavailable model was accepted")
	}
	if _, _, err := selectLearningModel(model.LearnSystemRequest{ReasoningEffort: "high"}, models); err == nil {
		t.Fatal("unsupported reasoning effort was accepted")
	}
}

func TestBuildLearningPromptKeepsScopeCompactAndBounded(t *testing.T) {
	docsPath := filepath.Join(t.TempDir(), "docs")
	roots := []string{filepath.Dir(docsPath), filepath.Join(filepath.Dir(docsPath), "service")}
	prompt := buildLearningPrompt("Write to {{docs_folder}}.", docsPath, roots)
	for _, expected := range []string{
		"Write to " + docsPath + ".",
		"primary_root=" + filepath.Dir(docsPath),
		"docs_folder=" + docsPath,
		"configured_roots=",
		"inspect every configured root recursively",
		"Final response must be compact JSON",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("learning prompt does not contain %q: %s", expected, prompt)
		}
	}
}

func TestInspectLearningDocsDistinguishesRegularFiles(t *testing.T) {
	docsPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(docsPath, learnSystemDocumentNames[0]), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(docsPath, learnSystemDocumentNames[1]), 0o700); err != nil {
		t.Fatal(err)
	}
	written, missing := inspectLearningDocs(docsPath)
	if len(written) != 1 || len(missing) != len(learnSystemDocumentNames)-1 {
		t.Fatalf("inspectLearningDocs = (%v, %v), want one written and the rest missing", written, missing)
	}
}

func TestValidateLearningTargetsRejectsSymlinkAndDirectory(t *testing.T) {
	docsPath := t.TempDir()
	if err := os.Mkdir(filepath.Join(docsPath, learnSystemDocumentNames[0]), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := validateLearningTargets(docsPath); err == nil {
		t.Fatal("directory documentation target was accepted")
	}
	if err := os.Remove(filepath.Join(docsPath, learnSystemDocumentNames[0])); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(docsPath, "outside.md")
	if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(docsPath, learnSystemDocumentNames[0])); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	if err := validateLearningTargets(docsPath); err == nil {
		t.Fatal("symbolic-link documentation target was accepted")
	}
}
