package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	featureRegistryPath   = "docs/lts/core-feature-contracts.yaml"
	protectedDeltasPath   = "docs/lts/protected-deltas.yaml"
	downstreamPatchesPath = "docs/lts/downstream-patches.yaml"
)

var (
	featureKinds          = stringSet("retained-capability", "lts-feature")
	featureSupports       = stringSet("protected", "maintained", "optional", "upstream-shared")
	featureOwners         = stringSet("cpa-core-lts", "upstream", "shared")
	upstreamRelations     = stringSet("downstream-only", "divergent", "upstream-equivalent", "removed-upstream")
	patchStates           = stringSet("required", "upstreamed", "removable", "retired")
	registryEvidenceFiles = stringSet(featureRegistryPath, protectedDeltasPath, downstreamPatchesPath)
	commitSHA             = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)
)

type registryIndex struct {
	features           map[string]feature
	reviewSeams        map[string]reviewSeam
	validationProfiles map[string]validationProfile
}

type validationErrors []string

func (errs validationErrors) Error() string {
	items := append([]string(nil), errs...)
	sort.Strings(items)
	return "validation failed:\n  - " + strings.Join(items, "\n  - ")
}

type validator struct {
	root               string
	problems           validationErrors
	configLoaded       bool
	configExample      map[string]any
	configErr          error
	configSchemaLoaded bool
	configSchema       map[string]map[string]string
	configSchemaErr    error
}

func validateRoot(root string) error {
	return validateRootAgainst(root, "")
}

func validateRootAgainst(root, baseRef string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve root: %w", err)
	}
	info, err := os.Stat(rootAbs)
	if err != nil {
		return fmt.Errorf("stat root: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("root %q is not a directory", rootAbs)
	}

	var registry featureRegistry
	if err := decodeStrict(filepath.Join(rootAbs, filepath.FromSlash(featureRegistryPath)), &registry); err != nil {
		return fmt.Errorf("%s: %w", featureRegistryPath, err)
	}
	var deltas protectedDeltas
	if err := decodeStrict(filepath.Join(rootAbs, filepath.FromSlash(protectedDeltasPath)), &deltas); err != nil {
		return fmt.Errorf("%s: %w", protectedDeltasPath, err)
	}
	var patches downstreamPatches
	if err := decodeStrict(filepath.Join(rootAbs, filepath.FromSlash(downstreamPatchesPath)), &patches); err != nil {
		return fmt.Errorf("%s: %w", downstreamPatchesPath, err)
	}

	v := &validator{root: rootAbs}
	index := v.validateFeatureRegistry(registry)
	v.validateProtectedDeltas(deltas, index)
	v.validateDownstreamPatches(patches)
	if strings.TrimSpace(baseRef) != "" {
		v.validatePatchHistory(patches, baseRef)
	}
	if len(v.problems) > 0 {
		return v.problems
	}
	v.validateActivePatchTestsWithGo(patches)
	if len(v.problems) > 0 {
		return v.problems
	}
	return nil
}

func decodeStrict(path string, out any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	decoder := yaml.NewDecoder(f)
	decoder.KnownFields(true)
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple YAML documents are not supported")
		}
		return err
	}
	return nil
}

