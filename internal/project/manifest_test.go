package project

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/RuanFernandes/centurion/internal/model"
)

func TestWriteManifestCreatesProjectMetadataAndIgnoresLocalState(t *testing.T) {
	root := t.TempDir()
	value := model.Project{ID: "project-test", Name: "Test project", Folders: []string{root}, UpdatedAt: "2026-08-25T00:00:00Z"}

	path, err := WriteManifest(value)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := ReadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ProjectID != value.ID || manifest.Name != value.Name || len(manifest.Folders) != 1 {
		t.Fatalf("unexpected manifest: %#v", manifest)
	}
	if _, err := os.Stat(filepath.Join(root, DirectoryName, ".gitignore")); err != nil {
		t.Fatalf("expected local state gitignore: %v", err)
	}
	value.Name = "Updated project"
	if _, err := WriteManifest(value); err != nil {
		t.Fatalf("expected an existing manifest to be replaceable: %v", err)
	}
	updated, err := ReadManifest(path)
	if err != nil || updated.Name != value.Name {
		t.Fatalf("updated manifest was not persisted: %#v, %v", updated, err)
	}
}

func TestWriteSnapshotUsesPrivateExportDirectory(t *testing.T) {
	root := t.TempDir()
	path, err := WriteSnapshot(model.ProjectSnapshot{Project: model.Project{ID: "project-test", Name: "Test project", Folders: []string{root}}})
	if err != nil {
		t.Fatal(err)
	}
	expectedDirectory := filepath.Join(root, DirectoryName, ExportName)
	expectedInfo, expectedErr := os.Stat(expectedDirectory)
	actualInfo, actualErr := os.Stat(filepath.Dir(path))
	if expectedErr != nil || actualErr != nil || !os.SameFile(expectedInfo, actualInfo) {
		t.Fatalf("snapshot escaped project export directory: %s", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestProjectConfigRoundTrip(t *testing.T) {
	root := t.TempDir()
	value := model.ProjectConfig{
		ProjectID: "project-config-test",
		Folders:   []string{root},
		Agents:    []model.AgentProfile{{ID: "agent-test", Name: "Builder", Role: "Implementer"}},
		Workflows: []model.WorkflowDefinition{{ID: "workflow-test", Name: "Build", Version: 1}},
	}

	path, err := WriteConfig(value)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != ConfigName || filepath.Base(filepath.Dir(path)) != DirectoryName {
		t.Fatalf("config was written outside .centurion: %s", path)
	}
	loaded, err := ReadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SchemaVersion != 1 || loaded.ProjectID != value.ProjectID || len(loaded.Agents) != 1 || len(loaded.Workflows) != 1 {
		t.Fatalf("project config round-trip failed: %#v", loaded)
	}
}
