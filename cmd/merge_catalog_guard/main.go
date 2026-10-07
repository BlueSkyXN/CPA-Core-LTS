// Command merge_catalog_guard merges LTS catalog guard entries from the
// embedded catalogs into remote catalog refresh candidates, in place.
//
// CI catalog refresh calls it on freshly fetched remote candidates before
// replacing the repository catalogs, so a remote catalog that does not ship
// the guard models yet still updates every other entry instead of being
// rejected wholesale. The embedded source is compiled from the current
// repository catalogs, so running the tool before the files are replaced uses
// the pre-refresh embedded state, mirroring the runtime guard merge; once the
// remote candidate carries the same IDs, the remote entries win and the merge
// is a no-op.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func main() {
	modelsPath := flag.String("models", "", "path to a remote models.json candidate to guard-merge in place")
	codexClientPath := flag.String("codex-client", "", "path to a remote codex_client_models.json candidate to guard-merge in place")
	flag.Parse()

	if *modelsPath == "" && *codexClientPath == "" {
		fmt.Fprintln(os.Stderr, "merge_catalog_guard: at least one of --models or --codex-client is required")
		os.Exit(2)
	}

	ok := true
	if *modelsPath != "" {
		ok = mergeFile(*modelsPath, "models.json", registry.MergeLTSModelsCatalogFile) && ok
	}
	if *codexClientPath != "" {
		ok = mergeFile(*codexClientPath, "codex_client_models.json", registry.MergeLTSCodexClientCatalogFile) && ok
	}
	if !ok {
		os.Exit(1)
	}
}

func mergeFile(path, catalogName string, merge func([]byte) ([]byte, []string, error)) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "merge_catalog_guard: read %s: %v\n", path, err)
		return false
	}
	merged, entries, err := merge(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "merge_catalog_guard: %v\n", err)
		return false
	}
	if len(entries) == 0 {
		fmt.Printf("%s: no guard entries missing.\n", catalogName)
		return true
	}
	if err := os.WriteFile(path, merged, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "merge_catalog_guard: write %s: %v\n", path, err)
		return false
	}
	fmt.Printf("%s: merged guard entries:", catalogName)
	for _, entry := range entries {
		fmt.Printf(" %s", entry)
	}
	fmt.Println()
	return true
}