func (v *validator) validateFeatureRegistry(registry featureRegistry) registryIndex {
	index := registryIndex{
		features:           make(map[string]feature),
		reviewSeams:        make(map[string]reviewSeam),
		validationProfiles: make(map[string]validationProfile),
	}
	if registry.Version != 2 {
		v.addf("%s: version must be 2, got %d", featureRegistryPath, registry.Version)
	}
	v.requireString(featureRegistryPath+".generated_from", registry.GeneratedFrom)
	if strings.TrimSpace(registry.GeneratedFrom) != "" {
		v.validatePath(featureRegistryPath+".generated_from", registry.GeneratedFrom, false)
	}
	v.requireString(featureRegistryPath+".maintenance_model", registry.MaintenanceModel)
	v.requireString(featureRegistryPath+".guard_policy.purpose", registry.GuardPolicy.Purpose)
	v.requireStrings(featureRegistryPath+".guard_policy.add_marker_when", registry.GuardPolicy.AddMarkerWhen, true)
	v.requireStrings(featureRegistryPath+".guard_policy.avoid_marker_when", registry.GuardPolicy.AvoidMarkerWhen, true)
	v.requireStrings(featureRegistryPath+".guard_policy.behavior_proof", registry.GuardPolicy.BehaviorProof, true)

	allIDs := make(map[string]string)
	for i, item := range registry.Features {
		label := fmt.Sprintf("%s.features[%d]", featureRegistryPath, i)
		id := v.registerID(allIDs, label, item.ID)
		v.requireEnum(label+".kind", item.Kind, featureKinds)
		v.requireEnum(label+".support", item.Support, featureSupports)
		v.requireEnum(label+".owner", item.Owner, featureOwners)
		v.requireEnum(label+".upstream_relation", item.UpstreamRelation, upstreamRelations)
		v.validateFeatureClassification(label, item)
		v.requireString(label+".reason", item.Reason)
		v.requireStrings(label+".core_files", item.CoreFiles, true)
		v.requireStrings(label+".validation", item.Validation, true)
		v.requireStrings(label+".routes", item.Routes, false)
		v.requireStrings(label+".config_keys", item.ConfigKeys, false)
		v.requireStrings(label+".required_markers", item.RequiredMarkers, false)
		for j, path := range item.CoreFiles {
			v.validatePath(fmt.Sprintf("%s.core_files[%d]", label, j), path, true)
		}
		for j, marker := range item.RequiredMarkers {
			if strings.TrimSpace(marker) == "" {
				continue
			}
			if !v.markerInPaths(marker, item.CoreFiles) {
				v.addf("%s.required_markers[%d]: marker %q has no evidence in core_files outside registry YAML", label, j, marker)
			}
		}
		for j, route := range item.Routes {
			evidence := routeEvidenceToken(route)
			if evidence != "" && !v.markerInPaths(evidence, item.CoreFiles) {
				v.addf("%s.routes[%d]: route %q has no evidence token %q in core_files", label, j, route, evidence)
			}
		}
		for j, key := range item.ConfigKeys {
			exists, err := v.configPathExists(key)
			if err != nil {
				v.addf("%s.config_keys[%d]: validate config.example.yaml: %v", label, j, err)
			} else if !exists {
				v.addf("%s.config_keys[%d]: config key %q does not exist in config.example.yaml or the internal/config Go schema", label, j, key)
			}
		}
		if id != "" {
			index.features[id] = item
		}
	}

	for i, item := range registry.ReviewSeams {
		label := fmt.Sprintf("%s.review_seams[%d]", featureRegistryPath, i)
		id := v.registerID(allIDs, label, item.ID)
		if item.Support != "upstream-shared" {
			v.addf("%s.support: must be %q, got %q", label, "upstream-shared", item.Support)
		}
		if item.Owner != "shared" {
			v.addf("%s.owner: must be %q, got %q", label, "shared", item.Owner)
		}
		if item.UpstreamRelation != "upstream-equivalent" {
			v.addf("%s.upstream_relation: must be %q, got %q", label, "upstream-equivalent", item.UpstreamRelation)
		}
		v.requireString(label+".reason", item.Reason)
		v.requireStrings(label+".core_files", item.CoreFiles, true)
		v.requireStrings(label+".review_triggers", item.ReviewTriggers, true)
		v.requireStrings(label+".validation", item.Validation, true)
		for j, path := range item.CoreFiles {
			v.validatePath(fmt.Sprintf("%s.core_files[%d]", label, j), path, true)
		}
		if id != "" {
			index.reviewSeams[id] = item
		}
	}

	for i, item := range registry.ValidationProfiles {
		label := fmt.Sprintf("%s.validation_profiles[%d]", featureRegistryPath, i)
		id := v.registerID(allIDs, label, item.ID)
		v.requireString(label+".reason", item.Reason)
		v.requireStrings(label+".reference_space", item.ReferenceSpace, false)
		v.requireStrings(label+".runtime_checks", item.RuntimeChecks, false)
		v.requireStrings(label+".required_markers", item.RequiredMarkers, false)
		v.requireStrings(label+".validation", item.Validation, true)
		for j, marker := range item.RequiredMarkers {
			if strings.TrimSpace(marker) == "" {
				continue
			}
			if !v.markerInRepository(marker) {
				v.addf("%s.required_markers[%d]: marker %q has no repository evidence outside registry YAML", label, j, marker)
			}
		}
		if id != "" {
			index.validationProfiles[id] = item
		}
	}

	for i, item := range registry.Relationships {
		label := fmt.Sprintf("%s.relationships[%d]", featureRegistryPath, i)
		v.registerID(allIDs, label, item.ID)
		if item.Type != "coexist" {
			v.addf("%s.type: must be %q, got %q", label, "coexist", item.Type)
		}
		v.requireString(label+".reason", item.Reason)
		from := strings.TrimSpace(item.From)
		to := strings.TrimSpace(item.To)
		if _, ok := index.features[from]; !ok {
			v.addf("%s.from: unknown feature %q", label, item.From)
		}
		if _, ok := index.features[to]; !ok {
			v.addf("%s.to: unknown feature %q", label, item.To)
		}
		if from != "" && from == to {
			v.addf("%s: relationship endpoints must be different", label)
		}
	}

	return index
}

