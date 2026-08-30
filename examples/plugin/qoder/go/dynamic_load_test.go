package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
)

func TestDynamicPluginLoadsWithValidMetadataAndSchema5Capabilities(t *testing.T) {
	if raceEnabled {
		t.Skip("c-shared dynamic loading starts a second Go runtime; validate it separately from -race")
	}
	if runtime.GOOS == "windows" {
		t.Skip("c-shared dynamic-load smoke is covered on Unix hosts")
	}
	dir := t.TempDir()
	extension := pluginhost.PluginExtension(runtime.GOOS)
	library := filepath.Join(dir, "qoder-v0.1.0"+extension)
	command := exec.Command("go", "build", "-buildmode=c-shared", "-o", library, ".")
	if output, errBuild := command.CombinedOutput(); errBuild != nil {
		t.Fatalf("c-shared build failed: %v\n%s", errBuild, output)
	}
	helper := exec.Command(os.Args[0], "-test.run=TestDynamicPluginLoadHelper")
	helper.Env = append(os.Environ(), "GO_WANT_QODER_DYNAMIC_LOAD=1", "QODER_PLUGIN_LIBRARY="+library)
	if output, errHelper := helper.CombinedOutput(); errHelper != nil {
		t.Fatalf("dynamic plugin load helper failed: %v\n%s", errHelper, output)
	}
}

func TestDynamicPluginLoadHelper(t *testing.T) {
	if os.Getenv("GO_WANT_QODER_DYNAMIC_LOAD") != "1" {
		return
	}
	library := os.Getenv("QODER_PLUGIN_LIBRARY")
	dir := filepath.Dir(library)
	rawConfig := []byte(fmt.Sprintf("plugins:\n  enabled: true\n  dir: %q\n  configs:\n    qoder:\n      enabled: true\n", dir))
	cfg, errConfig := config.ParseConfigBytes(rawConfig)
	if errConfig != nil {
		t.Fatal(errConfig)
	}
	host := pluginhost.New()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	host.ApplyConfig(ctx, cfg)
	defer host.ShutdownAll()
	if !host.PluginRegistered("qoder") {
		t.Fatalf("Qoder dynamic plugin did not register; loaded=%v", host.PluginLoaded("qoder"))
	}
	plugins := host.RegisteredPlugins()
	if len(plugins) != 1 {
		t.Fatalf("registered plugins = %#v", plugins)
	}
	metadata := plugins[0].Metadata
	if metadata.Name != pluginName || metadata.Version != pluginVersion || metadata.Author == "" || metadata.GitHubRepository != "https://github.com/BlueSkyXN/CPA-Core-LTS" {
		t.Fatalf("metadata = %#v", metadata)
	}
	if !plugins[0].SupportsOAuth || plugins[0].OAuthProvider != pluginIdentifier {
		t.Fatalf("auth registration = %#v", plugins[0])
	}
}
