package delegate

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
)

// maxLineBytes bounds one line of runner output. A longer line is cut, and the cut part is handled as one line.
const maxLineBytes = 16 << 20

// parsed is what one line of runner output means to its task. It is computed from the line alone.
type parsed struct {
	events    []event // in order; their text is not clipped yet
	session   string  // the runner's session id, when this line gives it
	answer    string  // the final reply, when hasAnswer is set
	hasAnswer bool
	failed    bool // the final result is marked as an error
}

// toolItems are the codex items that describe a command or a tool call (CTR-DLG-05).
var toolItems = map[string]bool{
	"command_execution": true,
	"mcp_tool_call":     true,
	"file_change":       true,
	"web_search":        true,
	"tool_call":         true,
}

// parseLine maps one line of output by runner kind: "claude", "codex", or anything else for the generic rule. It
// never fails. A line that is not a JSON object, or holds nothing the mapping knows, becomes a raw event.
func parseLine(kind string, line []byte) parsed {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(line, &obj); err != nil || obj == nil {
		return rawLine(line)
	}
	switch kind {
	case "claude":
		return parseClaude(obj, line)
	case "codex":
		return parseCodex(obj, line)
	}
	return parseGeneric(obj, line)
}

func rawLine(line []byte) parsed {
	return parsed{events: []event{{typ: "raw", text: string(line)}}}
}

func parseClaude(obj map[string]json.RawMessage, line []byte) parsed {
	switch fieldString(obj, "type") {
	case "system":
		p := parsed{events: []event{{typ: "system", text: string(line)}}}
		if fieldString(obj, "subtype") == "init" {
			p.session = fieldString(obj, "session_id")
		}
		return p
	case "assistant", "user":
		blocks, ok := contentBlocks(obj["message"])
		if !ok {
			return rawLine(line)
		}
		var p parsed
		for _, raw := range blocks {
			var block map[string]json.RawMessage
			if json.Unmarshal(raw, &block) != nil {
				p.events = append(p.events, event{typ: "raw", text: string(raw)})
				continue
			}
			switch kind := fieldString(block, "type"); {
			case kind == "text" && fieldString(obj, "type") == "assistant":
				p.events = append(p.events, event{typ: "assistant", text: fieldString(block, "text")})
			case kind == "tool_use":
				p.events = append(p.events, event{typ: "tool", text: string(raw)})
			case kind == "tool_result":
				p.events = append(p.events, event{typ: "tool", text: toolResultText(block, raw)})
			default:
				p.events = append(p.events, event{typ: "raw", text: string(raw)})
			}
		}
		return p
	case "result":
		// The final result is the reply of the task and carries its error flag; it is not an output event.
		s, ok := stringField(obj, "result")
		if !ok {
			p := rawLine(line)
			p.failed = fieldBool(obj, "is_error")
			return p
		}
		return parsed{answer: s, hasAnswer: true, failed: fieldBool(obj, "is_error")}
	}
	return rawLine(line)
}

func parseCodex(obj map[string]json.RawMessage, line []byte) parsed {
	switch fieldString(obj, "type") {
	case "thread.started":
		return parsed{session: fieldString(obj, "thread_id"), events: []event{{typ: "system", text: string(line)}}}
	case "item.completed", "item.started":
		var item map[string]json.RawMessage
		if json.Unmarshal(obj["item"], &item) != nil || item == nil {
			return rawLine(line)
		}
		kind := fieldString(item, "type")
		if kind == "agent_message" && fieldString(obj, "type") == "item.completed" {
			text := fieldString(item, "text")
			return parsed{answer: text, hasAnswer: true, events: []event{{typ: "assistant", text: text}}}
		}
		if toolItems[kind] {
			return parsed{events: []event{{typ: "tool", text: string(line)}}}
		}
	}
	return rawLine(line)
}

// parseGeneric is the rule of a runner this package does not know: only a result line whose result field is a
// string counts, as the final reply. Every other line is raw.
func parseGeneric(obj map[string]json.RawMessage, line []byte) parsed {
	if fieldString(obj, "type") == "result" {
		if s, ok := stringField(obj, "result"); ok {
			return parsed{answer: s, hasAnswer: true}
		}
	}
	return rawLine(line)
}

// contentBlocks returns the items of the message content array. A content that is a string, or a message that
// is missing, reports false.
func contentBlocks(message json.RawMessage) ([]json.RawMessage, bool) {
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(message, &m) != nil {
		return nil, false
	}
	var blocks []json.RawMessage
	if json.Unmarshal(m.Content, &blocks) != nil {
		return nil, false
	}
	return blocks, true
}

// toolResultText is the text of a tool_result block: its content string, or the text parts of its content array.
// Anything else is kept as the block itself.
func toolResultText(block map[string]json.RawMessage, raw []byte) string {
	if s, ok := stringField(block, "content"); ok {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(block["content"], &parts) == nil && len(parts) > 0 {
		var out []byte
		for _, p := range parts {
			out = append(out, p.Text...)
		}
		return string(out)
	}
	return string(raw)
}

func stringField(obj map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := obj[key]
	if !ok {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

// fieldString returns a string field, or "" when it is missing or not a string.
func fieldString(obj map[string]json.RawMessage, key string) string {
	s, _ := stringField(obj, key)
	return s
}

// fieldBool returns a boolean field, false when it is missing or not a boolean.
func fieldBool(obj map[string]json.RawMessage, key string) bool {
	var b bool
	_ = json.Unmarshal(obj[key], &b)
	return b
}

// scanLines calls fn with each line of r, without its line break. Blank lines are skipped. A line longer than limit
// is passed in its first limit bytes. It returns when r fails or ends.
func scanLines(r io.Reader, limit int, fn func(line []byte)) {
	br := bufio.NewReaderSize(r, 32<<10)
	var line []byte
	truncated := false
	for {
		part, err := br.ReadSlice('\n')
		if !truncated {
			if room := limit - len(line); len(part) > room {
				part, truncated = part[:room], true
			}
			line = append(line, part...)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			if text := bytes.TrimRight(line, "\r\n"); len(text) > 0 {
				fn(text)
			}
			return
		}
		if text := bytes.TrimRight(line, "\r\n"); len(text) > 0 {
			fn(text)
		}
		line, truncated = line[:0], false
	}
}