func (v *validator) validateProtectedDeltas(deltas protectedDeltas, index registryIndex) {
	if deltas.Version != 2 {
		v.addf("%s: version must be 2, got %d", protectedDeltasPath, deltas.Version)
	}
	v.requireLiteral(protectedDeltasPath+".maintenance_model.type", deltas.MaintenanceModel.Type, "protected-full-sync")
	v.requireLiteral(protectedDeltasPath+".maintenance_model.product_branch", deltas.MaintenanceModel.ProductBranch, "main")
	v.requireLiteral(protectedDeltasPath+".maintenance_model.operation_mode", deltas.MaintenanceModel.OperationMode, "manual-ai-assisted")
	if deltas.MaintenanceModel.ScheduledSync {
		v.addf("%s.maintenance_model.scheduled_sync: must be false", protectedDeltasPath)
	}
	v.requireString(protectedDeltasPath+".maintenance_model.cadence_guidance", deltas.MaintenanceModel.CadenceGuidance)
	v.requireLiteral(protectedDeltasPath+".maintenance_model.optional_runtime_smoke", deltas.MaintenanceModel.OptionalRuntimeSmoke, "manual_huggingface_space")
	v.requireString(protectedDeltasPath+".maintenance_model.feature_contract_registry", deltas.MaintenanceModel.FeatureContractRegistry)
	if strings.TrimSpace(deltas.MaintenanceModel.FeatureContractRegistry) != "" {
		v.validatePath(protectedDeltasPath+".maintenance_model.feature_contract_registry", deltas.MaintenanceModel.FeatureContractRegistry, false)
		if filepath.ToSlash(filepath.Clean(deltas.MaintenanceModel.FeatureContractRegistry)) != featureRegistryPath {
			v.addf("%s.maintenance_model.feature_contract_registry: must reference %q, got %q", protectedDeltasPath, featureRegistryPath, deltas.MaintenanceModel.FeatureContractRegistry)
		}
	}
	v.requireString(protectedDeltasPath+".maintenance_model.downstream_patch_ledger", deltas.MaintenanceModel.DownstreamPatchLedger)
	if strings.TrimSpace(deltas.MaintenanceModel.DownstreamPatchLedger) != "" {
		v.validatePath(protectedDeltasPath+".maintenance_model.downstream_patch_ledger", deltas.MaintenanceModel.DownstreamPatchLedger, false)
		if filepath.ToSlash(filepath.Clean(deltas.MaintenanceModel.DownstreamPatchLedger)) != downstreamPatchesPath {
			v.addf("%s.maintenance_model.downstream_patch_ledger: must reference %q, got %q", protectedDeltasPath, downstreamPatchesPath, deltas.MaintenanceModel.DownstreamPatchLedger)
		}
	}
	v.requireLiteral(protectedDeltasPath+".upstream_source.repo", deltas.UpstreamSource.Repo, "router-for-me/CLIProxyAPI")
	v.requireLiteral(protectedDeltasPath+".upstream_source.branch", deltas.UpstreamSource.Branch, "main")
	v.requireLiteral(protectedDeltasPath+".sync_rules.default_mode", deltas.SyncRules.DefaultMode, "protected_full_sync")
	v.requireLiteral(protectedDeltasPath+".sync_rules.upstream_changes", deltas.SyncRules.UpstreamChanges, "accept_by_default")
	if !deltas.SyncRules.MergeCommitRequired {
		v.addf("%s.sync_rules.merge_commit_required: must be true", protectedDeltasPath)
	}
	if !deltas.SyncRules.SquashForbidden {
		v.addf("%s.sync_rules.squash_forbidden: must be true", protectedDeltasPath)
	}
	if !deltas.SyncRules.RebaseForbidden {
		v.addf("%s.sync_rules.rebase_forbidden: must be true", protectedDeltasPath)
	}
	if !deltas.SyncRules.ContractRegistryRequired {
		v.addf("%s.sync_rules.contract_registry_required: must be true", protectedDeltasPath)
	}
	if !deltas.SyncRules.DownstreamPatchReviewRequired {
		v.addf("%s.sync_rules.downstream_patch_review_required: must be true", protectedDeltasPath)
	}
	if !deltas.SyncRules.PatchProvenanceMergeRequired {
		v.addf("%s.sync_rules.patch_provenance_merge_commit_required: must be true", protectedDeltasPath)
	}
	v.requireStrings(protectedDeltasPath+".sync_rules.forbidden_shortcuts", deltas.SyncRules.ForbiddenShortcuts, true)
	for _, required := range []string{"GitHub Sync fork", "git pull upstream main on main", "git checkout upstream/main -- ."} {
		if !containsString(deltas.SyncRules.ForbiddenShortcuts, required) {
			v.addf("%s.sync_rules.forbidden_shortcuts: missing required value %q", protectedDeltasPath, required)
		}
	}

	allIDs := make(map[string]string)
	featureRefs := make(map[string]string)
	for i, item := range deltas.RetainedCapabilities {
		label := fmt.Sprintf("%s.retained_capabilities[%d]", protectedDeltasPath, i)
		v.registerID(allIDs, label, item.ID)
		v.requireString(label+".registry_feature", item.RegistryFeature)
		featureItem, ok := index.features[strings.TrimSpace(item.RegistryFeature)]
		if !ok {
			v.addf("%s.registry_feature: unknown feature %q", label, item.RegistryFeature)
		} else if featureItem.Kind != "retained-capability" {
			v.addf("%s.registry_feature: feature %q has kind %q, want retained-capability", label, item.RegistryFeature, featureItem.Kind)
		}
		v.registerReference(featureRefs, label+".registry_feature", item.RegistryFeature)
		if !item.MustKeep {
			v.addf("%s.must_keep: retained capability must be true", label)
		}
		v.requireStrings(label+".markers", item.Markers, true)
		v.requireStrings(label+".validation", item.Validation, true)
		v.requireString(label+".conflict_policy", item.ConflictPolicy)
	}

	for i, item := range deltas.LTSOwnedFeatures {
		label := fmt.Sprintf("%s.lts_owned_features[%d]", protectedDeltasPath, i)
		v.registerID(allIDs, label, item.ID)
		v.requireString(label+".registry_feature", item.RegistryFeature)
		featureItem, ok := index.features[strings.TrimSpace(item.RegistryFeature)]
		if !ok {
			v.addf("%s.registry_feature: unknown feature %q", label, item.RegistryFeature)
		} else if featureItem.Kind != "lts-feature" {
			v.addf("%s.registry_feature: feature %q has kind %q, want lts-feature", label, item.RegistryFeature, featureItem.Kind)
		}
		v.registerReference(featureRefs, label+".registry_feature", item.RegistryFeature)
		v.requireString(label+".maintenance_policy", item.MaintenancePolicy)
		v.requireStrings(label+".markers", item.Markers, true)
		v.requireStrings(label+".validation", item.Validation, true)
		v.requireString(label+".conflict_policy", item.ConflictPolicy)
	}

	seamRefs := make(map[string]string)
	for i, item := range deltas.SharedReviewSeams {
		label := fmt.Sprintf("%s.shared_review_seams[%d]", protectedDeltasPath, i)
		v.registerID(allIDs, label, item.ID)
		v.requireString(label+".registry_seam", item.RegistrySeam)
		if _, ok := index.reviewSeams[strings.TrimSpace(item.RegistrySeam)]; !ok {
			v.addf("%s.registry_seam: unknown review seam %q", label, item.RegistrySeam)
		}
		v.registerReference(seamRefs, label+".registry_seam", item.RegistrySeam)
		if !item.ReviewRequired {
			v.addf("%s.review_required: shared review seam must be true", label)
		}
		v.requireStrings(label+".review_when", item.ReviewWhen, true)
		v.requireStrings(label+".validation", item.Validation, true)
		v.requireString(label+".conflict_policy", item.ConflictPolicy)
	}
	for id := range index.features {
		if _, ok := featureRefs[id]; !ok {
			v.addf("%s: registry feature %q is not referenced by retained_capabilities or lts_owned_features", protectedDeltasPath, id)
		}
	}
	for id := range index.reviewSeams {
		if _, ok := seamRefs[id]; !ok {
			v.addf("%s: registry review seam %q is not referenced by shared_review_seams", protectedDeltasPath, id)
		}
	}

	seenProfiles := make(map[string]int)
	for i, rawID := range deltas.RuntimeValidationProfiles {
		id := strings.TrimSpace(rawID)
		if id == "" {
			v.addf("%s.runtime_validation_profiles[%d]: must not be empty", protectedDeltasPath, i)
			continue
		}
		if first, ok := seenProfiles[id]; ok {
			v.addf("%s.runtime_validation_profiles[%d]: duplicate id %q (first at index %d)", protectedDeltasPath, i, id, first)
		} else {
			seenProfiles[id] = i
		}
		if _, ok := index.validationProfiles[id]; !ok {
			v.addf("%s.runtime_validation_profiles[%d]: unknown validation profile %q", protectedDeltasPath, i, id)
		}
	}
	for id := range index.validationProfiles {
		if _, ok := seenProfiles[id]; !ok {
			v.addf("%s: registry validation profile %q is not referenced by runtime_validation_profiles", protectedDeltasPath, id)
		}
	}
}

