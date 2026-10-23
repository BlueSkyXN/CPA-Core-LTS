package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestObjectStoreCodexSavePreservesIndependentCredentialPartitions(t *testing.T) {
	for _, mode := range []string{"metadata", "storage", "siwc-refresh"} {
		t.Run(mode, func(t *testing.T) {
			store, authDir, _ := newPathSwapObjectStore(t, "")
			var uploaded []byte
			store.putObjectFn = func(_ context.Context, _ string, data []byte, _ string) error {
				uploaded = append([]byte(nil), data...)
				return nil
			}
			before := map[string]any{"type": "codex", "access_token": "outer-old", "refresh_token": "outer-refresh", "siwc": map[string]any{"access_token": "siwc-old", "refresh_token": "siwc-refresh"}}
			auth := &cliproxyauth.Auth{ID: "auth.json", Provider: "codex", Metadata: before}
			if _, err := store.Save(cliproxyauth.WithAuthCreationIntent(context.Background()), auth); err != nil {
				t.Fatal(err)
			}
			stale := auth.Clone()
			disk := auth.Clone()
			disk.Metadata["access_token"] = "outer-current"
			disk.Metadata["note"] = "operator edit"
			raw, err := json.Marshal(disk.Metadata)
			if err != nil {
				t.Fatal(err)
			}
			if err := secureWriteAuthFile(authDir, store.authRoot, "auth.json", raw); err != nil {
				t.Fatal(err)
			}
			wantSIWC := "siwc-old"
			switch mode {
			case "storage":
				stale.Storage = &codex.CodexTokenStorage{AccessToken: "outer-old", RefreshToken: "outer-refresh"}
			case "siwc-refresh":
				updated := stale.Clone()
				updated.Metadata["siwc"].(map[string]any)["access_token"] = "siwc-current"
				stale.CodexRefreshPatch = codex.NewCredentialRefreshPatch(stale.Metadata, updated.Metadata, false, true)
				stale.Metadata = updated.Metadata
				wantSIWC = "siwc-current"
			}
			if _, err := store.Save(context.Background(), stale); err != nil {
				t.Fatal(err)
			}
			var saved map[string]any
			if err := json.Unmarshal(uploaded, &saved); err != nil {
				t.Fatal(err)
			}
			if saved["access_token"] != "outer-current" || saved["siwc"].(map[string]any)["access_token"] != wantSIWC {
				t.Fatal("save overwrote an independent credential partition")
			}
			if mode == "siwc-refresh" && saved["note"] != "operator edit" {
				t.Fatal("scoped refresh discarded a concurrent metadata edit")
			}
			if stale.Metadata["access_token"] != "outer-current" {
				t.Fatal("returned auth did not adopt persisted credentials")
			}
			info, err := os.Stat(filepath.Join(authDir, "auth.json"))
			if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
				t.Fatalf("credential permissions: %v, %v", info, err)
			}
		})
	}
}

func TestObjectStoreCodexSaveRejectsParentSwap(t *testing.T) {
	store, authDir, outsideDir := newPathSwapObjectStore(t, "")
	store.putObjectFn = func(context.Context, string, []byte, string) error { return nil }
	auth := &cliproxyauth.Auth{ID: "nested/auth.json", Provider: "codex", Metadata: map[string]any{"type": "codex", "access_token": "test-token"}}
	if _, err := store.Save(cliproxyauth.WithAuthCreationIntent(context.Background()), auth); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(outsideDir, "auth.json")
	if err := os.WriteFile(outside, []byte("outside sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	store.beforeAuthWriteHook = pathSwapHook(t, authDir, "nested", outsideDir)
	auth.Storage = &codex.CodexTokenStorage{AccessToken: "replacement"}
	if _, err := store.Save(context.Background(), auth); err == nil {
		t.Fatal("Codex save accepted a swapped parent directory")
	}
	got, err := os.ReadFile(outside)
	if err != nil || string(got) != "outside sentinel" {
		t.Fatal("Codex save changed the outside file")
	}
}
