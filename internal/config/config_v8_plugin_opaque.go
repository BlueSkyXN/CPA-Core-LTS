package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// Plugin-owned settings under plugins.configs are opaque to the host. The v8
// layout helpers resolve aliases and reject duplicate keys across the whole
// document, but plugin YAML may legitimately use composite keys, keys that only
// differ by tag (1 and "1"), or merge keys that rely on typed key equality.
// These helpers keep that subtree byte-for-byte equivalent to the v7 loader.

// pluginConfigsValue returns the plugins.configs value node of a root mapping.
// Aliased plugins/configs containers are not treated as opaque because their
// identity is shared with another part of the document.
func pluginConfigsValue(root *yaml.Node) (plugins *yaml.Node, configsIndex int) {
	if root == nil || root.Kind != yaml.MappingNode {
		return nil, -1
	}
	idx := findMapKeyIndex(root, "plugins")
	if idx < 0 || idx+1 >= len(root.Content) {
		return nil, -1
	}
	plugins = root.Content[idx+1]
	if plugins == nil || plugins.Kind != yaml.MappingNode {
		return nil, -1
	}
	configsIndex = findMapKeyIndex(plugins, "configs")
	if configsIndex < 0 || configsIndex+1 >= len(plugins.Content) {
		return nil, -1
	}
	if configs := plugins.Content[configsIndex+1]; configs == nil || configs.Kind == yaml.AliasNode {
		return nil, -1
	}
	return plugins, configsIndex
}

// withoutPluginConfigs returns a shallow copy of root whose plugins.configs
// value is an empty mapping. The original nodes are not modified.
func withoutPluginConfigs(root *yaml.Node) *yaml.Node {
	plugins, configsIndex := pluginConfigsValue(root)
	if plugins == nil {
		return root
	}
	pluginsCopy := *plugins
	pluginsCopy.Content = append([]*yaml.Node(nil), plugins.Content...)
	pluginsCopy.Content[configsIndex+1] = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	rootCopy := *root
	rootCopy.Content = append([]*yaml.Node(nil), root.Content...)
	rootCopy.Content[findMapKeyIndex(root, "plugins")+1] = &pluginsCopy
	return &rootCopy
}

// decodeConfigShape rejects malformed host configuration (for example duplicate
// keys) without imposing host map semantics on plugin-owned YAML.
func decodeConfigShape(root *yaml.Node) error {
	var shape map[string]any
	if err := withoutPluginConfigs(root).Decode(&shape); err != nil {
		return err
	}
	if plugins, configsIndex := pluginConfigsValue(root); plugins != nil && hasAliasCycle(plugins.Content[configsIndex+1]) {
		return fmt.Errorf("plugins.configs contains a recursive alias")
	}
	return nil
}

// hasAliasCycle reports whether following aliases from node reaches one of its
// own ancestors, which yaml.v3 would otherwise reject during a full decode.
func hasAliasCycle(node *yaml.Node) bool {
	onStack := map[*yaml.Node]bool{}
	done := map[*yaml.Node]bool{}
	var visit func(*yaml.Node) bool
	visit = func(n *yaml.Node) bool {
		if n == nil || done[n] {
			return false
		}
		if onStack[n] {
			return true
		}
		onStack[n] = true
		if n.Kind == yaml.AliasNode && visit(n.Alias) {
			return true
		}
		for _, child := range n.Content {
			if visit(child) {
				return true
			}
		}
		onStack[n] = false
		done[n] = true
		return false
	}
	return visit(node)
}

// expandConfigAliases resolves aliases and merge keys for host-owned settings.
// plugins.configs keeps its original nodes unless it references an anchor that
// expansion would remove, in which case only that subtree is expanded.
func expandConfigAliases(root *yaml.Node) *yaml.Node {
	plugins, configsIndex := pluginConfigsValue(root)
	if plugins == nil {
		return expandYAMLAliases(root)
	}
	configs := plugins.Content[configsIndex+1]
	expanded := expandYAMLAliases(withoutPluginConfigs(root))
	expandedPlugins, expandedIndex := pluginConfigsValue(expanded)
	if expandedPlugins == nil {
		return expanded
	}
	if referencesExternalAnchor(configs) && !hasAliasCycle(configs) {
		expandedPlugins.Content[expandedIndex+1] = expandYAMLAliases(configs)
	} else {
		expandedPlugins.Content[expandedIndex+1] = configs
	}
	return expanded
}

// referencesExternalAnchor reports whether an alias inside node targets a node
// outside node. Such aliases would dangle once the surrounding document drops
// its anchors during expansion.
func referencesExternalAnchor(node *yaml.Node) bool {
	inside := map[*yaml.Node]bool{}
	var collect func(*yaml.Node)
	collect = func(n *yaml.Node) {
		if n == nil || inside[n] {
			return
		}
		inside[n] = true
		for _, child := range n.Content {
			collect(child)
		}
	}
	collect(node)
	external := false
	visited := map[*yaml.Node]bool{}
	var walk func(*yaml.Node)
	walk = func(n *yaml.Node) {
		if n == nil || external || visited[n] {
			return
		}
		visited[n] = true
		if n.Kind == yaml.AliasNode && n.Alias != nil && !inside[n.Alias] {
			external = true
			return
		}
		for _, child := range n.Content {
			walk(child)
		}
	}
	walk(node)
	return external
}

// findMergeKeyIndex locates a mapping key using YAML key equality: scalar keys
// match only when both the resolved tag and the value match.
func findMergeKeyIndex(mapNode *yaml.Node, key *yaml.Node) int {
	if mapNode == nil || mapNode.Kind != yaml.MappingNode || key == nil {
		return -1
	}
	for i := 0; i+1 < len(mapNode.Content); i += 2 {
		existing := mapNode.Content[i]
		if existing == nil {
			continue
		}
		if existing == key {
			return i
		}
		if existing.Kind == yaml.ScalarNode && key.Kind == yaml.ScalarNode &&
			existing.Value == key.Value && existing.ShortTag() == key.ShortTag() {
			return i
		}
	}
	return -1
}
