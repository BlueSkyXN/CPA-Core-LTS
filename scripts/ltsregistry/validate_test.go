package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateRoot(t *testing.T) {
	root := writeValidFixture(t)
	if err := validateRoot(root); err != nil {
		t.Fatalf("validateRoot() unexpected error:\n%v", err)
	}
}

func TestRun(t *testing.T) {
	root := writeValidFixture(t)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"--root", root}, &stdout, &stderr); code != 0 {
		t.Fatalf("run() code = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "registry validation passed") {
		t.Fatalf("run() stdout = %q", stdout.String())
	}
}

func TestRetiredPatchMayReferenceDeletedCode(t *testing.T) {
	root := writeValidFixture(t)
	retiredIn := initGitRepo(t, root)
	replaceFile(t, root, downstreamPatchesPath, "state: required", "state: retired")
	replaceFile(t, root, downstreamPatchesPath, "- internal/demo/demo.go", "- internal/demo/deleted.go")
	replaceFile(t, root, downstreamPatchesPath, "- internal/demo/demo_test.go", "- internal/demo/deleted_test.go")
	replaceFile(t, root, downstreamPatchesPath, "- TestDemo", "- TestDeletedBehavior")
	replaceFile(t, root, downstreamPatchesPath, "upstream_issue_or_pr: not-filed", "upstream_issue_or_pr: example/upstream#123")
	replaceFile(t, root, downstreamPatchesPath, "retired_in: \"\"", "retired_in: "+retiredIn)
	if err := validateRoot(root); err != nil {
		t.Fatalf("validateRoot() retired patch unexpected error:\n%v", err)
	}
}

func TestPatchHistoryRejectsDeletionAndSkippedRetirement(t *testing.T) {
	t.Run("deleted historical id", func(t *testing.T) {
		root := writeValidFixture(t)
		baseRef := initGitRepo(t, root)
		writeFixtureFile(t, root, downstreamPatchesPath, "version: 1\npatches: []\n")
		err := validateRootAgainst(root, baseRef)
		if err == nil || !strings.Contains(err.Error(), "historical patch \"patch-a\"") {
			t.Fatalf("validateRootAgainst() error = %v, want historical patch deletion", err)
		}
	})

	t.Run("required directly to retired", func(t *testing.T) {
		root := writeValidFixture(t)
		baseRef := initGitRepo(t, root)
		replaceFile(t, root, downstreamPatchesPath, "state: required", "state: retired")
		replaceFile(t, root, downstreamPatchesPath, "upstream_issue_or_pr: not-filed", "upstream_issue_or_pr: example/upstream#123")
		replaceFile(t, root, downstreamPatchesPath, "retired_in: \"\"", "retired_in: "+baseRef)
		err := validateRootAgainst(root, baseRef)
		if err == nil || !strings.Contains(err.Error(), "invalid state transition \"required\" -> \"retired\"") {
			t.Fatalf("validateRootAgainst() error = %v, want skipped retirement rejection", err)
		}
	})

	t.Run("introduced commit rewritten", func(t *testing.T) {
		root := writeValidFixture(t)
		baseRef := initGitRepo(t, root)
		replacePrefixedLine(t, root, downstreamPatchesPath, "    introduced_in:", "    introduced_in: deadbeef")
		err := validateRootAgainst(root, baseRef)
		if err == nil || !strings.Contains(err.Error(), "must not rewrite introduced_in") {
			t.Fatalf("validateRootAgainst() error = %v, want introduced_in rewrite rejection", err)
		}
	})

	t.Run("retired provenance rewritten", func(t *testing.T) {
		root := writeValidFixture(t)
		introducedRef := initGitRepo(t, root)
		replaceFile(t, root, downstreamPatchesPath, "state: required", "state: retired")
		replaceFile(t, root, downstreamPatchesPath, "upstream_issue_or_pr: not-filed", "upstream_issue_or_pr: example/upstream#123")
		replaceFile(t, root, downstreamPatchesPath, "retired_in: \"\"", "retired_in: "+introducedRef)
		baseRef := commitAll(t, root, "retire fixture patch")
		replaceFile(t, root, downstreamPatchesPath, "reason: Preserve downstream behavior until upstream matches it.", "reason: Rewritten retired history.")
		err := validateRootAgainst(root, baseRef)
		if err == nil || !strings.Contains(err.Error(), "retired patch \"patch-a\" is immutable") {
			t.Fatalf("validateRootAgainst() error = %v, want retired history immutability rejection", err)
		}
	})

	t.Run("upstreamed may conservatively return to required", func(t *testing.T) {
		root := writeValidFixture(t)
		replaceFile(t, root, downstreamPatchesPath, "state: required", "state: upstreamed")
		replaceFile(t, root, downstreamPatchesPath, "upstream_issue_or_pr: not-filed", "upstream_issue_or_pr: example/upstream#123")
		baseRef := initGitRepo(t, root)
		replaceFile(t, root, downstreamPatchesPath, "state: upstreamed", "state: required")
		if err := validateRootAgainst(root, baseRef); err != nil {
			t.Fatalf("validateRootAgainst() conservative rollback unexpected error:\n%v", err)
		}
	})
}

