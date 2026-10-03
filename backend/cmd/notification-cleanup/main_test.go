package main

import "testing"

func TestCleanupDefaultsToPreviewAndRequiresSeparateWriteApproval(t *testing.T) {
	preview := cleanupOptions{userID: "user", workspaceID: "workspace"}
	if err := validateOptions(preview); err != nil {
		t.Fatal(err)
	}
	if preview.apply || preview.approved || preview.restore != "" || preview.backup != "" {
		t.Fatal("preview must have no write options")
	}
	for _, opts := range []cleanupOptions{
		{userID: "user", workspaceID: "workspace", apply: true},
		{userID: "user", workspaceID: "workspace", apply: true, approved: true, backup: "review.json"},
		{userID: "user", workspaceID: "workspace", restore: "review.json"},
	} {
		if validateOptions(opts) == nil {
			t.Fatal("write without scope, digest, approval or backup was accepted")
		}
	}
}
