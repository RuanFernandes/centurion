package security

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var ErrPathOutsideWorkspace = errors.New("path is outside the configured workspace")

func NormalizePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	abs = filepath.Clean(abs)
	if info, statErr := os.Stat(abs); statErr == nil {
		if real, evalErr := filepath.EvalSymlinks(abs); evalErr == nil {
			abs = filepath.Clean(real)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("workspace root is not a directory: %s", abs)
		}
	}
	return abs, nil
}

func NormalizeRoots(roots []string) ([]string, error) {
	if len(roots) == 0 {
		return nil, errors.New("at least one workspace root is required")
	}
	result := make([]string, 0, len(roots))
	seen := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		normalized, err := NormalizePath(root)
		if err != nil {
			return nil, err
		}
		key := strings.ToLower(normalized)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, normalized)
	}
	return result, nil
}

func IsWithinRoots(path string, roots []string) bool {
	normalized, err := NormalizePath(path)
	if err != nil {
		return false
	}
	for _, root := range roots {
		rootPath, rootErr := NormalizePath(root)
		if rootErr != nil {
			continue
		}
		rel, relErr := filepath.Rel(rootPath, normalized)
		if relErr != nil {
			continue
		}
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return true
		}
	}
	return false
}

func ValidateArtifactPath(path string, roots []string) (string, error) {
	normalized, err := NormalizePath(path)
	if err != nil {
		return "", err
	}
	if !IsWithinRoots(normalized, roots) {
		return "", ErrPathOutsideWorkspace
	}
	return normalized, nil
}

func SandboxPolicy(roots []string, networkAccess bool) map[string]any {
	policy := map[string]any{
		"type":          "workspaceWrite",
		"writableRoots": append([]string(nil), roots...),
		"networkAccess": networkAccess,
	}
	return policy
}

func ApprovalPolicy(profile string) string {
	switch strings.ToLower(strings.TrimSpace(profile)) {
	case "never", "autonomous":
		return "never"
	case "trusted", "unless_trusted":
		return "unlessTrusted"
	default:
		return "onRequest"
	}
}