func (v *validator) validateDownstreamPatches(patches downstreamPatches) {
	if patches.Version != 1 {
		v.addf("%s: version must be 1, got %d", downstreamPatchesPath, patches.Version)
	}
	if len(patches.Patches) == 0 {
		v.addf("%s.patches: must contain at least one historical or active patch", downstreamPatchesPath)
	}
	seen := make(map[string]string)
	for i, item := range patches.Patches {
		label := fmt.Sprintf("%s.patches[%d]", downstreamPatchesPath, i)
		v.registerID(seen, label, item.ID)
		v.requireString(label+".reason", item.Reason)
		v.requireString(label+".introduced_in", item.IntroducedIn)
		if introducedIn := strings.TrimSpace(item.IntroducedIn); introducedIn != "" && !commitSHA.MatchString(introducedIn) {
			v.addf("%s.introduced_in: must be a 7-40 character commit SHA, got %q", label, item.IntroducedIn)
		} else if introducedIn != "" && v.repositoryHasGitHistory() && !v.repositoryIsShallow() {
			if !v.commitExists(introducedIn) {
				v.addf("%s.introduced_in: commit %q does not exist in the current repository", label, item.IntroducedIn)
			} else if !v.commitIsAncestorOfHead(introducedIn) {
				v.addf("%s.introduced_in: commit %q is not reachable from HEAD", label, item.IntroducedIn)
			}
		}
		v.requireString(label+".upstream_issue_or_pr", item.UpstreamIssueOrPR)
		v.requireEnum(label+".state", item.State, patchStates)
		v.requireStrings(label+".files", item.Files, true)
		v.requireStrings(label+".regression_tests", item.RegressionTests, true)
		v.requireString(label+".affected_upstream_range", item.AffectedUpstreamRange)
		v.requireString(label+".retire_when", item.RetireWhen)

		state := strings.TrimSpace(item.State)
		active := state == "required" || state == "upstreamed" || state == "removable"
		if active {
			for j, path := range item.Files {
				v.validatePath(fmt.Sprintf("%s.files[%d]", label, j), path, false)
			}
			for j, testName := range item.RegressionTests {
				if strings.TrimSpace(testName) == "" {
					continue
				}
				if !strings.HasPrefix(testName, "Test") {
					v.addf("%s.regression_tests[%d]: %q must start with Test", label, j, testName)
					continue
				}
				if !v.testFunctionInPatchFiles(testName, item.Files) {
					v.addf("%s.regression_tests[%d]: Go test function %q not found in patch _test.go files", label, j, testName)
				}
			}
		}
		if state == "retired" {
			v.requireString(label+".retired_in", item.RetiredIn)
			if retiredIn := strings.TrimSpace(item.RetiredIn); retiredIn != "" && !commitSHA.MatchString(retiredIn) {
				v.addf("%s.retired_in: must be a 7-40 character commit SHA, got %q", label, item.RetiredIn)
			} else if retiredIn != "" && !v.repositoryIsShallow() {
				if !v.commitExists(retiredIn) {
					v.addf("%s.retired_in: commit %q does not exist in the current repository", label, item.RetiredIn)
				} else if !v.commitIsAncestorOfHead(retiredIn) {
					v.addf("%s.retired_in: commit %q is not reachable from HEAD", label, item.RetiredIn)
				}
			}
		}
		if (state == "upstreamed" || state == "removable" || state == "retired") && strings.EqualFold(strings.TrimSpace(item.UpstreamIssueOrPR), "not-filed") {
			v.addf("%s.upstream_issue_or_pr: state %q requires concrete upstream evidence", label, state)
		}
		if active && strings.TrimSpace(item.RetiredIn) != "" {
			v.addf("%s.retired_in: active patch must not declare retired_in", label)
		}
	}
}

