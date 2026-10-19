package config

import (
	"bytes"
	"encoding/json"
	"fmt"

	"go.yaml.in/yaml/v3"
)

// MigrateDeviceConfig converts the network section to independently enabled version 2 paths.
// It preserves application policy, comments and the existing choice of network providers.
// Device trust is established separately by pairing the new public identities.
func MigrateDeviceConfig(raw []byte) ([]byte, error) {
	cfg, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	if cfg.SchemaVersion == 2 {
		return bytes.Clone(raw), nil
	}
	var document yaml.Node
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return nil, err
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("节点配置需要 YAML 对象")
	}
	root := document.Content[0]
	mesh := mappingValue(root, "mesh")
	if mesh == nil || mesh.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("缺少 mesh 配置")
	}
	setMapping(root, "schema_version", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: "2"})
	lan := mappingValue(mesh, "lan")
	if lan == nil {
		lan = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		setMapping(mesh, "lan", lan)
	}
	setMapping(lan, "enabled", boolNode(cfg.Mesh.Mode == "lan"))
	if mappingValue(lan, "mdns") == nil {
		setMapping(lan, "mdns", boolNode(true))
	}
	tailnet := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	setMapping(tailnet, "enabled", boolNode(cfg.Mesh.Mode == "tailnet"))
	for _, key := range []string{"control_url", "authkey_file", "auth_method", "ephemeral"} {
		if node := mappingValue(mesh, key); node != nil {
			setMapping(tailnet, key, node)
		}
	}
	setMapping(mesh, "tailnet", tailnet)
	for _, key := range []string{"mode", "control_url", "authkey_file", "auth_method", "ephemeral"} {
		deleteMapping(mesh, key)
	}
	// The v2 local device uses tag:peer. Preserve the previous Tailnet local
	// grant when the file only declared tag:device, retaining its exact limits.
	if cfg.Mesh.Mode == "tailnet" {
		authz := mappingValue(mappingValue(root, "agent"), "authz")
		if authz != nil && mappingValue(authz, "tag:peer") == nil {
			if grant := mappingValue(authz, "tag:device"); grant != nil {
				setMapping(authz, "tag:peer", grant)
			}
		}
	}
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	result := output.Bytes()
	if json.Valid(raw) {
		var model map[string]any
		if err := yaml.Unmarshal(result, &model); err != nil {
			return nil, err
		}
		encoded, err := json.MarshalIndent(model, "", "  ")
		if err != nil {
			return nil, err
		}
		result = append(encoded, '\n')
	}
	if _, err := Parse(result); err != nil {
		return nil, err
	}
	return result, nil
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.AliasNode {
		node = node.Alias
		if node == nil {
			return nil
		}
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}
func setMapping(node *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			node.Content[i+1] = value
			return
		}
	}
	node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}
func deleteMapping(node *yaml.Node, key string) {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			node.Content = append(node.Content[:i], node.Content[i+2:]...)
			return
		}
	}
}
func boolNode(value bool) *yaml.Node {
	encoded := "false"
	if value {
		encoded = "true"
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: encoded}
}
