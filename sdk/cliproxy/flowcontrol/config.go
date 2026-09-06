// Package flowcontrol provides process-local, conjunctive concurrency and rolling
// request-window limits. It does not know tokens, HTTP authentication or routing.
package flowcontrol

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	Request           = "request"
	Attempt           = "attempt"
	maxWindowMS int64 = 366 * 24 * 60 * 60 * 1000
)

// Window permits Requests admissions in any trailing PeriodMS milliseconds.
// Multiple windows apply together; this is not an average-rate token bucket.
type Window struct {
	Requests int   `yaml:"requests" json:"requests"`
	PeriodMS int64 `yaml:"period-ms" json:"period-ms"`
}

type QueueConfig struct {
	MaxWaiting       int   `yaml:"max-waiting" json:"max-waiting"`
	MaxWaitingPerKey int   `yaml:"max-waiting-per-key" json:"max-waiting-per-key"`
	MaxBytes         int64 `yaml:"max-bytes" json:"max-bytes"`
	MaxWaitMS        int64 `yaml:"max-wait-ms" json:"max-wait-ms"`
}

// Scope defines the independently counted bucket, Match fields select traffic.
// Thus scope=key, key="*" creates one bucket PER key, not one shared key bucket.
// Every matching rule applies; a specific rule never silently overrides another.
type Rule struct {
	ID             string   `yaml:"id" json:"id"`
	Label          string   `yaml:"label,omitempty" json:"label,omitempty"`
	Stage          string   `yaml:"stage" json:"stage"`
	Scope          string   `yaml:"scope" json:"scope"`
	GroupBy        []string `yaml:"group-by,omitempty" json:"group-by,omitempty"`
	Keys           []string `yaml:"keys,omitempty" json:"keys,omitempty"`
	Models         []string `yaml:"models,omitempty" json:"models,omitempty"`
	Accounts       []string `yaml:"accounts,omitempty" json:"accounts,omitempty"`
	qualifiedModel bool
	Key            string   `yaml:"key,omitempty" json:"key,omitempty"`
	Model          string   `yaml:"model,omitempty" json:"model,omitempty"`
	Provider       string   `yaml:"provider,omitempty" json:"provider,omitempty"`
	Account        string   `yaml:"account,omitempty" json:"account,omitempty"`
	Credential     string   `yaml:"credential,omitempty" json:"credential,omitempty"`
	AuthKind       string   `yaml:"auth-kind,omitempty" json:"auth-kind,omitempty"`
	MaxConcurrent  int      `yaml:"max-concurrent" json:"max-concurrent"`
	Windows        []Window `yaml:"windows,omitempty" json:"windows,omitempty"`
}

type Config struct {
	// Version 3 explicitly opts into single-auth identity and joint first admission.
	// Older policies retain their behavior until explicitly migrated.
	Version     int               `yaml:"version,omitempty" json:"version,omitempty"`
	Observation ObservationConfig `yaml:"observation" json:"observation"`
	Enabled     bool              `yaml:"enabled" json:"enabled"`
	Queue       QueueConfig       `yaml:"queue" json:"queue"`
	MaxBuckets  int               `yaml:"max-buckets,omitempty" json:"max-buckets,omitempty"`
	MaxHistory  int               `yaml:"max-history,omitempty" json:"max-history,omitempty"`
	Rules       []Rule            `yaml:"rules,omitempty" json:"rules,omitempty"`
}

var ruleID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
var opaqueRef = regexp.MustCompile(`^(anonymous|[a-f0-9]{64}|\*)$`)

func (c Config) Effective() Config {
	c.Rules = append([]Rule(nil), c.Rules...)
	for i := range c.Rules {
		c.Rules[i].Windows = append([]Window(nil), c.Rules[i].Windows...)
		c.Rules[i].GroupBy = cloneStrings(c.Rules[i].GroupBy)
		c.Rules[i].Keys = normalizeSelection(c.Rules[i].Keys, false)
		c.Rules[i].Accounts = normalizeSelection(c.Rules[i].Accounts, false)
		c.Rules[i].Models = normalizeSelection(c.Rules[i].Models, true)
		c.Rules[i].qualifiedModel = c.Version >= 3
	}
	c.Observation = c.Observation.Effective()
	if c.MaxBuckets == 0 {
		c.MaxBuckets = 10000
	}
	if c.MaxHistory == 0 {
		c.MaxHistory = 200000
	}
	if c.Queue.MaxWaiting > 0 {
		if c.Queue.MaxWaitingPerKey == 0 {
			c.Queue.MaxWaitingPerKey = c.Queue.MaxWaiting
		}
		if c.Queue.MaxBytes == 0 {
			c.Queue.MaxBytes = 64 << 20
		}
	}
	return c
}