func (v *validator) validatePatchHistory(current downstreamPatches, baseRef string) {
	previous, exists, err := loadPatchesAtRef(v.root, baseRef)
	if err != nil {
		v.addf("%s: load patch history from %q: %v", downstreamPatchesPath, baseRef, err)
		return
	}
	if !exists {
		return
	}
	currentByID := make(map[string]downstreamPatch, len(current.Patches))
	for _, patch := range current.Patches {
		currentByID[strings.TrimSpace(patch.ID)] = patch
	}
	for _, oldPatch := range previous.Patches {
		id := strings.TrimSpace(oldPatch.ID)
		newPatch, ok := currentByID[id]
		if !ok {
			v.addf("%s: historical patch %q from %s must not be deleted", downstreamPatchesPath, id, baseRef)
			continue
		}
		if !allowedPatchTransition(strings.TrimSpace(oldPatch.State), strings.TrimSpace(newPatch.State)) {
			v.addf("%s: patch %q has invalid state transition %q -> %q", downstreamPatchesPath, id, oldPatch.State, newPatch.State)
		}
		if strings.TrimSpace(oldPatch.IntroducedIn) != strings.TrimSpace(newPatch.IntroducedIn) {
			v.addf("%s: patch %q must not rewrite introduced_in", downstreamPatchesPath, id)
		}
		if strings.TrimSpace(oldPatch.State) == "retired" && !reflect.DeepEqual(oldPatch, newPatch) {
			v.addf("%s: retired patch %q is immutable", downstreamPatchesPath, id)
		}
	}
}

func loadPatchesAtRef(root, baseRef string) (downstreamPatches, bool, error) {
	var patches downstreamPatches
	baseRef = strings.TrimSpace(baseRef)
	if baseRef == "" {
		return patches, false, nil
	}
	if err := exec.Command("git", "-C", root, "cat-file", "-e", baseRef+"^{commit}").Run(); err != nil {
		return patches, false, fmt.Errorf("git ref is not a commit: %w", err)
	}
	spec := baseRef + ":" + downstreamPatchesPath
	if err := exec.Command("git", "-C", root, "cat-file", "-e", spec).Run(); err != nil {
		return patches, false, nil
	}
	data, err := exec.Command("git", "-C", root, "show", spec).Output()
	if err != nil {
		return patches, false, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&patches); err != nil {
		return patches, false, err
	}
	return patches, true, nil
}

func allowedPatchTransition(from, to string) bool {
	if from == to {
		return true
	}
	switch from {
	case "required":
		return to == "upstreamed" || to == "removable"
	case "upstreamed":
		return to == "required" || to == "removable"
	case "removable":
		return to == "required" || to == "retired"
	default:
		return false
	}
}

func (v *validator) commitExists(sha string) bool {
	return exec.Command("git", "-C", v.root, "cat-file", "-e", strings.TrimSpace(sha)+"^{commit}").Run() == nil
}

