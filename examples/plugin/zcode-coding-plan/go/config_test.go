package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateTextRejectsInvalidUTF8(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.txt")
	if err := os.WriteFile(path, []byte{0xff, 0xfe}, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := privateText(path); err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
}

func TestConfigurationRequiresLoggingAcknowledgement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private.json")
	if err := os.WriteFile(path, []byte(`{"host_logging_disabled":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := loadConfig(path)
	if err == nil || safeError(err).Code != "unsafe_host_logging" {
		t.Fatal("logging acknowledgement must precede secret reads")
	}
}
