package store

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

var errObjectStoreTestWrite = errors.New("stop after credential write")

type writeThenFailTokenStorage struct {
	called bool
}

func (s *writeThenFailTokenStorage) SaveTokenToFile(path string) error {
	s.called = true
	if err := os.WriteFile(path, []byte(`{"type":"test"}`), 0o600); err != nil {
		return err
	}
	return errObjectStoreTestWrite
}

type noOpTokenStorage struct{}

func (*noOpTokenStorage) SaveTokenToFile(string) error { return nil }

type successfulTokenStorage struct{}

func (*successfulTokenStorage) SaveTokenToFile(path string) error {
	return os.WriteFile(path, []byte(`{"type":"test"}`), 0o600)
}

type rawJSONTokenStorage struct {
	called bool
}

func (s *rawJSONTokenStorage) SaveTokenToFile(string) error {
	s.called = true
	return errors.New("path writer must not be called")
}

func (*rawJSONTokenStorage) RawJSON() []byte { return []byte(`{"type":"plugin"}`) }

func TestRenderAuthStoragePreservesNoOpWriter(t *testing.T) {
	stagingDir, stagingRoot := mustPrepareAuthRenderStaging(t)
	raw, wrote, err := renderAuthStorage(&noOpTokenStorage{}, stagingDir, stagingRoot)
	if err != nil {
		t.Fatalf("render no-op token storage: %v", err)
	}
	if wrote {
		t.Fatalf("render no-op token storage reported a file with contents %q", raw)
	}
}

func TestRenderAuthStoragePrefersPathFreeRawJSON(t *testing.T) {
	storage := &rawJSONTokenStorage{}
	stagingDir, stagingRoot := mustPrepareAuthRenderStaging(t)
	raw, wrote, err := renderAuthStorage(storage, stagingDir, stagingRoot)
	if err != nil {
		t.Fatalf("render raw JSON token storage: %v", err)
	}
	if !wrote || string(raw) != `{"type":"plugin"}` {
		t.Fatalf("rendered raw JSON = %q, wrote=%v", raw, wrote)
	}
	if storage.called {
		t.Fatal("path-based token writer was called despite RawJSON support")
	}
}

func TestObjectTokenStoreRejectsAuthPathsOutsideAuthDir(t *testing.T) {
	authDir := t.TempDir()
	outsideDir := t.TempDir()
	outsidePath := filepath.Join(outsideDir, "token.json")
	if err := os.WriteFile(outsidePath, []byte(`{"type":"codex"}`), 0o600); err != nil {
		t.Fatalf("write outside auth: %v", err)
	}
	store := &ObjectTokenStore{authDir: authDir, authRoot: mustCaptureSecureAuthRootIdentity(t, authDir)}

	checks := []struct {
		name string
		call func() error
	}{
		{
			name: "absolute attribute path",
			call: func() error {
				_, err := store.resolveAuthPath(&cliproxyauth.Auth{
					ID:         "outside",
					Attributes: map[string]string{cliproxyauth.AttributePath: outsidePath},
				})
				return err
			},
		},
		{
			name: "filename traversal",
			call: func() error {
				_, err := store.resolveAuthPath(&cliproxyauth.Auth{ID: "traversal", FileName: "../token.json"})
				return err
			},
		},
		{
			name: "absolute delete path",
			call: func() error {
				_, err := store.resolveDeletePath(outsidePath)
				return err
			},
		},
		{name: "upload", call: func() error { return store.uploadAuth(context.Background(), outsidePath) }},
		{name: "remote delete", call: func() error { return store.deleteAuthObject(context.Background(), outsidePath) }},
		{name: "persist files", call: func() error { return store.PersistAuthFiles(context.Background(), "", outsidePath) }},
	}

	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.call(); err == nil {
				t.Fatal("accepted auth path outside auth directory")
			}
		})
	}
}

