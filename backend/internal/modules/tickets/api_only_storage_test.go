package tickets

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestC5APIOnlyAttachmentStorageNeverInitializesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "existing-instance-storage")
	storage, err := OpenAttachmentStorage(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("opening API-only storage initialized a directory")
	}
	if _, err := storage.Save("image/png", []byte("synthetic attachment")); err == nil {
		t.Fatal("API-only storage silently initialized missing storage on write")
	}
	// Default startup still creates the configured directory; the API-only object
	// can immediately reuse it without a second directory or changed source.
	normal, err := NewAttachmentStorage(dir)
	if err != nil {
		t.Fatal(err)
	}
	name, err := normal.Save("image/png", []byte("synthetic attachment"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := storage.Read(name)
	if err != nil || string(data) != "synthetic attachment" {
		t.Fatal("API-only storage does not share the original attachment directory")
	}
}
