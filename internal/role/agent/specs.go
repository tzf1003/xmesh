package agent

import (
	"encoding/json"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/tzf1003/xmesh/internal/toolerr"
	"strings"
	"sync"
)

type spec struct {
	desc     string
	schema   map[string]any
	compiled *jsonschema.Schema
}

func (s spec) validate(raw json.RawMessage) error {
	var v any
	if e := json.Unmarshal(raw, &v); e != nil {
		return toolerr.Args("arguments")
	}
	if e := s.compiled.Validate(v); e != nil {
		return toolerr.Args("arguments")
	}
	return nil
}

var builtinSpecs = sync.OnceValue(buildSpecs)

func buildSpecs() map[string]spec {
	props := map[string]any{}
	for _, k := range strings.Fields("path cwd from to content old_string new_string expected_sha256 pattern glob type output_mode cmd stdin mode idempotency_key output_ref stream session_id shell name data signal to_caller") {
		props[k] = map[string]any{"type": "string"}
	}
	for _, k := range strings.Fields("offset limit depth context head_limit cols rows cursor max_bytes wait_ms") {
		props[k] = map[string]any{"type": "integer"}
	}
	for _, k := range strings.Fields("replace_all recursive parents overwrite show_hidden ignore_case multiline include_ignored") {
		props[k] = map[string]any{"type": "boolean"}
	}
	props["args"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	props["env"] = map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}}
	props["timeout"] = map[string]any{"type": []string{"string", "integer"}}
	definitions := map[string][3]string{
		"sys.info": {"查询设备信息与配额", "", ""}, "fs.read": {"读取文件", "path", "path cwd offset limit"}, "fs.write": {"原子覆盖文件", "path content", "path cwd content expected_sha256"}, "fs.edit": {"精确替换文件文本", "path old_string new_string", "path cwd old_string new_string expected_sha256 replace_all"}, "fs.glob": {"按路径模式搜索", "pattern", "path cwd pattern limit include_ignored"}, "fs.grep": {"按 RE2 搜索文本", "pattern output_mode", "path cwd pattern glob type output_mode context ignore_case multiline head_limit"}, "fs.list": {"列出目录", "path", "path cwd depth show_hidden"}, "fs.stat": {"查询路径元数据", "path", "path cwd"}, "fs.sha256": {"计算文件哈希", "path", "path cwd"}, "fs.mkdir": {"创建目录", "path", "path cwd parents"}, "fs.move": {"移动文件或目录", "from to", "from to cwd overwrite"}, "fs.delete": {"删除路径", "path", "path cwd recursive"},
		"exec.run": {"直接按 argv 执行命令", "cmd", "cmd args cwd env stdin timeout mode idempotency_key"}, "exec.output": {"分页读取执行输出", "output_ref", "output_ref offset limit stream"},
		"pty.open": {"打开交互终端", "cols rows", "shell cwd env cols rows name"}, "pty.write": {"写入所属终端", "session_id data", "session_id data"}, "pty.read": {"读取终端增量输出", "session_id", "session_id cursor max_bytes wait_ms"}, "pty.resize": {"调整终端尺寸", "session_id cols rows", "session_id cols rows"}, "pty.signal": {"向终端发送信号", "session_id signal", "session_id signal"}, "pty.transfer": {"交接终端所有权", "session_id to_caller", "session_id to_caller"}, "pty.close": {"关闭所属终端", "session_id", "session_id"}, "pty.list": {"列出可见终端", "", ""},
	}
	specs := map[string]spec{}
	for name, d := range definitions {
		properties := map[string]any{}
		for _, k := range strings.Fields(d[2]) {
			properties[k] = props[k]
		}
		schema := map[string]any{"type": "object", "properties": properties}
		if d[1] != "" {
			schema["required"] = strings.Fields(d[1])
		}
		compiler := jsonschema.NewCompiler()
		b, _ := json.Marshal(schema)
		var normalized any
		_ = json.Unmarshal(b, &normalized)
		if e := compiler.AddResource("urn:xmesh:"+name, normalized); e != nil {
			panic(e)
		}
		compiled, e := compiler.Compile("urn:xmesh:" + name)
		if e != nil {
			panic(e)
		}
		specs[name] = spec{d[0], schema, compiled}
	}
	return specs
}