func TestObjectTokenStoreAcceptsNestedAuthPathInsideAuthDir(t *testing.T) {
	authDir := t.TempDir()
	store := &ObjectTokenStore{authDir: authDir, authRoot: mustCaptureSecureAuthRootIdentity(t, authDir)}

	got, err := store.resolveAuthPath(&cliproxyauth.Auth{ID: "nested", FileName: "team/token"})
	if err != nil {
		t.Fatalf("resolveAuthPath returned error: %v", err)
	}
	want := filepath.Join(authDir, "team", "token.json")
	if got != want {
		t.Fatalf("resolveAuthPath = %q, want %q", got, want)
	}
}

func TestObjectTokenStoreSecureWriterInstallsNestedAuthFile(t *testing.T) {
	authDir := t.TempDir()
	store := &ObjectTokenStore{authDir: authDir, authRoot: mustCaptureSecureAuthRootIdentity(t, authDir)}

	path, err := store.writeAuthMirrorFile("team/token.json", []byte(`{"type":"codex"}`))
	if err != nil {
		t.Fatalf("first secure mirror write: %v", err)
	}
	if want := filepath.Join(authDir, "team", "token.json"); path != want {
		t.Fatalf("secure mirror path = %q, want %q", path, want)
	}
	if err = os.WriteFile(filepath.Join(authDir, "team", "unrelated.json"), []byte(`{"keep":true}`), 0o600); err != nil {
		t.Fatalf("write unrelated auth fixture: %v", err)
	}
	if _, err = store.writeAuthMirrorFile("team/token.json", []byte(`{"type":"replacement"}`)); err != nil {
		t.Fatalf("replace secure mirror file: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read secure mirror file: %v", err)
	}
	if string(got) != `{"type":"replacement"}` {
		t.Fatalf("secure mirror contents = %q, want replacement", got)
	}
	if _, err = os.Stat(filepath.Join(authDir, "team", "unrelated.json")); err != nil {
		t.Fatalf("secure replacement disturbed sibling file: %v", err)
	}
}

func TestObjectTokenStoreSecureWriterSupportsLongAuthFileName(t *testing.T) {
	authDir := t.TempDir()
	store := &ObjectTokenStore{authDir: authDir, authRoot: mustCaptureSecureAuthRootIdentity(t, authDir)}
	fileName := strings.Repeat("a", 240) + ".json"

	path, err := store.writeAuthMirrorFile(fileName, []byte(`{"type":"codex"}`))
	if err != nil {
		t.Fatalf("secure mirror write with long auth filename: %v", err)
	}
	if got, errRead := os.ReadFile(path); errRead != nil || string(got) != `{"type":"codex"}` {
		t.Fatalf("long auth filename contents = %q, err = %v", got, errRead)
	}
}

func TestObjectTokenStoreRejectsSiblingPrefixAndSymlinkEscapes(t *testing.T) {
	root := t.TempDir()
	authDir := filepath.Join(root, "auths")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("create auth dir: %v", err)
	}
	store := &ObjectTokenStore{authDir: authDir, authRoot: mustCaptureSecureAuthRootIdentity(t, authDir)}

	siblingPath := filepath.Join(root, "auths-other", "token.json")
	if _, err := store.resolveDeletePath(siblingPath); err == nil {
		t.Fatal("accepted sibling path sharing the auth directory prefix")
	}

	outsideDir := t.TempDir()
	linkPath := filepath.Join(authDir, "escape")
	if err := os.Symlink(outsideDir, linkPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := store.resolveAuthPath(&cliproxyauth.Auth{ID: "symlink", FileName: "escape/token.json"}); err == nil {
		t.Fatal("accepted auth path through symlink outside auth directory")
	}
}

