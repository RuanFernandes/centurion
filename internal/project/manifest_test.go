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