func (v *validator) commitIsAncestorOfHead(sha string) bool {
	return exec.Command("git", "-C", v.root, "merge-base", "--is-ancestor", strings.TrimSpace(sha), "HEAD").Run() == nil
}

func (v *validator) repositoryIsShallow() bool {
	out, err := exec.Command("git", "-C", v.root, "rev-parse", "--is-shallow-repository").Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

func (v *validator) repositoryHasGitHistory() bool {
	return exec.Command("git", "-C", v.root, "rev-parse", "--verify", "HEAD^{commit}").Run() == nil
}

func (v *validator) validateFeatureClassification(label string, item feature) {
	switch item.Kind {
	case "retained-capability":
		if item.Support != "protected" {
			v.addf("%s.support: retained-capability must be protected", label)
		}
		if item.Owner != "cpa-core-lts" {
			v.addf("%s.owner: retained-capability must be owned by cpa-core-lts", label)
		}
		if item.UpstreamRelation != "divergent" && item.UpstreamRelation != "removed-upstream" {
			v.addf("%s.upstream_relation: retained-capability must be divergent or removed-upstream", label)
		}
	case "lts-feature":
		if item.Support != "maintained" && item.Support != "optional" {
			v.addf("%s.support: lts-feature must be maintained or optional", label)
		}
		if item.Owner != "cpa-core-lts" {
			v.addf("%s.owner: lts-feature must be owned by cpa-core-lts", label)
		}
		if item.UpstreamRelation != "downstream-only" && item.UpstreamRelation != "divergent" {
			v.addf("%s.upstream_relation: lts-feature must be downstream-only or divergent", label)
		}
	}
}

func (v *validator) registerReference(seen map[string]string, label, rawID string) {
	id := strings.TrimSpace(rawID)
	if id == "" {
		return
	}
	if first, ok := seen[id]; ok {
		v.addf("%s: duplicate reference to %q (first declared at %s)", label, id, first)
		return
	}
	seen[id] = label
}

func (v *validator) registerID(seen map[string]string, label, rawID string) string {
	id := strings.TrimSpace(rawID)
	if id == "" {
		v.addf("%s.id: must not be empty", label)
		return ""
	}
	if first, ok := seen[id]; ok {
		v.addf("%s.id: duplicate id %q (first declared at %s)", label, id, first)
		return id
	}
	seen[id] = label
	return id
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == want {
			return true
		}
	}
	return false
}

func routeEvidenceToken(route string) string {
	route = strings.TrimSpace(route)
	if strings.HasPrefix(route, "/v0/management/") {
		return strings.TrimPrefix(route, "/v0/management")
	}
	return route
}

func (v *validator) configPathExists(key string) (bool, error) {
	if !v.configLoaded {
		v.configLoaded = true
		data, err := os.ReadFile(filepath.Join(v.root, "config.example.yaml"))
		if err != nil {
			v.configErr = err
		} else {
			v.configErr = yaml.Unmarshal(data, &v.configExample)
		}
	}
	if v.configErr != nil {
		return false, v.configErr
	}
	segments := strings.Split(strings.TrimSpace(key), ".")
	current := any(v.configExample)
	foundInExample := true
	for _, segment := range segments {
		mapping, ok := current.(map[string]any)
		if !ok {
			foundInExample = false
			break
		}
		current, ok = mapping[segment]
		if !ok {
			foundInExample = false
			break
		}
	}
	if foundInExample {
		return true, nil
	}
	return v.configSchemaPathExists(segments)
}

func (v *validator) configSchemaPathExists(segments []string) (bool, error) {
	if !v.configSchemaLoaded {
		v.configSchemaLoaded = true
		v.configSchema = make(map[string]map[string]string)
		matches, err := filepath.Glob(filepath.Join(v.root, "internal/config/*.go"))
		if err != nil {
			v.configSchemaErr = err
		} else {
			files := token.NewFileSet()
			for _, path := range matches {
				if strings.HasSuffix(path, "_test.go") {
					continue
				}
				file, parseErr := parser.ParseFile(files, path, nil, parser.SkipObjectResolution)
				if parseErr != nil {
					v.configSchemaErr = parseErr
					break
				}
				for _, decl := range file.Decls {
					gen, ok := decl.(*ast.GenDecl)
					if !ok || gen.Tok != token.TYPE {
						continue
					}
					for _, spec := range gen.Specs {
						typeSpec, ok := spec.(*ast.TypeSpec)
						if !ok {
							continue
						}
						structType, ok := typeSpec.Type.(*ast.StructType)
						if !ok {
							continue
						}
						fields := make(map[string]string)
						for _, field := range structType.Fields.List {
							if field.Tag == nil {
								continue
							}
							rawTag, unquoteErr := strconv.Unquote(field.Tag.Value)
							if unquoteErr != nil {
								continue
							}
							yamlName := strings.Split(reflect.StructTag(rawTag).Get("yaml"), ",")[0]
							if yamlName == "" || yamlName == "-" {
								continue
							}
							fields[yamlName] = configStructTypeName(field.Type)
						}
						v.configSchema[typeSpec.Name.Name] = fields
					}
				}
			}
		}
	}
	if v.configSchemaErr != nil {
		return false, v.configSchemaErr
	}
	typeName := "Config"
	for index, segment := range segments {
		fields, ok := v.configSchema[typeName]
		if !ok {
			return false, nil
		}
		nextType, ok := fields[segment]
		if !ok {
			return false, nil
		}
		if index == len(segments)-1 {
			return true, nil
		}
		if nextType == "" {
			return false, nil
		}
		typeName = nextType
	}
	return false, nil
}

