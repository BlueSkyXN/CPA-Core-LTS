//go:build !windows

package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSecureAuthOperationsRejectAncestorSymlinkSwap(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T, string, string, func()) error
	}{
		{
			name: "read",
			run: func(t *testing.T, baseDir, relativePath string, hook func()) error {
				t.Helper()
				data, _, err := secureReadAuthFileWithRootHook(baseDir, relativePath, hook)
				if len(data) != 0 {
					t.Fatalf("secure read returned outside data %q", data)
				}
				return err
			},
		},
		{
			name: "write",
			run: func(_ *testing.T, baseDir, relativePath string, hook func()) error {
				return secureWriteAuthFileWithRootHook(baseDir, relativePath, []byte("replacement"), hook)
			},
		},
		{
			name: "delete",
			run: func(_ *testing.T, baseDir, relativePath string, hook func()) error {
				return secureRemoveAuthFileWithRootHook(baseDir, relativePath, hook)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSecureAuthAncestorSwapFixture(t)
			if err := test.run(t, fixture.authDir, fixture.relativePath, fixture.swapToOutsideSymlink); err == nil {
				t.Fatalf("secure %s accepted an auth-root ancestor symlink swap", test.name)
			}
			assertFileContents(t, fixture.outsideFile, "outside")
			assertFileContents(t, fixture.originalFileAfterSwap, "inside")
		})
	}
}

func TestSecureAuthRootRejectsAncestorDirectoryReplacement(t *testing.T) {
	fixture := newSecureAuthAncestorSwapFixture(t)
	err := secureWriteAuthFileWithRootHook(fixture.authDir, fixture.relativePath, []byte("replacement"), func() {
		if errRename := os.Rename(fixture.ancestor, fixture.originalAncestor); errRename != nil {
			t.Fatalf("move original auth ancestor: %v", errRename)
		}
		if errRename := os.Rename(fixture.outsideAncestor, fixture.ancestor); errRename != nil {
			t.Fatalf("replace auth ancestor with outside directory: %v", errRename)
		}
	})
	if err == nil {
		t.Fatal("secure write accepted a different auth root after canonicalization")
	}
	assertFileContents(t, filepath.Join(fixture.ancestor, "auths", fixture.relativePath), "outside")
	assertFileContents(t, fixture.originalFileAfterSwap, "inside")
}

func TestSecureAuthRootRejectsSymlinkLeaf(t *testing.T) {
	root := t.TempDir()
	relativePath := filepath.Join("team", "token.json")
	outsideAuthDir := filepath.Join(root, "outside-auths")
	if err := os.MkdirAll(filepath.Join(outsideAuthDir, "team"), 0o700); err != nil {
		t.Fatalf("create outside auth directory: %v", err)
	}
	outsideFile := filepath.Join(outsideAuthDir, relativePath)
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o600); err != nil {
		t.Fatalf("write outside auth fixture: %v", err)
	}
	authDir := filepath.Join(root, "auths")
	if err := os.Symlink(outsideAuthDir, authDir); err != nil {
		if errors.Is(err, os.ErrPermission) {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatalf("create auth-root symlink: %v", err)
	}

	data, _, err := secureReadAuthFile(authDir, relativePath)
	if err == nil {
		t.Fatalf("secure read accepted an auth-root symlink and returned %q", data)
	}
	assertFileContents(t, outsideFile, "outside")
}

type secureAuthAncestorSwapFixture struct {
	authDir               string
	relativePath          string
	ancestor              string
	originalAncestor      string
	outsideAncestor       string
	outsideFile           string
	originalFileAfterSwap string
	swapToOutsideSymlink  func()
}

func newSecureAuthAncestorSwapFixture(t *testing.T) secureAuthAncestorSwapFixture {
	t.Helper()
	root := t.TempDir()
	ancestor := filepath.Join(root, "managed")
	originalAncestor := filepath.Join(root, "managed-original")
	outsideAncestor := filepath.Join(root, "outside")
	relativePath := filepath.Join("team", "token.json")
	authDir := filepath.Join(ancestor, "auths")
	outsideAuthDir := filepath.Join(outsideAncestor, "auths")
	if err := os.MkdirAll(filepath.Join(authDir, "team"), 0o700); err != nil {
		t.Fatalf("create managed auth directory: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(outsideAuthDir, "team"), 0o700); err != nil {
		t.Fatalf("create outside auth directory: %v", err)
	}
	insideFile := filepath.Join(authDir, relativePath)
	outsideFile := filepath.Join(outsideAuthDir, relativePath)
	if err := os.WriteFile(insideFile, []byte("inside"), 0o600); err != nil {
		t.Fatalf("write managed auth fixture: %v", err)
	}
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o600); err != nil {
		t.Fatalf("write outside auth fixture: %v", err)
	}

	return secureAuthAncestorSwapFixture{
		authDir:               authDir,
		relativePath:          relativePath,
		ancestor:              ancestor,
		originalAncestor:      originalAncestor,
		outsideAncestor:       outsideAncestor,
		outsideFile:           outsideFile,
		originalFileAfterSwap: filepath.Join(originalAncestor, "auths", relativePath),
		swapToOutsideSymlink: func() {
			if err := os.Rename(ancestor, originalAncestor); err != nil {
				t.Fatalf("move original auth ancestor: %v", err)
			}
			if err := os.Symlink(outsideAncestor, ancestor); err != nil {
				if errors.Is(err, os.ErrPermission) {
					t.Skipf("symlink unavailable: %v", err)
				}
				t.Fatalf("replace auth ancestor with symlink: %v", err)
			}
		},
	}
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
