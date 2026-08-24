package model

import "testing"

func TestDefaultSystemPromptsUseNonNilVariableLists(t *testing.T) {
	for _, prompt := range DefaultSystemPrompts() {
		if prompt.Variables == nil {
			t.Fatalf("prompt %q returned a nil variables list", prompt.ID)
		}
	}
}
