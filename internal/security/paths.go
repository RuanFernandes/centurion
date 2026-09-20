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
	return resolvePathWithExistingParent(filepath.Clean(abs))
}

func resolvePathWithExistingParent(path string) (string, error) {
	current := filepath.Clean(path)
	var unresolved []string

	for {
		if _, err := os.Lstat(current); err == nil {
			resolved, evalErr := filepath.EvalSymlinks(current)
			if evalErr != nil {
				return "", fmt.Errorf("resolve symlinks: %w", evalErr)
			}
			resolved = filepath.Clean(resolved)
			for index := len(unresolved) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, unresolved[index])
			}
			return filepath.Clean(resolved), nil
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("inspect path: %w", err)
		}

		parent := filepath.Dir(current)
		if parent == current {
			return current, nil
		}
		unresolved = append(unresolved, filepath.Base(current))
		current = parent
	}
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
		info, err := os.Stat(normalized)
		if err != nil {
			return nil, fmt.Errorf("inspect workspace root: %w", err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("workspace root is not a directory: %s", normalized)
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
		rel = filepath.ToSlash(rel)
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, "../")) {
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

// ReadOnlySandboxPolicy prevents profile-level read-only agents from carrying
// a previous workspace-write policy into a resumed App Server thread. When
// roots are known, read access is limited to those roots as well.
func ReadOnlySandboxPolicy(roots []string) map[string]any {
	policy := map[string]any{"type": "readOnly"}
	if len(roots) == 0 {
		return policy
	}
	policy["access"] = map[string]any{
		"type":                    "restricted",
		"includePlatformDefaults": true,
		"readableRoots":           append([]string(nil), roots...),
	}
	return policy
}

func ApprovalPolicy(profile string) string {
	switch strings.ToLower(strings.TrimSpace(profile)) {
	case "never", "autonomous":
		return "never"
	case "untrusted":
		return "untrusted"
	case "trusted", "unless_trusted", "unless-trusted":
		// Keep the legacy Centurion profile meaningful with the current App
		// Server protocol. The old unlessTrusted value was renamed to untrusted.
		return "untrusted"
	case "granular":
		return "granular"
	case "on_request", "on-request", "onrequest", "approval", "approval_required":
		return "on-request"
	default:
		return "on-request"
	}
}