func (c Config) Validate() error {
	if c.Version < 0 || c.Version > 3 {
		return fmt.Errorf("flow-control: unsupported version")
	}
	if err := c.Observation.Effective().Validate(); err != nil {
		return err
	}

	// An operator can always disable a policy without first repairing its draft.
	if !c.Enabled {
		return nil
	}
	c = c.Effective()
	if len(c.Rules) > 128 {
		return fmt.Errorf("flow-control: at most 128 rules")
	}
	if c.MaxBuckets < 1 || c.MaxBuckets > 100000 || c.MaxHistory < 1 || c.MaxHistory > 2000000 {
		return fmt.Errorf("flow-control: max-buckets must be 1..100000 and max-history 1..2000000")
	}
	q := c.Queue
	if q.MaxWaiting < 0 || q.MaxWaiting > 10000 || q.MaxWaitingPerKey < 0 || q.MaxWaitingPerKey > q.MaxWaiting || q.MaxBytes < 0 || q.MaxBytes > (4<<30) || q.MaxWaitMS < 0 || q.MaxWaitMS > 300000 {
		return fmt.Errorf("flow-control: invalid bounded queue configuration")
	}
	if q.MaxWaiting > 0 && ((c.Version < 3 && q.MaxWaitMS == 0) || q.MaxBytes == 0) {
		return fmt.Errorf("flow-control: a queue needs positive max-wait-ms and max-bytes")
	}
	seen := map[string]bool{}
	for i, r := range c.Rules {
		if !ruleID.MatchString(r.ID) || seen[r.ID] {
			return fmt.Errorf("flow-control: rule %d needs a unique, short id", i+1)
		}
		seen[r.ID] = true
		if r.Stage != Request && r.Stage != Attempt {
			return fmt.Errorf("flow-control: rule %d stage must be request or attempt", i+1)
		}
		if c.Version < 3 && r.Scope == "custom" && len(r.GroupBy) == 0 {
			return fmt.Errorf("flow-control: empty custom group-by requires version: 3")
		}
		dims, dimsErr := r.Dimensions()
		if dimsErr != nil {
			return fmt.Errorf("flow-control: rule %d: %w", i+1, dimsErr)
		}
		if len(r.Label) > 256 || strings.ContainsAny(r.Label, "\r\n\x00") {
			return fmt.Errorf("flow-control: rule %d has an invalid label", i+1)
		}
		if r.Stage == Request {
			for _, dim := range dims {
				if dim != "key" && dim != "model" {
					return fmt.Errorf("flow-control: rule %d dimension needs attempt stage", i+1)
				}
			}
		}
		if r.Stage == Request && (r.Provider != "" || r.Account != "" || r.Accounts != nil || r.Credential != "" || r.AuthKind != "") {
			return fmt.Errorf("flow-control: request-stage rule %d cannot select an upstream provider/account", i+1)
		}
		if (r.Key != "" && !opaqueRef.MatchString(r.Key)) || (r.Account != "" && !opaqueRef.MatchString(r.Account)) || (r.Credential != "" && !opaqueRef.MatchString(r.Credential)) {
			return fmt.Errorf("flow-control: rule %d key/account/credential must be an opaque reference from the status API or *", i+1)
		}
		if c.Version < 3 && (r.Models != nil || r.Keys != nil || r.Accounts != nil) {
			return fmt.Errorf("flow-control: rule %d collections require version: 3", i+1)
		}
		if c.Version >= 3 {
			if r.Credential != "" || strings.Contains(r.Scope, "credential") {
				return fmt.Errorf("flow-control: rule %d: migrate credential to account", i+1)
			}
			for _, dim := range dims {
				if dim == "credential" {
					return fmt.Errorf("flow-control: rule %d: use the single account dimension", i+1)
				}
			}
		}
		for _, item := range []struct {
			old    string
			values []string
			model  bool
		}{{r.Model, r.Models, true}, {r.Key, r.Keys, false}, {r.Account, r.Accounts, false}} {
			if err := validateSelection(item.old, item.values, item.model); err != nil {
				return fmt.Errorf("flow-control: rule %d: %w", i+1, err)
			}
		}
		if r.Stage == Request {
			for _, model := range append(cloneStrings(r.Models), r.Model) {
				if strings.Contains(model, "::") {
					return fmt.Errorf("flow-control: request-stage model cannot name an upstream provider")
				}
			}
		}
		if len(r.Model) > 256 || strings.Count(r.Model, "*") > 1 || (strings.Contains(r.Model, "*") && !strings.HasSuffix(r.Model, "*")) || strings.ContainsAny(r.Model, "\r\n\x00") {
			return fmt.Errorf("flow-control: rule %d model accepts exact text or one trailing *", i+1)
		}
		if len(r.Provider) > 64 || strings.ContainsAny(r.Provider, "\r\n\x00") {
			return fmt.Errorf("flow-control: invalid provider on rule %d", i+1)
		}
		if r.AuthKind != "" && r.AuthKind != "*" && r.AuthKind != "oauth" && r.AuthKind != "apikey" {
			return fmt.Errorf("flow-control: rule %d auth-kind must be oauth or apikey", i+1)
		}
		if r.MaxConcurrent < 0 || r.MaxConcurrent > 100000 || len(r.Windows) > 8 || (r.MaxConcurrent == 0 && len(r.Windows) == 0) {
			return fmt.Errorf("flow-control: rule %d needs concurrency and/or up to 8 request windows", i+1)
		}
		periods := map[int64]bool{}
		for _, w := range r.Windows {
			if w.Requests < 1 || w.Requests > c.MaxHistory || w.PeriodMS < 1 || w.PeriodMS > maxWindowMS || periods[w.PeriodMS] {
				return fmt.Errorf("flow-control: invalid or duplicate window on rule %d", i+1)
			}
			periods[w.PeriodMS] = true
		}
	}
	return nil
}