func TestValidationFailures(t *testing.T) {
	tests := []struct {
		name       string
		want       string
		mutateRoot func(t *testing.T, root string)
	}{
		{
			name: "unknown field",
			want: "field unexpected not found",
			mutateRoot: func(t *testing.T, root string) {
				appendFile(t, root, featureRegistryPath, "\nunexpected: true\n")
			},
		},
		{
			name: "registry version",
			want: "version must be 2",
			mutateRoot: func(t *testing.T, root string) {
				replaceFile(t, root, featureRegistryPath, "version: 2", "version: 1")
			},
		},
		{
			name: "weakened merge policy",
			want: "sync_rules.merge_commit_required: must be true",
			mutateRoot: func(t *testing.T, root string) {
				replaceFile(t, root, protectedDeltasPath, "merge_commit_required: true", "merge_commit_required: false")
			},
		},
		{
			name: "weakened patch provenance merge policy",
			want: "sync_rules.patch_provenance_merge_commit_required: must be true",
			mutateRoot: func(t *testing.T, root string) {
				replaceFile(t, root, protectedDeltasPath, "patch_provenance_merge_commit_required: true", "patch_provenance_merge_commit_required: false")
			},
		},
		{
			name: "automatic operation mode",
			want: "maintenance_model.operation_mode: must be \"manual-ai-assisted\"",
			mutateRoot: func(t *testing.T, root string) {
				replaceFile(t, root, protectedDeltasPath, "operation_mode: manual-ai-assisted", "operation_mode: fully-automatic")
			},
		},
		{
			name: "missing forbidden shortcut",
			want: "missing required value \"GitHub Sync fork\"",
			mutateRoot: func(t *testing.T, root string) {
				replaceFile(t, root, protectedDeltasPath, "- GitHub Sync fork", "- harmless-placeholder")
			},
		},
		{
			name: "wrong registry reference",
			want: "must reference \"docs/lts/core-feature-contracts.yaml\"",
			mutateRoot: func(t *testing.T, root string) {
				writeFixtureFile(t, root, "docs/lts/other-registry.yaml", "version: 2\n")
				replaceFile(t, root, protectedDeltasPath, "feature_contract_registry: docs/lts/core-feature-contracts.yaml", "feature_contract_registry: docs/lts/other-registry.yaml")
			},
		},
		{
			name: "duplicate id",
			want: "duplicate id \"feature-a\"",
			mutateRoot: func(t *testing.T, root string) {
				replaceFile(t, root, featureRegistryPath, "  - id: feature-b", "  - id: feature-a")
			},
		},
		{
			name: "invalid enum",
			want: "invalid value \"fork\"",
			mutateRoot: func(t *testing.T, root string) {
				replaceFile(t, root, featureRegistryPath, "kind: retained-capability", "kind: fork")
			},
		},
		{
			name: "invalid classification combination",
			want: "retained-capability must be protected",
			mutateRoot: func(t *testing.T, root string) {
				replaceFile(t, root, featureRegistryPath, "support: protected", "support: optional")
			},
		},
		{
			name: "dangling feature reference",
			want: "unknown feature \"missing-feature\"",
			mutateRoot: func(t *testing.T, root string) {
				replaceFile(t, root, protectedDeltasPath, "registry_feature: feature-a", "registry_feature: missing-feature")
			},
		},
		{
			name: "duplicate feature reference",
			want: "duplicate reference to \"feature-a\"",
			mutateRoot: func(t *testing.T, root string) {
				replaceFile(t, root, protectedDeltasPath, "registry_feature: feature-b", "registry_feature: feature-a")
			},
		},
		{
			name: "missing validation",
			want: "features[0].validation: must contain at least one value",
			mutateRoot: func(t *testing.T, root string) {
				replaceFile(t, root, featureRegistryPath, "    validation:\n      - go test ./internal/demo", "    validation: []")
			},
		},
		{
			name: "route without source evidence",
			want: "route \"/v0/missing\" has no evidence token",
			mutateRoot: func(t *testing.T, root string) {
				replaceFile(t, root, featureRegistryPath, "- /v0/example", "- /v0/missing")
			},
		},
		{
			name: "config key without source evidence",
			want: "config key \"demo.nonexistent-key\" does not exist in config.example.yaml or the internal/config Go schema",
			mutateRoot: func(t *testing.T, root string) {
				replaceFile(t, root, featureRegistryPath, "- demo.enabled", "- demo.nonexistent-key")
			},
		},
		{
			name: "missing regression test",
			want: "Go test function \"TestMissing\" not found",
			mutateRoot: func(t *testing.T, root string) {
				replaceFile(t, root, downstreamPatchesPath, "- TestDemo", "- TestMissing")
			},
		},
		{
			name: "invalid regression test signature",
			want: "Go test function \"TestDemo\" not found",
			mutateRoot: func(t *testing.T, root string) {
				writeFixtureFile(t, root, "internal/demo/demo_test.go", "package demo\n\nfunc TestDemo() {}\n")
			},
		},
		{
			name: "regression test parameter is not testing T",
			want: "Go test function \"TestDemo\" not found",
			mutateRoot: func(t *testing.T, root string) {
				writeFixtureFile(t, root, "internal/demo/demo_test.go", "package demo\n\nimport fake \"example.invalid/fake\"\n\nfunc TestDemo(t *fake.T) {}\n")
			},
		},
		{
			name: "regression test has multiple parameters",
			want: "Go test function \"TestDemo\" not found",
			mutateRoot: func(t *testing.T, root string) {
				writeFixtureFile(t, root, "internal/demo/demo_test.go", "package demo\n\nimport \"testing\"\n\nfunc TestDemo(a, b *testing.T) {}\n")
			},
		},
		{
			name: "regression test is excluded from Go build",
			want: "Go tool did not list active regression test \"TestDemo\"",
			mutateRoot: func(t *testing.T, root string) {
				writeFixtureFile(t, root, "go.mod", "module example.invalid/ltsregistryfixture\n\ngo 1.23\n")
				writeFixtureFile(t, root, "internal/demo/demo_test.go", "//go:build never\n\npackage demo\n\nimport \"testing\"\n\nfunc TestDemo(t *testing.T) {}\n")
			},
		},
		{
			name: "missing retire when",
			want: ".retire_when: must not be empty",
			mutateRoot: func(t *testing.T, root string) {
				replaceFile(t, root, downstreamPatchesPath, "retire_when: upstream ships the same behavior", "retire_when: \"\"")
			},
		},
		{
			name: "missing upstream issue or pr",
			want: ".upstream_issue_or_pr: must not be empty",
			mutateRoot: func(t *testing.T, root string) {
				replaceFile(t, root, downstreamPatchesPath, "upstream_issue_or_pr: not-filed", "upstream_issue_or_pr: \"\"")
			},
		},
		{
			name: "invalid introduced commit",
			want: ".introduced_in: must be a 7-40 character commit SHA",
			mutateRoot: func(t *testing.T, root string) {
				replaceFile(t, root, downstreamPatchesPath, "introduced_in: abcdef12", "introduced_in: release-name")
			},
		},
		{
			name: "missing affected range",
			want: ".affected_upstream_range: must not be empty",
			mutateRoot: func(t *testing.T, root string) {
				replaceFile(t, root, downstreamPatchesPath, "affected_upstream_range: v1.0.0..HEAD", "affected_upstream_range: \"\"")
			},
		},
		{
			name: "empty patch ledger",
			want: ".patches: must contain at least one historical or active patch",
			mutateRoot: func(t *testing.T, root string) {
				writeFixtureFile(t, root, downstreamPatchesPath, "version: 1\npatches: []\n")
			},
		},
		{
			name: "upstreamed patch missing upstream evidence",
			want: "state \"upstreamed\" requires concrete upstream evidence",
			mutateRoot: func(t *testing.T, root string) {
				replaceFile(t, root, downstreamPatchesPath, "state: required", "state: upstreamed")
			},
		},
		{
			name: "marker only in registry",
			want: "marker \"registry-only-marker\" has no evidence",
			mutateRoot: func(t *testing.T, root string) {
				replaceFile(t, root, featureRegistryPath,
					"    core_files:\n      - internal/demo/demo.go\n    required_markers:\n      - feature-a-marker",
					"    core_files:\n      - docs/lts/core-feature-contracts.yaml\n    required_markers:\n      - registry-only-marker")
			},
		},
		{
			name: "marker only in file path",
			want: "marker \"demo.go\" has no evidence",
			mutateRoot: func(t *testing.T, root string) {
				replaceFile(t, root, featureRegistryPath, "- feature-a-marker", "- demo.go")
			},
		},
		{
			name: "missing file",
			want: "path \"internal/demo/missing.go\"",
			mutateRoot: func(t *testing.T, root string) {
				replaceFile(t, root, downstreamPatchesPath, "- internal/demo/demo.go", "- internal/demo/missing.go")
			},
		},
		{
			name: "retired missing retired in",
			want: ".retired_in: must not be empty",
			mutateRoot: func(t *testing.T, root string) {
				replaceFile(t, root, downstreamPatchesPath, "state: required", "state: retired")
			},
		},
		{
			name: "retired invalid commit",
			want: ".retired_in: must be a 7-40 character commit SHA",
			mutateRoot: func(t *testing.T, root string) {
				replaceFile(t, root, downstreamPatchesPath, "state: required", "state: retired")
				replaceFile(t, root, downstreamPatchesPath, "upstream_issue_or_pr: not-filed", "upstream_issue_or_pr: example/upstream#123")
				replaceFile(t, root, downstreamPatchesPath, "retired_in: \"\"", "retired_in: arbitrary-text")
			},
		},
		{
			name: "active declares retired in",
			want: "active patch must not declare retired_in",
			mutateRoot: func(t *testing.T, root string) {
				replaceFile(t, root, downstreamPatchesPath, "state: required", "state: removable")
				replaceFile(t, root, downstreamPatchesPath, "retired_in: \"\"", "retired_in: deadbeef")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := writeValidFixture(t)
			tt.mutateRoot(t, root)
			err := validateRoot(root)
			if err == nil {
				t.Fatalf("validateRoot() error = nil, want substring %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validateRoot() error:\n%v\nwant substring %q", err, tt.want)
			}
		})
	}
}

func writeValidFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFixtureFile(t, root, "docs/lts/sync-runbook.md", "# Sync runbook\n")
	writeFixtureFile(t, root, "config.example.yaml", "demo:\n  enabled: true\n")
	writeFixtureFile(t, root, "internal/demo/demo.go", "package demo\n\nconst FeatureAMarker = \"feature-a-marker\"\nconst FeatureBMarker = \"feature-b-marker\"\nconst ProfileMarker = \"profile-marker\"\nconst ExampleRoute = \"/v0/example\"\nconst DemoConfigKey = \"demo.enabled\"\n")
	writeFixtureFile(t, root, "internal/demo/demo_test.go", "package demo\n\nimport \"testing\"\n\nfunc TestDemo(t *testing.T) {}\n")
	writeFixtureFile(t, root, featureRegistryPath, validFeatureRegistryYAML)
	writeFixtureFile(t, root, protectedDeltasPath, validProtectedDeltasYAML)
	writeFixtureFile(t, root, downstreamPatchesPath, validDownstreamPatchesYAML)
	return root
}

func writeFixtureFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func replaceFile(t *testing.T, root, name, old, replacement string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !strings.Contains(string(data), old) {
		t.Fatalf("fixture %s does not contain %q", name, old)
	}
	updated := strings.Replace(string(data), old, replacement, 1)
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func appendFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("append %s: %v", path, err)
	}
}

