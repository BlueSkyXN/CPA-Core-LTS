package main

type featureRegistry struct {
	Version            int                 `yaml:"version"`
	GeneratedFrom      string              `yaml:"generated_from"`
	MaintenanceModel   string              `yaml:"maintenance_model"`
	GuardPolicy        guardPolicy         `yaml:"guard_policy"`
	Features           []feature           `yaml:"features"`
	ReviewSeams        []reviewSeam        `yaml:"review_seams"`
	ValidationProfiles []validationProfile `yaml:"validation_profiles"`
	Relationships      []relationship      `yaml:"relationships"`
}

type guardPolicy struct {
	Purpose         string   `yaml:"purpose"`
	AddMarkerWhen   []string `yaml:"add_marker_when"`
	AvoidMarkerWhen []string `yaml:"avoid_marker_when"`
	BehaviorProof   []string `yaml:"behavior_proof"`
}

type feature struct {
	ID               string   `yaml:"id"`
	Kind             string   `yaml:"kind"`
	Support          string   `yaml:"support"`
	Owner            string   `yaml:"owner"`
	UpstreamRelation string   `yaml:"upstream_relation"`
	Reason           string   `yaml:"reason"`
	Routes           []string `yaml:"routes"`
	ConfigKeys       []string `yaml:"config_keys"`
	CoreFiles        []string `yaml:"core_files"`
	RequiredMarkers  []string `yaml:"required_markers"`
	Validation       []string `yaml:"validation"`
}

type reviewSeam struct {
	ID               string   `yaml:"id"`
	Support          string   `yaml:"support"`
	Owner            string   `yaml:"owner"`
	UpstreamRelation string   `yaml:"upstream_relation"`
	Reason           string   `yaml:"reason"`
	CoreFiles        []string `yaml:"core_files"`
	ReviewTriggers   []string `yaml:"review_triggers"`
	Validation       []string `yaml:"validation"`
}

type validationProfile struct {
	ID              string   `yaml:"id"`
	Reason          string   `yaml:"reason"`
	ReferenceSpace  []string `yaml:"reference_space"`
	RuntimeChecks   []string `yaml:"runtime_checks"`
	RequiredMarkers []string `yaml:"required_markers"`
	Validation      []string `yaml:"validation"`
}

type relationship struct {
	ID     string `yaml:"id"`
	Type   string `yaml:"type"`
	From   string `yaml:"from"`
	To     string `yaml:"to"`
	Reason string `yaml:"reason"`
}

type protectedDeltas struct {
	Version                   int                  `yaml:"version"`
	MaintenanceModel          maintenanceModel     `yaml:"maintenance_model"`
	UpstreamSource            upstreamSource       `yaml:"upstream_source"`
	RetainedCapabilities      []retainedCapability `yaml:"retained_capabilities"`
	LTSOwnedFeatures          []ltsOwnedFeature    `yaml:"lts_owned_features"`
	SharedReviewSeams         []sharedReviewSeam   `yaml:"shared_review_seams"`
	SyncRules                 syncRules            `yaml:"sync_rules"`
	RuntimeValidationProfiles []string             `yaml:"runtime_validation_profiles"`
}

type maintenanceModel struct {
	Type                    string `yaml:"type"`
	ProductBranch           string `yaml:"product_branch"`
	OperationMode           string `yaml:"operation_mode"`
	ScheduledSync           bool   `yaml:"scheduled_sync"`
	CadenceGuidance         string `yaml:"cadence_guidance"`
	OptionalRuntimeSmoke    string `yaml:"optional_runtime_smoke"`
	FeatureContractRegistry string `yaml:"feature_contract_registry"`
	DownstreamPatchLedger   string `yaml:"downstream_patch_ledger"`
}

type upstreamSource struct {
	Repo   string `yaml:"repo"`
	Branch string `yaml:"branch"`
}

type retainedCapability struct {
	ID              string   `yaml:"id"`
	RegistryFeature string   `yaml:"registry_feature"`
	MustKeep        bool     `yaml:"must_keep"`
	Markers         []string `yaml:"markers"`
	Validation      []string `yaml:"validation"`
	ConflictPolicy  string   `yaml:"conflict_policy"`
}

type ltsOwnedFeature struct {
	ID                string   `yaml:"id"`
	RegistryFeature   string   `yaml:"registry_feature"`
	MaintenancePolicy string   `yaml:"maintenance_policy"`
	Markers           []string `yaml:"markers"`
	Validation        []string `yaml:"validation"`
	ConflictPolicy    string   `yaml:"conflict_policy"`
}

type sharedReviewSeam struct {
	ID             string   `yaml:"id"`
	RegistrySeam   string   `yaml:"registry_seam"`
	ReviewRequired bool     `yaml:"review_required"`
	ReviewWhen     []string `yaml:"review_when"`
	Validation     []string `yaml:"validation"`
	ConflictPolicy string   `yaml:"conflict_policy"`
}

type syncRules struct {
	DefaultMode                   string   `yaml:"default_mode"`
	UpstreamChanges               string   `yaml:"upstream_changes"`
	MergeCommitRequired           bool     `yaml:"merge_commit_required"`
	SquashForbidden               bool     `yaml:"squash_forbidden"`
	RebaseForbidden               bool     `yaml:"rebase_forbidden"`
	ContractRegistryRequired      bool     `yaml:"contract_registry_required"`
	DownstreamPatchReviewRequired bool     `yaml:"downstream_patch_review_required"`
	PatchProvenanceMergeRequired  bool     `yaml:"patch_provenance_merge_commit_required"`
	ForbiddenShortcuts            []string `yaml:"forbidden_shortcuts"`
}

type downstreamPatches struct {
	Version int               `yaml:"version"`
	Patches []downstreamPatch `yaml:"patches"`
}

type downstreamPatch struct {
	ID                    string   `yaml:"id"`
	Reason                string   `yaml:"reason"`
	IntroducedIn          string   `yaml:"introduced_in"`
	AffectedUpstreamRange string   `yaml:"affected_upstream_range"`
	Files                 []string `yaml:"files"`
	RegressionTests       []string `yaml:"regression_tests"`
	UpstreamIssueOrPR     string   `yaml:"upstream_issue_or_pr"`
	State                 string   `yaml:"state"`
	ConflictPolicy        string   `yaml:"conflict_policy,omitempty"`
	RetireWhen            string   `yaml:"retire_when"`
	RetiredIn             string   `yaml:"retired_in,omitempty"`
}
