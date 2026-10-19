package onboarding

import (
	"strings"

	"go.yaml.in/yaml/v3"
)

// PublicPlan returns the review representation of a private persisted plan.
// Existing command arguments and connection credentials are represented by
// placeholders; applying the plan always uses the private persisted config.
func PublicPlan(p *Plan) *Plan {
	if p == nil {
		return nil
	}
	copy := *p
	var document any
	if err := yaml.Unmarshal([]byte(p.Config), &document); err != nil {
		copy.Config = "配置预览暂不可用；请重新生成计划。"
		return &copy
	}
	redactPlanValues(document)
	encoded, err := yaml.Marshal(document)
	if err != nil {
		copy.Config = "配置预览暂不可用；请重新生成计划。"
		return &copy
	}
	copy.Config = string(encoded)
	return &copy
}

func redactPlanValues(value any) {
	switch object := value.(type) {
	case map[string]any:
		for key, item := range object {
			lower := strings.ToLower(key)
			sensitive := lower == "command" || lower == "args" || lower == "env" || lower == "environment" || lower == "headers" || lower == "http_headers" || lower == "url" || lower == "allow_cmd" || lower == "deny_patterns" || strings.Contains(lower, "secret") || strings.Contains(lower, "password") || lower == "authorization" || lower == "token" || lower == "api_key" || lower == "authkey"
			if sensitive {
				object[key] = "[已保存的私有配置]"
				continue
			}
			redactPlanValues(item)
		}
	case []any:
		for _, item := range object {
			redactPlanValues(item)
		}
	}
}
