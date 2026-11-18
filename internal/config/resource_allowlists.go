package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

// ResourceAllowlists reserves individual resources for authenticated client key references.
type ResourceAllowlists struct {
	AuthFiles       map[string][]string `yaml:"auth-files,omitempty" json:"auth-files,omitempty"`
	UpstreamAPIKeys map[string][]string `yaml:"upstream-api-keys,omitempty" json:"upstream-api-keys,omitempty"`
	Models          map[string][]string `yaml:"models,omitempty" json:"models,omitempty"`

	// Only compiled runtime snapshots contain this index. A nil value denotes
	// a shared key. A non-nil empty slice denies every caller.
	upstreamKeyRefs map[[sha256.Size]byte][]string
}

func (p *ResourceAllowlists) Enabled() bool {
	return p != nil && (len(p.AuthFiles) != 0 || len(p.UpstreamAPIKeys) != 0 || len(p.Models) != 0)
}

var resourceCredentialIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

func (p *ResourceAllowlists) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("access.resource-allowlists must be a mapping")
	}
	var decoded ResourceAllowlists
	for i := 0; i < len(node.Content); i += 2 {
		field, value := node.Content[i].Value, node.Content[i+1]
		switch field {
		case "auth-files", "upstream-api-keys", "models":
		default:
			return fmt.Errorf("access.resource-allowlists accepts auth-files, upstream-api-keys and models")
		}
		if value.Kind != yaml.MappingNode {
			return fmt.Errorf("access.resource-allowlists.%s must be a mapping", field)
		}
		rules := make(map[string][]string, len(value.Content)/2)
		for j := 0; j < len(value.Content); j += 2 {
			name, list := value.Content[j], value.Content[j+1]
			if name.Tag != "!!str" || list.Kind != yaml.SequenceNode {
				return fmt.Errorf("access.resource-allowlists.%s requires a resource name and a list of key references", field)
			}
			refs := make([]string, 0, len(list.Content))
			for _, ref := range list.Content {
				if ref.Tag != "!!str" {
					return fmt.Errorf("access.resource-allowlists.%s key references must be strings", field)
				}
				refs = append(refs, ref.Value)
			}
			if _, exists := rules[name.Value]; exists {
				return fmt.Errorf("access.resource-allowlists.%s contains a duplicate resource name", field)
			}
			rules[name.Value] = refs
		}
		switch field {
		case "auth-files":
			decoded.AuthFiles = rules
		case "upstream-api-keys":
			decoded.UpstreamAPIKeys = rules
		case "models":
			decoded.Models = rules
		}
	}
	if _, err := decoded.Normalize(); err != nil {
		return err
	}
	*p = decoded
	return nil
}

// Normalize validates and copies rules, folding names and key references for lookup.
func (p *ResourceAllowlists) Normalize() (*ResourceAllowlists, error) {
	if !p.Enabled() {
		return nil, nil
	}
	files, err := normalizeResourceRules("auth-files", p.AuthFiles)
	if err != nil {
		return nil, err
	}
	models, err := normalizeResourceRules("models", p.Models)
	if err != nil {
		return nil, err
	}
	apiKeys, err := normalizeResourceRules("upstream-api-keys", p.UpstreamAPIKeys)
	if err != nil {
		return nil, err
	}
	return &ResourceAllowlists{AuthFiles: files, UpstreamAPIKeys: apiKeys, Models: models}, nil
}

func normalizeResourceRules(field string, rules map[string][]string) (map[string][]string, error) {
	if len(rules) == 0 {
		return nil, nil
	}
	out := make(map[string][]string, len(rules))
	for name, refs := range rules {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" || strings.ContainsAny(name, "*?") || strings.IndexFunc(name, unicode.IsControl) >= 0 {
			return nil, fmt.Errorf("access.resource-allowlists.%s requires an exact resource name", field)
		}
		if field == "auth-files" && (strings.ContainsAny(name, "/\\") || !strings.HasSuffix(name, ".json") || name == ".json") {
			return nil, fmt.Errorf("access.resource-allowlists.auth-files requires a .json file name without a directory")
		}
		if field == "upstream-api-keys" && !resourceCredentialIDPattern.MatchString(name) {
			return nil, fmt.Errorf("access.resource-allowlists.upstream-api-keys requires a valid credential-id")
		}
		if _, exists := out[name]; exists {
			return nil, fmt.Errorf("access.resource-allowlists.%s contains duplicate resource names after case folding", field)
		}
		if refs == nil {
			return nil, fmt.Errorf("access.resource-allowlists.%s requires a list of key references", field)
		}
		normalized := make([]string, len(refs))
		for i, ref := range refs {
			ref = strings.ToLower(strings.TrimSpace(ref))
			if len(ref) != 64 {
				return nil, fmt.Errorf("access.resource-allowlists.%s key references must be 64 hexadecimal characters", field)
			}
			if _, err := hex.DecodeString(ref); err != nil {
				return nil, fmt.Errorf("access.resource-allowlists.%s key references must be 64 hexadecimal characters", field)
			}
			normalized[i] = ref
		}
		out[name] = normalized
	}
	return out, nil
}

func (c *Config) ValidateResourceAllowlists() error {
	_, err := c.CompileResourceAllowlists(false)
	return err
}