// Identity keeps caller credentials separate from upstream credentials. RequestID
// is observational only: it is never a limiter grouping key or a secret token.
type Identity struct {
	Stage      string `json:"stage"`
	Key        string `json:"key,omitempty"`
	Model      string `json:"model,omitempty"`
	Provider   string `json:"provider,omitempty"`
	Account    string `json:"account,omitempty"`
	Credential string `json:"credential,omitempty"`
	AuthKind   string `json:"auth-kind,omitempty"`
	RequestID  string `json:"request-id,omitempty"`
}

// Dimensions preserves every v1 scope and permits explicit cross-products without
// inventing a second matcher. Custom dimension order does not change grouping.
func (r Rule) Dimensions() ([]string, error) {
	var dims []string
	switch r.Scope {
	case "global":
		dims = []string{}
	case "key":
		dims = []string{"key"}
	case "model":
		dims = []string{"model"}
	case "key-model":
		dims = []string{"key", "model"}
	case "provider":
		dims = []string{"provider"}
	case "account":
		dims = []string{"account"}
	case "account-model":
		dims = []string{"account", "model"}
	case "credential":
		dims = []string{"credential"}
	case "credential-model":
		dims = []string{"credential", "model"}
	case "key-account":
		dims = []string{"key", "account"}
	case "key-credential":
		dims = []string{"key", "credential"}
	case "key-account-model":
		dims = []string{"key", "account", "model"}
	case "key-credential-model":
		dims = []string{"key", "credential", "model"}
	}
	if dims != nil {
		if len(r.GroupBy) > 0 {
			return nil, fmt.Errorf("group-by requires scope=custom")
		}
		return dims, nil
	}
	if r.Scope != "custom" || len(r.GroupBy) > 6 {
		return nil, fmt.Errorf("unknown scope or invalid custom group-by")
	}
	dims = append([]string(nil), r.GroupBy...)
	seen := map[string]bool{}
	for _, d := range dims {
		if seen[d] {
			return nil, fmt.Errorf("duplicate dimension %s", d)
		}
		seen[d] = true
		switch d {
		case "key", "model", "provider", "account", "credential", "auth-kind":
		default:
			return nil, fmt.Errorf("unknown dimension %s", d)
		}
	}
	sort.Strings(dims)
	return dims, nil
}
func (d Identity) dimension(name string) string {
	switch name {
	case "key":
		return d.Key
	case "model":
		return d.Model
	case "provider":
		return d.Provider
	case "account":
		return d.Account
	case "credential":
		return d.Credential
	case "auth-kind":
		return d.AuthKind
	}
	return ""
}

