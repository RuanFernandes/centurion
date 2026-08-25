package project

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/RuanFernandes/centurion/internal/model"
	"github.com/RuanFernandes/centurion/internal/security"
)

const (
	DirectoryName = ".centurion"
	ManifestName  = "project.json"
	ExportName    = "exports"
)

type Manifest struct {
	SchemaVersion int      `json:"schemaVersion"`
	ProjectID     string   `json:"projectID"`
	Name          string   `json:"name"`
	Description   string   `json:"description,omitempty"`
	Folders       []string `json:"folders"`
	UpdatedAt     string   `json:"updatedAt"`
}

func ManifestPath(folders []string) (string, error) {
	root, err := primaryRoot(folders)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, DirectoryName, ManifestName), nil
}

func WriteManifest(value model.Project) (string, error) {
	path, err := ManifestPath(value.Folders)
	if err != nil {
		return "", err
	}
	manifest := Manifest{
		SchemaVersion: 1,
		ProjectID:     value.ID,
		Name:          value.Name,
		Description:   value.Description,
		Folders:       append([]string(nil), value.Folders...),
		UpdatedAt:     value.UpdatedAt,
	}
	if manifest.UpdatedAt == "" {
		manifest.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode project manifest: %w", err)
	}
	if err := atomicWrite(path, append(encoded, '\n')); err != nil {
		return "", fmt.Errorf("write project manifest: %w", err)
	}
	if err := ensureGitignore(filepath.Dir(path)); err != nil {
		return "", err
	}
	return path, nil
}

func ReadManifest(path string) (Manifest, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return Manifest{}, errors.New("manifest path is required")
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(encoded, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode project manifest: %w", err)
	}
	if manifest.SchemaVersion != 1 || strings.TrimSpace(manifest.ProjectID) == "" || strings.TrimSpace(manifest.Name) == "" {
		return Manifest{}, errors.New("project manifest is invalid or unsupported")
	}
	return manifest, nil
}

func WriteSnapshot(value model.ProjectSnapshot) (string, error) {
	manifestPath, err := ManifestPath(value.Project.Folders)
	if err != nil {
		return "", err
	}
	directory := filepath.Join(filepath.Dir(manifestPath), ExportName)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("create project export directory: %w", err)
	}
	if value.SchemaVersion == 0 {
		value.SchemaVersion = 1
	}
	if value.ExportedAt == "" {
		value.ExportedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode project snapshot: %w", err)
	}
	name := "snapshot-" + time.Now().UTC().Format("20060102-150405.000000000") + ".json"
	path := filepath.Join(directory, name)
	if err := atomicWrite(path, append(encoded, '\n')); err != nil {
		return "", fmt.Errorf("write project snapshot: %w", err)
	}
	return path, nil
}

func primaryRoot(folders []string) (string, error) {
	if len(folders) == 0 {
		return "", errors.New("at least one project folder is required")
	}
	roots, err := security.NormalizeRoots(folders)
	if err != nil {
		return "", err
	}
	if len(roots) == 0 {
		return "", errors.New("at least one project folder is required")
	}
	return roots[0], nil
}

func atomicWrite(path string, data []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".centurion-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err == nil {
		return nil
	} else if _, statErr := os.Stat(path); statErr == nil {
		// Windows does not replace an existing file with os.Rename. The
		// destination is the exact manifest path selected by the user, so
		// remove only that file before retrying the move.
		if removeErr := os.Remove(path); removeErr != nil {
			return err
		}
		return os.Rename(temporaryPath, path)
	} else {
		return err
	}
}

func ensureGitignore(directory string) error {
	path := filepath.Join(directory, ".gitignore")
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	content := "# Centurion local state\nstate/\n*.db\n*.db-*\nlogs/\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return fmt.Errorf("write project state gitignore: %w", err)
	}
	return nil
}