func TestObjectTokenStoreRejectsDanglingLeafSymlinkBeforeCredentialWrite(t *testing.T) {
	root := t.TempDir()
	authDir := filepath.Join(root, "auths")
	outsideDir := filepath.Join(root, "outside")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("create auth dir: %v", err)
	}
	if err := os.MkdirAll(outsideDir, 0o700); err != nil {
		t.Fatalf("create outside dir: %v", err)
	}

	outsidePath := filepath.Join(outsideDir, "created-by-symlink.json")
	linkPath := filepath.Join(authDir, "dangling.json")
	if err := os.Symlink(outsidePath, linkPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	storage := &writeThenFailTokenStorage{}
	store := &ObjectTokenStore{authDir: authDir, authRoot: mustCaptureSecureAuthRootIdentity(t, authDir)}
	_, err := store.Save(context.Background(), &cliproxyauth.Auth{
		ID:       "dangling",
		FileName: "dangling.json",
		Storage:  storage,
	})
	if err == nil {
		t.Fatal("Save() accepted dangling symlink auth path")
	}
	if storage.called {
		t.Fatal("credential storage was invoked for dangling symlink auth path")
	}
	if _, errStat := os.Stat(outsidePath); !errors.Is(errStat, os.ErrNotExist) {
		t.Fatalf("outside target was created through dangling symlink: %v", errStat)
	}
}

func TestObjectTokenStoreRejectsDirectorySymlinkSwappedAfterValidation(t *testing.T) {
	root := t.TempDir()
	authDir := filepath.Join(root, "auths")
	outsideDir := filepath.Join(root, "outside")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("create auth dir: %v", err)
	}
	if err := os.MkdirAll(outsideDir, 0o700); err != nil {
		t.Fatalf("create outside dir: %v", err)
	}

	store := &ObjectTokenStore{authDir: authDir, authRoot: mustCaptureSecureAuthRootIdentity(t, authDir)}
	store.beforeAuthWriteHook = func() {
		if err := os.Symlink(outsideDir, filepath.Join(authDir, "team")); err != nil {
			t.Fatalf("swap auth directory with symlink: %v", err)
		}
	}

	_, err := store.Save(context.Background(), &cliproxyauth.Auth{
		ID:       "swapped",
		FileName: "team/token.json",
		Metadata: map[string]any{"type": "codex"},
	})
	if err == nil {
		t.Fatal("Save() followed a directory symlink introduced after path validation")
	}
	if _, errStat := os.Stat(filepath.Join(outsideDir, "token.json")); !errors.Is(errStat, os.ErrNotExist) {
		t.Fatalf("outside target was created after path swap: %v", errStat)
	}
}

func TestObjectTokenStoreMirrorWriteRejectsDirectorySymlinkSwappedAfterValidation(t *testing.T) {
	root := t.TempDir()
	authDir := filepath.Join(root, "auths")
	outsideDir := filepath.Join(root, "outside")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("create auth dir: %v", err)
	}
	if err := os.MkdirAll(outsideDir, 0o700); err != nil {
		t.Fatalf("create outside dir: %v", err)
	}

	store := &ObjectTokenStore{authDir: authDir, authRoot: mustCaptureSecureAuthRootIdentity(t, authDir)}
	store.beforeAuthWriteHook = func() {
		if err := os.Symlink(outsideDir, filepath.Join(authDir, "remote")); err != nil {
			t.Fatalf("swap mirror directory with symlink: %v", err)
		}
	}

	if _, err := store.writeAuthMirrorFile("remote/token.json", []byte(`{"type":"test"}`)); err == nil {
		t.Fatal("bucket mirror write followed a directory symlink introduced after validation")
	}
	if _, errStat := os.Stat(filepath.Join(outsideDir, "token.json")); !errors.Is(errStat, os.ErrNotExist) {
		t.Fatalf("bucket mirror wrote outside authDir after path swap: %v", errStat)
	}
}