func (d Identity) normalized() Identity {
	if d.Key == "" {
		d.Key = "anonymous"
	}
	d.Model = strings.ToLower(strings.TrimSpace(d.Model))
	d.Provider = strings.ToLower(strings.TrimSpace(d.Provider))
	d.AuthKind = strings.ToLower(strings.TrimSpace(d.AuthKind))
	return d
}

func (r Rule) matches(d Identity) bool {
	if r.Stage != d.Stage || !selectionMatches(r.Key, r.Keys, d.Key, "", false) || !selectionMatches(r.Account, r.Accounts, d.Account, "", false) || !exact(r.Credential, d.Credential) || !exact(r.AuthKind, d.AuthKind) || !exact(strings.ToLower(strings.TrimSpace(r.Provider)), d.Provider) {
		return false
	}
	return selectionMatches(r.Model, r.Models, d.Model, d.Provider, true)
}

// Collections are OR within a field, AND across fields. A rule returns only one
// boolean even when both an exact name and a prefix match the same request.
func selectionMatches(old string, values []string, value, provider string, model bool) bool {
	if values == nil {
		return matchOne(old, value, provider, model)
	}
	for _, p := range values {
		if matchOne(p, value, provider, model) {
			return true
		}
	}
	return false
}
func matchOne(pattern, value, provider string, model bool) bool {
	if model {
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		value = strings.ToLower(strings.TrimSpace(value))
	}
	if pattern == "" || pattern == "*" {
		return true
	}
	if model {
		if p, name, ok := strings.Cut(pattern, "::"); ok {
			if !strings.EqualFold(p, provider) {
				return false
			}
			pattern = name
		}
		if strings.HasSuffix(pattern, "*") {
			return strings.HasPrefix(value, strings.TrimSuffix(pattern, "*"))
		}
	}
	return pattern == value
}
func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append(make([]string, 0, len(values)), values...)
}
func normalizeSelection(values []string, lower bool) []string {
	if values == nil {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, v := range values {
		v = strings.TrimSpace(v)
		if lower {
			v = strings.ToLower(v)
		}
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}
func validateSelection(old string, values []string, model bool) error {
	if values == nil {
		return nil
	}
	if len(values) == 0 || len(values) > 256 {
		return fmt.Errorf("a selection needs 1..256 entries; omit the field for all")
	}
	if old != "" && (len(values) != 1 || !strings.EqualFold(strings.TrimSpace(old), values[0])) {
		return fmt.Errorf("conflicting scalar and collection filters")
	}
	for _, v := range values {
		if model {
			if v == "" || v == "*" || len(v) > 512 || strings.ContainsAny(v, "\r\n\x00") || strings.Contains(strings.TrimSuffix(v, "*"), "*") || strings.HasSuffix(v, "::") {
				return fmt.Errorf("model selection needs exact names or explicit trailing prefixes")
			}
			if strings.Count(v, "::") > 1 || strings.HasPrefix(v, "::") {
				return fmt.Errorf("invalid qualified model")
			}
		} else if v == "*" || !opaqueRef.MatchString(v) {
			return fmt.Errorf("selection needs opaque references; omit for all")
		}
	}
	return nil
}

func exact(pattern, value string) bool { return pattern == "" || pattern == "*" || pattern == value }
func (r Rule) retention() time.Duration {
	var ms int64
	for _, w := range r.Windows {
		if w.PeriodMS > ms {
			ms = w.PeriodMS
		}
	}
	return time.Duration(ms) * time.Millisecond
}
func (r Rule) bucketID(d Identity) string {
	// Length prefixes prevent ambiguous key/model tuples and delimiter collisions.
	fields := []string{r.ID, r.Stage, r.Scope}
	dims, _ := r.Dimensions()
	if r.qualifiedModel {
		// v3 aliases of the same projection share stable counters. A UI change
		// from key-model to custom[key,model] must not reset frequency history.
		fields = []string{"v3", r.ID, r.Stage}
		dims = append([]string(nil), dims...)
		sort.Strings(dims)
	}
	for _, dim := range dims {
		if r.Scope == "custom" || r.qualifiedModel {
			fields = append(fields, dim)
		}
		fields = append(fields, d.dimension(dim))
		if r.qualifiedModel && dim == "model" && d.Stage == Attempt {
			// Same spelling at different providers is not automatically one model.
			fields = append(fields, d.Provider)
		}
	}
	var b strings.Builder
	for _, v := range fields {
		fmt.Fprintf(&b, "%d:%s", len(v), v)
	}
	return b.String()
}
