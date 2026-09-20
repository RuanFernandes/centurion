package main

import "testing"

func TestServerApprovalResponseUsesAppServerAcceptDecision(t *testing.T) {
	for _, method := range []string{
		"item/commandExecution/requestApproval",
		"item/fileChange/requestApproval",
	} {
		response := serverApprovalResponse(&pendingApproval{serverMethod: method}, "approve")
		if response["decision"] != "accept" {
			t.Fatalf("%s response = %#v, want decision=accept", method, response)
		}
	}
	declined := serverApprovalResponse(&pendingApproval{serverMethod: "item/commandExecution/requestApproval"}, "decline")
	if declined["decision"] != "decline" {
		t.Fatalf("declined response = %#v, want decision=decline", declined)
	}
}

func TestServerPermissionApprovalReturnsRequestedSubset(t *testing.T) {
	requested := map[string]any{"fileSystem": map[string]any{"write": []string{"C:\\workspace"}}}
	response := serverApprovalResponse(&pendingApproval{
		serverMethod: "item/permissions/requestApproval",
		serverParams: map[string]any{"permissions": requested},
	}, "accept")
	if response["scope"] != "turn" {
		t.Fatalf("permission scope = %#v, want turn", response["scope"])
	}
	if response["permissions"] == nil {
		t.Fatal("permission response did not include the requested subset")
	}
}