func configStructTypeName(expr ast.Expr) string {
	switch value := expr.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.StarExpr:
		return configStructTypeName(value.X)
	case *ast.ArrayType:
		return configStructTypeName(value.Elt)
	case *ast.MapType:
		return configStructTypeName(value.Value)
	default:
		return ""
	}
}

func (v *validator) requireString(label, value string) {
	if strings.TrimSpace(value) == "" {
		v.addf("%s: must not be empty", label)
	}
}

func (v *validator) requireLiteral(label, value, want string) {
	if strings.TrimSpace(value) != want {
		v.addf("%s: must be %q, got %q", label, want, value)
	}
}

func (v *validator) requireStrings(label string, values []string, requireNonEmpty bool) {
	if requireNonEmpty && len(values) == 0 {
		v.addf("%s: must contain at least one value", label)
		return
	}
	for i, value := range values {
		if strings.TrimSpace(value) == "" {
			v.addf("%s[%d]: must not be empty", label, i)
		}
	}
}

func (v *validator) requireEnum(label, value string, allowed map[string]struct{}) {
	value = strings.TrimSpace(value)
	if _, ok := allowed[value]; ok {
		return
	}
	choices := make([]string, 0, len(allowed))
	for choice := range allowed {
		choices = append(choices, choice)
	}
	sort.Strings(choices)
	v.addf("%s: invalid value %q (allowed: %s)", label, value, strings.Join(choices, ", "))
}

func (v *validator) validatePath(label, rawPath string, allowDirectory bool) {
	abs, _, err := v.resolvePath(rawPath)
	if err != nil {
		v.addf("%s: %v", label, err)
		return
	}
	info, err := os.Stat(abs)
	if err != nil {
		v.addf("%s: path %q: %v", label, rawPath, err)
		return
	}
	if !allowDirectory && !info.Mode().IsRegular() {
		v.addf("%s: path %q must be a regular file", label, rawPath)
	}
}

func (v *validator) resolvePath(rawPath string) (string, string, error) {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return "", "", fmt.Errorf("path must not be empty")
	}
	if filepath.IsAbs(rawPath) {
		return "", "", fmt.Errorf("path %q must be repository-relative", rawPath)
	}
	clean := filepath.Clean(filepath.FromSlash(rawPath))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("path %q escapes or names the repository root", rawPath)
	}
	abs := filepath.Join(v.root, clean)
	rel, err := filepath.Rel(v.root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("path %q escapes repository root", rawPath)
	}
	return abs, filepath.ToSlash(rel), nil
}

func (v *validator) markerInPaths(marker string, paths []string) bool {
	markerBytes := []byte(marker)
	for _, rawPath := range paths {
		abs, rel, err := v.resolvePath(rawPath)
		if err != nil || isRegistryEvidencePath(rel) {
			continue
		}
		info, err := os.Stat(abs)
		if err != nil {
			continue
		}
		if info.IsDir() {
			found := false
			_ = filepath.WalkDir(abs, func(path string, entry os.DirEntry, walkErr error) error {
				if walkErr != nil || found {
					return nil
				}
				relPath, _ := filepath.Rel(v.root, path)
				if entry.IsDir() && shouldSkipDirectory(filepath.ToSlash(relPath), entry.Name()) {
					return filepath.SkipDir
				}
				if entry.Type().IsRegular() && !isRegistryEvidencePath(filepath.ToSlash(relPath)) && fileContains(path, markerBytes) {
					found = true
				}
				return nil
			})
			if found {
				return true
			}
			continue
		}
		if info.Mode().IsRegular() && fileContains(abs, markerBytes) {
			return true
		}
	}
	return false
}

func (v *validator) markerInRepository(marker string) bool {
	markerBytes := []byte(marker)
	found := false
	_ = filepath.WalkDir(v.root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || found {
			return nil
		}
		rel, _ := filepath.Rel(v.root, path)
		rel = filepath.ToSlash(rel)
		if entry.IsDir() && shouldSkipDirectory(rel, entry.Name()) {
			return filepath.SkipDir
		}
		if entry.Type().IsRegular() && !isRegistryEvidencePath(rel) && fileContains(path, markerBytes) {
			found = true
		}
		return nil
	})
	return found
}

func (v *validator) testFunctionInPatchFiles(testName string, paths []string) bool {
	_, ok := v.testFunctionPath(testName, paths)
	return ok
}

