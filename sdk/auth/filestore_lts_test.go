package auth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestFileTokenStoreListReturnsAuthFileErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "valid.json"), []byte(`{"type":"custom"}`), 0o600); err != nil {
		t.Fatalf("write valid auth: %v", err)
	}
	brokenPath := filepath.Join(dir, "broken.json")
	if err := os.WriteFile(brokenPath, []byte(`{"type":`), 0o600); err != nil {
		t.Fatalf("write broken auth: %v", err)
	}

	store := NewFileTokenStore()
	store.SetBaseDir(dir)
	entries, err := store.List(context.Background())
	if err == nil {
		t.Fatal("List succeeded, want error for broken auth file")
	}
	if entries != nil {
		t.Fatalf("entries = %#v, want nil on error", entries)
	}
	if !strings.Contains(err.Error(), brokenPath) {
		t.Fatalf("error = %q, want broken file path", err.Error())
	}
}

func TestFileTokenStoreListReturnsPluginParserErrors(t *testing.T) {
	baseDir := t.TempDir()
	path := filepath.Join(baseDir, "plugin.json")
	if err := os.WriteFile(path, []byte(`{"type":"plugin-provider"}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	RegisterPluginAuthParser(fileStoreMultiAuthParserFunc(func(context.Context, pluginapi.AuthParseRequest) ([]*cliproxyauth.Auth, bool, error) {
		return nil, true, errors.New("plugin parse failed")
	}))
	t.Cleanup(func() { RegisterPluginAuthParser(nil) })

	store := NewFileTokenStore()
	store.SetBaseDir(baseDir)
	auths, err := store.List(context.Background())
	if err == nil {
		t.Fatal("List succeeded, want plugin parser error")
	}
	if auths != nil {
		t.Fatalf("auths = %#v, want nil on parser error", auths)
	}
	if !strings.Contains(err.Error(), "plugin parse failed") || !strings.Contains(err.Error(), path) {
		t.Fatalf("error = %q, want parser error and file path", err.Error())
	}
}

func TestFileTokenStorePluginAuthOwnsPrefix(t *testing.T) {
	baseDir := t.TempDir()
	path := filepath.Join(baseDir, "plugin.json")
	if err := os.WriteFile(path, []byte(`{"type":"plugin-provider","prefix":"source-prefix"}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	RegisterPluginAuthParser(fileStoreMultiAuthParserFunc(func(context.Context, pluginapi.AuthParseRequest) ([]*cliproxyauth.Auth, bool, error) {
		return []*cliproxyauth.Auth{{ID: "plugin.json", Provider: "plugin-provider", Prefix: "plugin-prefix"}}, true, nil
	}))
	t.Cleanup(func() { RegisterPluginAuthParser(nil) })

	store := NewFileTokenStore()
	store.SetBaseDir(baseDir)
	auths, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(auths) != 1 || auths[0].Prefix != "plugin-prefix" {
		t.Fatalf("auths = %#v, want plugin-owned prefix", auths)
	}
}
