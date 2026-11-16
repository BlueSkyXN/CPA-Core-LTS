package watcher

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type erroringPluginParser struct{}

func (erroringPluginParser) ParseAuth(context.Context, pluginapi.AuthParseRequest) (*auth.Auth, bool, error) {
	return nil, false, context.Canceled
}

type unhandledPluginParser struct{}

func (unhandledPluginParser) ParseAuth(context.Context, pluginapi.AuthParseRequest) (*auth.Auth, bool, error) {
	return nil, false, nil
}

// A plugin auth parser that fails on the provider must stop generic credential
// synthesis on the real watcher snapshot path (no StrictFileParsing), instead of
// silently synthesizing a plain file credential for the same JSON.
func TestSnapshotCoreAuthsPluginParseErrorSkipsGenericCredential(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "acct.json")
	if errWrite := os.WriteFile(path, []byte(`{"type":"codex","email":"a@b.c"}`), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	w := &Watcher{authDir: dir, config: &config.Config{AuthDir: dir}}
	w.SetPluginAuthParser(erroringPluginParser{})

	auths := w.SnapshotCoreAuths()
	for _, a := range auths {
		if a.Attributes["source_backend"] == "file" || a.ID == "acct.json" {
			t.Fatalf("SnapshotCoreAuths() = %#v, want no generic credential synthesized from the plugin-error file", auths)
		}
	}
}

// The file-event path must not register a generic replacement credential (and
// must not emit an Add/Modify update) when the plugin parser reports an error.
func TestAddOrUpdateClientPluginParseErrorDoesNotRegisterGeneric(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "acct.json")
	if errWrite := os.WriteFile(path, []byte(`{"type":"codex","email":"a@b.c"}`), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	w := &Watcher{authDir: dir, config: &config.Config{AuthDir: dir}, authQueue: make(chan AuthUpdate, 1)}
	w.SetPluginAuthParser(erroringPluginParser{})

	w.addOrUpdateClient(path)
	if update, queued := takeQueuedAuthUpdate(w, "acct.json"); queued {
		t.Fatalf("addOrUpdateClient() queued %v, want no Add/Modify for a plugin parse error", update.Action)
	}
	w.clientsMutex.RLock()
	_, registered := w.currentAuths["acct.json"]
	_, byPath := w.fileAuthsByPath[w.normalizeAuthPath(path)]
	w.clientsMutex.RUnlock()
	if registered || byPath {
		t.Fatalf("generic credential registered for plugin parse error: currentAuths=%v fileAuthsByPath=%v", registered, byPath)
	}
}

// A parser that declines the file (handled=false, err=nil) keeps the builtin
// file synthesis fallback on both real watcher entry points.
func TestWatcherPluginUnhandledKeepsBuiltinFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "acct.json")
	if errWrite := os.WriteFile(path, []byte(`{"type":"codex","email":"a@b.c"}`), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	w := &Watcher{authDir: dir, config: &config.Config{AuthDir: dir}, authQueue: make(chan AuthUpdate, 1)}
	w.SetPluginAuthParser(unhandledPluginParser{})

	auths := w.SnapshotCoreAuths()
	found := false
	for _, a := range auths {
		if a.ID == "acct.json" && a.Provider == "codex" {
			found = true
		}
	}
	if !found {
		t.Fatalf("SnapshotCoreAuths() = %#v, want builtin codex credential for unhandled file", auths)
	}

	w.addOrUpdateClient(path)
	update, queued := takeQueuedAuthUpdate(w, "acct.json")
	if !queued || update.Action != AuthUpdateActionAdd {
		t.Fatalf("addOrUpdateClient() queued=%v action=%v, want builtin Add", queued, update.Action)
	}
}
