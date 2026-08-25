package security

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateArtifactPathBlocksTraversal(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "artifacts", "result.json")
	outside := filepath.Join(filepath.Dir(root), "outside.json")
	expectedInside, err := NormalizePath(inside)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ValidateArtifactPath(inside, []string{root}); err != nil || got != expectedInside {
		t.Fatalf("expected path inside workspace, got %q, %v", got, err)
	}
	if _, err := ValidateArtifactPath(outside, []string{root}); err == nil {
		t.Fatal("outside path was accepted")
	}
	if _, err := ValidateArtifactPath(filepath.Join(root, "..", "outside.json"), []string{root}); err == nil {
		t.Fatal("path traversal was accepted")
	}
	if err := os.Mkdir(filepath.Join(root, "artifacts"), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestRedactSensitiveText(t *testing.T) {
	value := `Authorization: Bearer abc.def.ghi api_key=super-secret cookie=session-value`
	redacted := RedactSensitiveText(value)
	for _, secret := range []string{"abc.def.ghi", "super-secret", "session-value"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("secret %q was not redacted: %s", secret, redacted)
		}
	}
}

func TestApprovalPolicyUsesCurrentAppServerVariants(t *testing.T) {
	tests := map[string]string{
		"":                  "on-request",
		"on_request":        "on-request",
		"on-request":        "on-request",
		"onRequest":         "on-request",
		"approval":          "on-request",
		"approval_required": "on-request",
		"trusted":           "untrusted",
		"unless_trusted":    "untrusted",
		"unless-trusted":    "untrusted",
		"untrusted":         "untrusted",
		"granular":          "granular",
		"autonomous":        "never",
		"never":             "never",
	}
	for profile, expected := range tests {
		if got := ApprovalPolicy(profile); got != expected {
			t.Errorf("ApprovalPolicy(%q) = %q, want %q", profile, got, expected)
		}
	}
}
