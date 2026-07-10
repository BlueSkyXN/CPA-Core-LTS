package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("ltsregistry", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "repository root containing docs/lts registries")
	baseRef := flags.String("base-ref", "", "optional git ref used to enforce append-only patch history")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "ltsregistry: unexpected arguments: %v\n", flags.Args())
		return 2
	}

	if strings.TrimSpace(*baseRef) == "" {
		*baseRef = discoverBaseRef(*root)
	}
	if err := validateRootAgainst(*root, *baseRef); err != nil {
		fmt.Fprintf(stderr, "ltsregistry: %v\n", err)
		return 1
	}

	fmt.Fprintln(stdout, "CPA-Core-LTS registry validation passed.")
	return 0
}

func discoverBaseRef(root string) string {
	if ref := strings.TrimSpace(os.Getenv("LTS_REGISTRY_BASE_REF")); ref != "" {
		return ref
	}
	if err := exec.Command("git", "-C", root, "rev-parse", "--verify", "origin/main^{commit}").Run(); err != nil {
		return ""
	}
	out, err := exec.Command("git", "-C", root, "merge-base", "HEAD", "origin/main").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