func (v *validator) testFunctionPath(testName string, paths []string) (string, bool) {
	for _, rawPath := range paths {
		if !strings.HasSuffix(strings.TrimSpace(rawPath), "_test.go") {
			continue
		}
		abs, _, err := v.resolvePath(rawPath)
		if err != nil {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), abs, nil, parser.SkipObjectResolution)
		if err != nil {
			v.addf("downstream patch test file %q: parse Go AST: %v", rawPath, err)
			continue
		}
		testingImports := testingImportNames(file)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && isGoTestFunction(fn, testName, testingImports) {
				return strings.TrimSpace(rawPath), true
			}
		}
	}
	return "", false
}

func (v *validator) validateActivePatchTestsWithGo(patches downstreamPatches) {
	goMod := filepath.Join(v.root, "go.mod")
	if info, err := os.Stat(goMod); err != nil || !info.Mode().IsRegular() {
		return
	}

	expectedByDir := make(map[string]map[string]struct{})
	for _, patch := range patches.Patches {
		state := strings.TrimSpace(patch.State)
		if state != "required" && state != "upstreamed" && state != "removable" {
			continue
		}
		for _, rawName := range patch.RegressionTests {
			name := strings.TrimSpace(rawName)
			path, ok := v.testFunctionPath(name, patch.Files)
			if !ok {
				continue
			}
			dir := filepath.ToSlash(filepath.Dir(filepath.FromSlash(path)))
			if dir == "." {
				dir = ""
			}
			if expectedByDir[dir] == nil {
				expectedByDir[dir] = make(map[string]struct{})
			}
			expectedByDir[dir][name] = struct{}{}
		}
	}

	dirs := make([]string, 0, len(expectedByDir))
	for dir := range expectedByDir {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		expected := expectedByDir[dir]
		names := make([]string, 0, len(expected))
		for name := range expected {
			names = append(names, name)
		}
		sort.Strings(names)
		quoted := make([]string, 0, len(names))
		for _, name := range names {
			quoted = append(quoted, regexp.QuoteMeta(name))
		}
		pattern := "^(" + strings.Join(quoted, "|") + ")$"
		pkg := "."
		if dir != "" {
			pkg = "./" + dir
		}
		cmd := exec.Command("go", "test", "-list", pattern, pkg)
		cmd.Dir = v.root
		output, err := cmd.CombinedOutput()
		if err != nil {
			v.addf("%s: Go tool could not compile/list active patch tests in %s: %v: %s", downstreamPatchesPath, pkg, err, strings.TrimSpace(string(output)))
			continue
		}
		listed := make(map[string]struct{})
		for _, line := range strings.Split(string(output), "\n") {
			line = strings.TrimSpace(line)
			if _, ok := expected[line]; ok {
				listed[line] = struct{}{}
			}
		}
		for _, name := range names {
			if _, ok := listed[name]; !ok {
				v.addf("%s: Go tool did not list active regression test %q in %s", downstreamPatchesPath, name, pkg)
			}
		}
	}
}

func testingImportNames(file *ast.File) map[string]struct{} {
	names := make(map[string]struct{})
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || path != "testing" {
			continue
		}
		name := "testing"
		if spec.Name != nil {
			name = spec.Name.Name
		}
		if name != "_" && name != "." {
			names[name] = struct{}{}
		}
	}
	return names
}

func isGoTestFunction(fn *ast.FuncDecl, testName string, testingImports map[string]struct{}) bool {
	if fn == nil || fn.Recv != nil || fn.Name == nil || fn.Name.Name != testName || fn.Type == nil {
		return false
	}
	if fn.Type.TypeParams != nil && len(fn.Type.TypeParams.List) > 0 {
		return false
	}
	if fn.Type.Results != nil && len(fn.Type.Results.List) > 0 {
		return false
	}
	if fn.Type.Params == nil || len(fn.Type.Params.List) != 1 || len(fn.Type.Params.List[0].Names) > 1 {
		return false
	}
	star, ok := fn.Type.Params.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	selector, ok := star.X.(*ast.SelectorExpr)
	if !ok || selector.Sel == nil || selector.Sel.Name != "T" {
		return false
	}
	pkg, ok := selector.X.(*ast.Ident)
	if !ok {
		return false
	}
	_, ok = testingImports[pkg.Name]
	return ok
}

func (v *validator) addf(format string, args ...any) {
	v.problems = append(v.problems, fmt.Sprintf(format, args...))
}

func fileContains(path string, marker []byte) bool {
	data, err := os.ReadFile(path)
	return err == nil && bytes.Contains(data, marker)
}

func isRegistryEvidencePath(path string) bool {
	_, ok := registryEvidenceFiles[filepath.ToSlash(path)]
	return ok
}

func shouldSkipDirectory(rel, name string) bool {
	rel = filepath.ToSlash(rel)
	if rel == ".git" || strings.HasPrefix(rel, ".git/") || rel == "vendor" || strings.HasPrefix(rel, "vendor/") || rel == "local" || strings.HasPrefix(rel, "local/") {
		return true
	}
	return name == ".gocache" || name == ".playwright-mcp" || strings.Contains(name, "worktree") && strings.HasPrefix(name, ".")
}

func stringSet(values ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		out[value] = struct{}{}
	}
	return out
}