func replacePrefixedLine(t *testing.T, root, name, prefix, replacement string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	lines := strings.Split(string(data), "\n")
	replaced := false
	for i, line := range lines {
		if strings.HasPrefix(line, prefix) {
			lines[i] = replacement
			replaced = true
			break
		}
	}
	if !replaced {
		t.Fatalf("fixture %s does not contain line prefix %q", name, prefix)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func initGitRepo(t *testing.T, root string) string {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.name", "LTS Registry Test"},
		{"config", "user.email", "lts-registry@example.invalid"},
		{"commit", "--allow-empty", "-qm", "fixture bootstrap"},
	} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	bootstrapOutput, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("git rev-parse bootstrap HEAD: %v", err)
	}
	replaceFile(t, root, downstreamPatchesPath, "introduced_in: abcdef12", "introduced_in: "+strings.TrimSpace(string(bootstrapOutput)))
	for _, args := range [][]string{{"add", "."}, {"commit", "-qm", "fixture baseline"}} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func commitAll(t *testing.T, root, message string) string {
	t.Helper()
	for _, args := range [][]string{{"add", "."}, {"commit", "-qm", message}} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	return strings.TrimSpace(string(out))
}

const validFeatureRegistryYAML = `version: 2
generated_from: docs/lts/sync-runbook.md
maintenance_model: protected-full-sync-with-contract-registry
guard_policy:
  purpose: Keep cheap sentinels around LTS identity surfaces.
  add_marker_when:
    - Marker is stable.
  avoid_marker_when:
    - Marker is an implementation detail.
  behavior_proof:
    - Behavior requires tests.
features:
  - id: feature-a
    kind: retained-capability
    support: protected
    owner: cpa-core-lts
    upstream_relation: divergent
    reason: Feature A is retained by the LTS product.
    routes:
      - /v0/example
    config_keys: []
    core_files:
      - internal/demo/demo.go
    required_markers:
      - feature-a-marker
    validation:
      - go test ./internal/demo
  - id: feature-b
    kind: lts-feature
    support: maintained
    owner: cpa-core-lts
    upstream_relation: downstream-only
    reason: Feature B is maintained downstream.
    routes: []
    config_keys:
      - demo.enabled
    core_files:
      - internal/demo/demo.go
    required_markers:
      - feature-b-marker
    validation:
      - go test ./internal/demo
review_seams:
  - id: seam-a
    support: upstream-shared
    owner: shared
    upstream_relation: upstream-equivalent
    reason: Shared request lifecycle requires review.
    core_files:
      - internal/demo/demo.go
    review_triggers:
      - request lifecycle changes
    validation:
      - go test ./internal/demo
validation_profiles:
  - id: profile-a
    reason: Runtime validation is optional after merge.
    reference_space:
      - example/space
    runtime_checks:
      - /healthz returns ok
    required_markers:
      - profile-marker
    validation:
      - manual smoke
relationships:
  - id: relationship-a
    type: coexist
    from: feature-a
    to: feature-b
    reason: Both features coexist.
`

const validProtectedDeltasYAML = `version: 2
maintenance_model:
  type: protected-full-sync
  product_branch: main
  operation_mode: manual-ai-assisted
  scheduled_sync: false
  cadence_guidance: frequent-small-syncs
  optional_runtime_smoke: manual_huggingface_space
  feature_contract_registry: docs/lts/core-feature-contracts.yaml
  downstream_patch_ledger: docs/lts/downstream-patches.yaml
upstream_source:
  repo: router-for-me/CLIProxyAPI
  branch: main
retained_capabilities:
  - id: retained-a
    registry_feature: feature-a
    must_keep: true
    markers:
      - feature-a-marker
    validation:
      - go test ./internal/demo
    conflict_policy: preserve
lts_owned_features:
  - id: owned-b
    registry_feature: feature-b
    maintenance_policy: maintain downstream
    markers:
      - feature-b-marker
    validation:
      - go test ./internal/demo
    conflict_policy: reapply
shared_review_seams:
  - id: shared-a
    registry_seam: seam-a
    review_required: true
    review_when:
      - request lifecycle changes
    validation:
      - go test ./internal/demo
    conflict_policy: review
sync_rules:
  default_mode: protected_full_sync
  upstream_changes: accept_by_default
  merge_commit_required: true
  squash_forbidden: true
  rebase_forbidden: true
  contract_registry_required: true
  downstream_patch_review_required: true
  patch_provenance_merge_commit_required: true
  forbidden_shortcuts:
    - GitHub Sync fork
    - git pull upstream main on main
    - git checkout upstream/main -- .
runtime_validation_profiles:
  - profile-a
`

const validDownstreamPatchesYAML = `version: 1
patches:
  - id: patch-a
    reason: Preserve downstream behavior until upstream matches it.
    introduced_in: abcdef12
    affected_upstream_range: v1.0.0..HEAD
    files:
      - internal/demo/demo.go
      - internal/demo/demo_test.go
    regression_tests:
      - TestDemo
    upstream_issue_or_pr: not-filed
    state: required
    retire_when: upstream ships the same behavior
    retired_in: ""
`