func TestObjectTokenStoreDeleteRejectsPathSwapWithoutRemoteDelete(t *testing.T) {
	remoteDeleteCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.RawQuery, "location") {
			_, _ = w.Write([]byte(`<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/"></LocationConstraint>`))
			return
		}
		if r.Method == http.MethodDelete {
			remoteDeleteCalls++
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	store, authDir, outsideDir := newPathSwapObjectStore(t, server.URL)
	localDir := filepath.Join(authDir, "team")
	localPath := filepath.Join(localDir, "token.json")
	if err := os.MkdirAll(localDir, 0o700); err != nil {
		t.Fatalf("create local auth directory: %v", err)
	}
	if err := os.WriteFile(localPath, []byte(`{"type":"local"}`), 0o600); err != nil {
		t.Fatalf("write local auth: %v", err)
	}
	externalPath := filepath.Join(outsideDir, "token.json")
	if err := os.WriteFile(externalPath, []byte(`{"type":"external"}`), 0o600); err != nil {
		t.Fatalf("write external auth: %v", err)
	}
	store.beforeAuthDeleteHook = pathSwapHook(t, authDir, "team", outsideDir)

	if err := store.Delete(context.Background(), "team/token.json"); err == nil {
		t.Fatal("Delete() followed a directory symlink introduced after validation")
	}
	if got, err := os.ReadFile(externalPath); err != nil || string(got) != `{"type":"external"}` {
		t.Fatalf("external auth changed after Delete(): data=%q err=%v", got, err)
	}
	if remoteDeleteCalls != 0 {
		t.Fatalf("remote delete calls = %d, want 0", remoteDeleteCalls)
	}
}

func TestObjectTokenStoreUploadRejectsPathSwapWithoutExternalReadOrUpload(t *testing.T) {
	remotePutCalls := 0
	var uploadedPayload []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.RawQuery, "location") {
			_, _ = w.Write([]byte(`<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/"></LocationConstraint>`))
			return
		}
		if r.Method == http.MethodPut {
			remotePutCalls++
			uploadedPayload, _ = io.ReadAll(r.Body)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	store, authDir, outsideDir := newPathSwapObjectStore(t, server.URL)
	localDir := filepath.Join(authDir, "team")
	localPath := filepath.Join(localDir, "token.json")
	if err := os.MkdirAll(localDir, 0o700); err != nil {
		t.Fatalf("create local auth directory: %v", err)
	}
	if err := os.WriteFile(localPath, []byte(`{"type":"local"}`), 0o600); err != nil {
		t.Fatalf("write local auth: %v", err)
	}
	externalPath := filepath.Join(outsideDir, "token.json")
	if err := os.WriteFile(externalPath, []byte(`{"type":"external"}`), 0o600); err != nil {
		t.Fatalf("write external auth: %v", err)
	}
	store.beforeAuthReadHook = pathSwapHook(t, authDir, "team", outsideDir)

	if err := store.uploadAuth(context.Background(), localPath); err == nil {
		t.Fatal("uploadAuth() followed a directory symlink introduced after validation")
	}
	if remotePutCalls != 0 {
		t.Fatalf("remote upload calls = %d, want 0; uploaded payload=%q", remotePutCalls, uploadedPayload)
	}
}

func TestObjectTokenStoreUploadMissingAuthDeletesRemoteObject(t *testing.T) {
	remoteDeleteCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.RawQuery, "location") {
			_, _ = w.Write([]byte(`<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/"></LocationConstraint>`))
			return
		}
		if r.Method == http.MethodDelete {
			remoteDeleteCalls++
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	store, authDir, _ := newPathSwapObjectStore(t, server.URL)
	if err := store.uploadAuth(context.Background(), filepath.Join(authDir, "missing.json")); err != nil {
		t.Fatalf("upload missing auth: %v", err)
	}
	if remoteDeleteCalls != 1 {
		t.Fatalf("remote delete calls = %d, want 1", remoteDeleteCalls)
	}
}

