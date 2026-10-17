//go:build cgo && darwin

package pluginhost

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDarwinLibraryLifetime(t *testing.T) {
	for _, kind := range []string{"go", "c", "hybrid"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			library := filepath.Join(dir, "lifetime.dylib")
			marker := filepath.Join(dir, "lifetime.txt")
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			var build *exec.Cmd
			if kind == "go" {
				build = exec.CommandContext(ctx, "go", "build", "-buildmode=c-shared", "-ldflags=-s -w", "-o", library, "./testdata/darwin_library_lifetime")
			} else if kind == "hybrid" {
				archive := filepath.Join(dir, "go_payload.a")
				core := exec.CommandContext(ctx, "go", "build", "-buildmode=c-archive", "-o", archive, "./testdata/darwin_library_lifetime/go_payload")
				if output, err := core.CombinedOutput(); err != nil {
					t.Fatalf("build go payload archive: %v\n%s", err, output)
				}
				build = exec.CommandContext(ctx, "cc", "-dynamiclib", "-DLIFETIME_EMBEDS_GO", "-o", library, "testdata/darwin_library_lifetime/plugin.c", archive)
			} else {
				build = exec.CommandContext(ctx, "cc", "-dynamiclib", "-o", library, "testdata/darwin_library_lifetime/plugin.c")
			}
			if output, err := build.CombinedOutput(); err != nil {
				t.Fatalf("build %s fixture: %v\n%s", kind, err, output)
			}
			expectPinned := kind != "c"
			if keepGoPluginMapped(library) != expectPinned {
				t.Fatalf("%s library residency detection mismatch", kind)
			}
			childCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			child := exec.CommandContext(childCtx, os.Args[0], "-test.run=^TestDarwinLibraryLifetimeHelper$", "-test.timeout=8s")
			child.Env = append(os.Environ(), "CPA_PLUGIN_LIFETIME_LIBRARY="+library, "CPA_PLUGIN_LIFETIME_MARKER="+marker, "CPA_PLUGIN_LIFETIME_KIND="+kind)
			if output, err := child.CombinedOutput(); err != nil {
				t.Fatalf("%s library shutdown: %v\n%s", kind, err, output)
			}
		})
	}
}

func TestDarwinLibraryLifetimeHelper(t *testing.T) {
	library := os.Getenv("CPA_PLUGIN_LIFETIME_LIBRARY")
	if library == "" {
		return
	}
	marker := os.Getenv("CPA_PLUGIN_LIFETIME_MARKER")
	client, err := (dynamicLibraryLoader{}).Open(pluginFile{ID: "lifetime", Path: library}, New())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Shutdown)
	client.Shutdown()
	client.Shutdown()
	content, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "SD" {
		t.Fatalf("shutdown/finalizer markers = %q, want SD", content)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	mapped, err := exec.CommandContext(ctx, "/usr/bin/vmmap", "-w", strconv.Itoa(os.Getpid())).CombinedOutput()
	if err != nil {
		t.Fatalf("inspect mapped library: %v\n%s", err, mapped)
	}
	resident := strings.Contains(string(mapped), library)
	if resident != (os.Getenv("CPA_PLUGIN_LIFETIME_KIND") != "c") {
		t.Fatalf("library resident after shutdown = %t\n%s", resident, mapped)
	}
	closed := client.(*dynamicLibraryClient)
	if closed.handle != nil || closed.hostAPI != nil || closed.hostCtx != nil || !closed.instance.closed.Load() {
		t.Fatal("shutdown retained host resources or an open callback instance")
	}
}
