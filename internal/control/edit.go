package control

import (
	"bytes"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Edit changes one value of a configuration document. Path is a JSON Pointer such as
// /agent/policy/allow_dangerous; arrays are replaced as a whole. The control API itself takes
// whole-file content (PUT /v1/config); clients such as the CLI use Edit to build that content
// from the current file while keeping its comments.
type Edit struct {
	Path   string `json:"path"`
	Value  any    `json:"value"`
	Delete bool   `json:"delete"`
}

// ApplyEdits changes src through a YAML node tree, so comments and key order of
// everything not edited survive.
func ApplyEdits(src []byte, edits []Edit) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, err
	}
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("配置根必须是映射")
	}
	for _, e := range edits {
		segments, err := pointer(e.Path)
		if err != nil {
			return nil, err
		}
		if err = edit(doc.Content[0], segments, e); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Path, err)
		}
	}
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
func pointer(p string) ([]string, error) {
	if !strings.HasPrefix(p, "/") || p == "/" {
		return nil, fmt.Errorf("路径必须是 JSON Pointer: %q", p)
	}
	parts := strings.Split(p[1:], "/")
	for i, part := range parts {
		if part == "" {
			return nil, fmt.Errorf("路径含空段: %q", p)
		}
		parts[i] = strings.NewReplacer("~1", "/", "~0", "~").Replace(part)
	}
	return parts, nil
}
func edit(m *yaml.Node, segments []string, e Edit) error {
	key, rest := segments[0], segments[1:]
	index := -1
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			index = i
			break
		}
	}
	if len(rest) == 0 {
		if e.Delete {
			if index < 0 {
				return nil
			}
			m.Content = append(m.Content[:index], m.Content[index+2:]...)
			return nil
		}
		value := &yaml.Node{}
		if err := value.Encode(e.Value); err != nil {
			return err
		}
		if index >= 0 {
			old := m.Content[index+1]
			value.HeadComment, value.LineComment, value.FootComment = old.HeadComment, old.LineComment, old.FootComment
			m.Content[index+1] = value
			return nil
		}
		m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
		return nil
	}
	if index < 0 {
		if e.Delete {
			return nil
		}
		child := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, child)
		return edit(child, rest, e)
	}
	child := m.Content[index+1]
	if child.Kind != yaml.MappingNode {
		return fmt.Errorf("%q 不是映射", key)
	}
	return edit(child, rest, e)
}