func TestObjectTokenStoreListSkipsPathSwappedExternalAuth(t *testing.T) {
	store, authDir, outsideDir := newPathSwapObjectStore(t, "")
	localDir := filepath.Join(authDir, "team")
	if err := os.MkdirAll(localDir, 0o700); err != nil {
		t.Fatalf("create local auth directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "token.json"), []byte(`{"type":"local"}`), 0o600); err != nil {
		t.Fatalf("write local auth: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outsideDir, "token.json"), []byte(`{"type":"external","email":"external@example.com"}`), 0o600); err != nil {
		t.Fatalf("write external auth: %v", err)
	}
	store.beforeAuthReadHook = pathSwapHook(t, authDir, "team", outsideDir)

	auths, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(auths) != 0 {
		t.Fatalf("List() loaded path-swapped external auth: %#v", auths)
	}
}

func TestObjectTokenStoreRejectsAuthRootReplacementAfterPreflight(t *testing.T) {
	type operation struct {
		name string
		run  func(context.Context, *ObjectTokenStore, string, func()) error
	}
	operations := []operation{
		{
			name: "write",
			run: func(_ context.Context, store *ObjectTokenStore, _ string, hook func()) error {
				store.beforeAuthWriteHook = hook
				_, err := store.writeAuthMirrorFile("team/token.json", []byte(`{"type":"replacement"}`))
				return err
			},
		},
		{
			name: "read",
			run: func(ctx context.Context, store *ObjectTokenStore, authDir string, hook func()) error {
				store.beforeAuthReadHook = hook
				return store.uploadAuth(ctx, filepath.Join(authDir, "team", "token.json"))
			},
		},
		{
			name: "delete",
			run: func(ctx context.Context, store *ObjectTokenStore, _ string, hook func()) error {
				store.beforeAuthDeleteHook = hook
				return store.Delete(ctx, "team/token.json")
			},
		},
		{
			name: "list",
			run: func(ctx context.Context, store *ObjectTokenStore, _ string, hook func()) error {
				hook()
				_, err := store.List(ctx)
				return err
			},
		},
	}

	for _, replacement := range []string{"symlink", "directory", "missing"} {
		for _, operation := range operations {
			t.Run(replacement+"/"+operation.name, func(t *testing.T) {
				remotePutCalls := 0
				remoteDeleteCalls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodGet && strings.HasPrefix(r.URL.RawQuery, "location") {
						_, _ = w.Write([]byte(`<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/"></LocationConstraint>`))
						return
					}
					switch r.Method {
					case http.MethodPut:
						remotePutCalls++
					case http.MethodDelete:
						remoteDeleteCalls++
					}
					w.WriteHeader(http.StatusNoContent)
				}))
				defer server.Close()

				store, authDir, originalAuthDir, replacementFixtureAuthDir, replacementObservedAuthDir, hook := newAuthRootReplacementStore(t, server.URL, replacement)
				localPath := filepath.Join(authDir, "team", "token.json")
				if operation.name != "write" {
					if err := os.MkdirAll(filepath.Dir(localPath), 0o700); err != nil {
						t.Fatalf("create original auth directory: %v", err)
					}
					if err := os.WriteFile(localPath, []byte(`{"type":"local"}`), 0o600); err != nil {
						t.Fatalf("write original auth: %v", err)
					}
					if replacement != "missing" {
						outsidePath := filepath.Join(replacementFixtureAuthDir, "team", "token.json")
						if err := os.MkdirAll(filepath.Dir(outsidePath), 0o700); err != nil {
							t.Fatalf("create replacement auth directory: %v", err)
						}
						if err := os.WriteFile(outsidePath, []byte(`{"type":"external"}`), 0o600); err != nil {
							t.Fatalf("write replacement auth: %v", err)
						}
					}
				}

				err := operation.run(context.Background(), store, authDir, hook)
				if err == nil {
					t.Fatal("auth operation accepted a replacement root after store initialization")
				}
				if replacement == "directory" && !errors.Is(err, errObjectStoreAuthRootChanged) {
					t.Fatalf("auth operation error = %v, want root identity change", err)
				}
				if replacement == "missing" && !errors.Is(err, errObjectStoreAuthRootUnavailable) {
					t.Fatalf("auth operation error = %v, want unavailable initialized root", err)
				}

				replacementPath := filepath.Join(replacementObservedAuthDir, "team", "token.json")
				if operation.name == "write" || replacement == "missing" {
					if _, err := os.Stat(replacementPath); !errors.Is(err, fs.ErrNotExist) {
						t.Fatalf("operation reached unavailable/replacement auth root: %v", err)
					}
				} else if got, err := os.ReadFile(replacementPath); err != nil || string(got) != `{"type":"external"}` {
					t.Fatalf("replacement auth changed: data=%q err=%v", got, err)
				}

				originalPath := filepath.Join(originalAuthDir, "team", "token.json")
				if operation.name == "write" {
					if _, err := os.Stat(originalPath); !errors.Is(err, fs.ErrNotExist) {
						t.Fatalf("failed write changed original auth root: %v", err)
					}
				} else if got, err := os.ReadFile(originalPath); err != nil || string(got) != `{"type":"local"}` {
					t.Fatalf("original auth changed: data=%q err=%v", got, err)
				}
				if remotePutCalls != 0 || remoteDeleteCalls != 0 {
					t.Fatalf("remote mutations after root replacement: PUT=%d DELETE=%d", remotePutCalls, remoteDeleteCalls)
				}
			})
		}
	}
}

func TestObjectTokenStoreSaveRejectsAuthRootRemovalBeforeUpload(t *testing.T) {
	remotePutCalls := 0
	remoteDeleteCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.RawQuery, "location") {
			_, _ = w.Write([]byte(`<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/"></LocationConstraint>`))
			return
		}
		switch r.Method {
		case http.MethodPut:
			remotePutCalls++
		case http.MethodDelete:
			remoteDeleteCalls++
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	managedRoot := filepath.Join(t.TempDir(), "managed")
	store, err := NewObjectTokenStore(ObjectStoreConfig{
		Endpoint:  strings.TrimPrefix(server.URL, "http://"),
		Bucket:    "test-bucket",
		AccessKey: "access",
		SecretKey: "secret",
		LocalRoot: managedRoot,
	})
	if err != nil {
		t.Fatalf("create object token store: %v", err)
	}
	movedRoot := managedRoot + "-moved"
	store.beforeAuthReadHook = func() {
		if err := os.Rename(managedRoot, movedRoot); err != nil {
			t.Fatalf("move initialized object-store root before upload: %v", err)
		}
	}

	_, err = store.Save(context.Background(), &cliproxyauth.Auth{
		ID:       "root-removed-before-upload",
		FileName: filepath.Join("team", "token.json"),
		Storage:  &successfulTokenStorage{},
	})
	if !errors.Is(err, errObjectStoreAuthRootUnavailable) {
		t.Fatalf("Save() error = %v, want unavailable initialized root", err)
	}
	if remotePutCalls != 0 || remoteDeleteCalls != 0 {
		t.Fatalf("remote mutations after root removal: PUT=%d DELETE=%d", remotePutCalls, remoteDeleteCalls)
	}
	writtenPath := filepath.Join(movedRoot, "auths", "team", "token.json")
	if got, err := os.ReadFile(writtenPath); err != nil || string(got) != `{"type":"test"}` {
		t.Fatalf("credential written before root removal = %q, err=%v", got, err)
	}
}

func newAuthRootReplacementStore(t *testing.T, serverURL, replacement string) (*ObjectTokenStore, string, string, string, string, func()) {
	t.Helper()
	container := t.TempDir()
	managedRoot := filepath.Join(container, "managed")
	store, err := NewObjectTokenStore(ObjectStoreConfig{
		Endpoint:  strings.TrimPrefix(serverURL, "http://"),
		Bucket:    "test-bucket",
		AccessKey: "access",
		SecretKey: "secret",
		LocalRoot: managedRoot,
	})
	if err != nil {
		t.Fatalf("create object token store: %v", err)
	}

	originalRoot := managedRoot + "-original"
	replacementRoot := filepath.Join(t.TempDir(), "replacement")
	replacementAuthDir := filepath.Join(replacementRoot, "auths")
	replacementObservedAuthDir := replacementAuthDir
	if err = os.MkdirAll(replacementAuthDir, 0o700); err != nil {
		t.Fatalf("create replacement root: %v", err)
	}
	if replacement == "symlink" {
		probe := managedRoot + "-symlink-probe"
		if err = os.Symlink(replacementRoot, probe); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if err = os.Remove(probe); err != nil {
			t.Fatalf("remove symlink probe: %v", err)
		}
	} else {
		replacementObservedAuthDir = filepath.Join(managedRoot, "auths")
	}

	called := false
	hook := func() {
		if called {
			return
		}
		called = true
		if err = os.Rename(managedRoot, originalRoot); err != nil {
			t.Fatalf("move initialized object-store root: %v", err)
		}
		if replacement == "symlink" {
			if err = os.Symlink(replacementRoot, managedRoot); err != nil {
				t.Fatalf("replace object-store root with symlink: %v", err)
			}
			return
		}
		if replacement == "missing" {
			return
		}
		if err = os.Rename(replacementRoot, managedRoot); err != nil {
			t.Fatalf("replace object-store root with directory: %v", err)
		}
	}
	return store, store.authDir, filepath.Join(originalRoot, "auths"), replacementAuthDir, replacementObservedAuthDir, hook
}

func newPathSwapObjectStore(t *testing.T, serverURL string) (*ObjectTokenStore, string, string) {
	t.Helper()
	root := t.TempDir()
	authDir := filepath.Join(root, "auths")
	outsideDir := filepath.Join(root, "outside")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("create auth directory: %v", err)
	}
	if err := os.MkdirAll(outsideDir, 0o700); err != nil {
		t.Fatalf("create outside directory: %v", err)
	}
	store := &ObjectTokenStore{authDir: authDir, authRoot: mustCaptureSecureAuthRootIdentity(t, authDir)}
	if serverURL != "" {
		client, err := minio.New(strings.TrimPrefix(serverURL, "http://"), &minio.Options{
			Creds:  credentials.NewStaticV4("access", "secret", ""),
			Secure: false,
		})
		if err != nil {
			t.Fatalf("create minio client: %v", err)
		}
		store.client = client
		store.cfg.Bucket = "test-bucket"
	}
	return store, authDir, outsideDir
}

func pathSwapHook(t *testing.T, authDir, component, outsideDir string) func() {
	t.Helper()
	called := false
	return func() {
		if called {
			return
		}
		called = true
		path := filepath.Join(authDir, component)
		if err := os.Rename(path, path+"-original"); err != nil {
			t.Fatalf("move validated auth directory: %v", err)
		}
		if err := os.Symlink(outsideDir, path); err != nil {
			t.Fatalf("replace validated auth directory with symlink: %v", err)
		}
	}
}

func mustCaptureSecureAuthRootIdentity(t *testing.T, authDir string) secureAuthRootIdentity {
	t.Helper()
	identity, err := captureSecureAuthRootIdentity(authDir)
	if err != nil {
		t.Fatalf("capture secure auth root identity: %v", err)
	}
	return identity
}

func mustPrepareAuthRenderStaging(t *testing.T) (string, secureAuthRootIdentity) {
	t.Helper()
	spoolRoot := t.TempDir()
	spoolIdentity := mustCaptureSecureAuthRootIdentity(t, spoolRoot)
	stagingDir, stagingRoot, err := prepareAuthRenderStaging(spoolRoot, spoolIdentity)
	if err != nil {
		t.Fatalf("prepare auth render staging: %v", err)
	}
	return stagingDir, stagingRoot
}

func assertFileContents(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("contents of %s = %q, want %q", path, got, want)
	}
}
