package computer

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/tzf1003/xmesh/internal/toolerr"
)

const (
	maxSnapshotBytes = 512 << 10
	maxRoleSnapshots = 16 * 16 // MaxSessions default sessions x per-session snapshots
)

var secretRole = regexp.MustCompile(`(?i)password|secure|passcode`)

// scrub removes values of secret fields from the tree and records each ref's role
// so ref_act can refuse to write into them.
func scrub(n any, roles map[string]string) {
	switch v := n.(type) {
	case map[string]any:
		role, _ := v["role"].(string)
		if ref, ok := v["ref_id"].(string); ok {
			roles[ref] = role
		}
		if secretRole.MatchString(role) {
			delete(v, "value")
		}
		for _, c := range v {
			scrub(c, roles)
		}
	case []any:
		for _, c := range v {
			scrub(c, roles)
		}
	}
}

func (a *AgentDesktop) Snapshot(ctx context.Context, q SnapshotQuery) (Snapshot, error) {
	if err := validSnapshotQuery(q); err != nil {
		return Snapshot{}, err
	}
	args := []string{"snapshot", "-i", "--compact"}
	if q.App != "" {
		args = append(args, "--app", q.App)
	}
	if q.WindowID != "" {
		args = append(args, "--window-id", q.WindowID)
	}
	if q.Skeleton {
		args = append(args, "--skeleton")
	}
	if q.Root != "" {
		args = append(args, "--root", q.Root)
	}
	data, aerr, err := a.call(ctx, args...)
	if err != nil {
		return Snapshot{}, err
	}
	if aerr != nil {
		switch aerr.Code {
		case "STALE_REF", "SNAPSHOT_NOT_FOUND":
			return Snapshot{}, toolerr.New("conflict", "快照或 ref 已过期", "reason", "stale_ref")
		case "AMBIGUOUS_TARGET":
			return Snapshot{}, toolerr.New("conflict", "目标不唯一，请指定 window_id", "reason", "ambiguous_target")
		case "APP_NOT_FOUND", "WINDOW_NOT_FOUND":
			return Snapshot{}, toolerr.New("not_found", "应用或窗口不存在", "name", q.App)
		}
		return Snapshot{}, mapObserveError(aerr)
	}
	var tree map[string]any
	if json.Unmarshal(data, &tree) != nil {
		return Snapshot{}, toolerr.New("desktop_unavailable", "引擎返回的快照无效", "reason", "invalid_snapshot")
	}
	id, _ := tree["snapshot_id"].(string)
	complete := true
	if c, ok := tree["complete"].(bool); ok {
		complete = c
	}
	roles := map[string]string{}
	scrub(tree, roles)
	out, err := json.Marshal(tree)
	if err != nil {
		return Snapshot{}, err
	}
	if len(out) > maxSnapshotBytes {
		return Snapshot{}, toolerr.New("too_large", "快照过大，请使用 skeleton 或 root 缩小范围", "size", len(out), "limit", maxSnapshotBytes)
	}
	a.mu.Lock()
	// The cache is shared by every session of this Host, so it keeps far more IDs
	// than one session's 16: another session's snapshots must not evict this one's refs.
	if i := slices.Index(a.order, id); i >= 0 {
		a.order = slices.Delete(a.order, i, i+1)
	} else if len(a.order) >= maxRoleSnapshots {
		delete(a.roles, a.order[0])
		a.order = a.order[1:]
	}
	a.order = append(a.order, id)
	// A --root drill-down replaces refs inside an existing snapshot, so merge.
	if a.roles[id] == nil {
		a.roles[id] = map[string]string{}
	}
	for k, v := range roles {
		a.roles[id][k] = v
	}
	a.mu.Unlock()
	return Snapshot{SnapshotID: id, Complete: complete, Tree: out}, nil
}

func refErrorCode(code string) string {
	switch code {
	case "STALE_REF", "SNAPSHOT_NOT_FOUND":
		return "stale_ref"
	case "AMBIGUOUS_TARGET":
		return "ambiguous_target"
	}
	return strings.ToLower(code)
}

// RefAct runs one headless element action: it never passes --headed, so
// agent-desktop fails closed instead of moving the pointer or taking focus.
func (a *AgentDesktop) RefAct(ctx context.Context, r RefExecution) (ActionResult, error) {
	res := ActionResult{ActionID: r.ActionID, State: "failed"}
	info, err := a.identity(ctx)
	if err != nil {
		res.Error = errorCode(err)
		return res, nil
	}
	if info.Epoch != r.Epoch {
		res.Error = "stale_ref"
		return res, nil
	}
	cmd, ok := adCatalogTable.commandForOp("computer.ref_act", r.Op)
	m := refPattern.FindStringSubmatch(r.Ref)
	if !ok || m == nil || m[1] != r.SnapshotID {
		res.Error = "invalid_args"
		return res, nil
	}
	a.mu.Lock()
	role, known := a.roles[r.SnapshotID][r.Ref]
	a.mu.Unlock()
	if !known {
		res.Error = "stale_ref"
		return res, nil
	}
	if (r.Op == "set_value" || r.Op == "type") && secretRole.MatchString(role) {
		res.Error = "policy_denied"
		return res, nil
	}
	args := []string{cmd, r.Ref}
	switch {
	case needsText(r.Op):
		if strings.HasPrefix(r.Text, "-") {
			args = append(args, "--")
		}
		args = append(args, r.Text)
	case r.Op == "scroll":
		args = append(args, "--direction", r.Direction, "--amount", itoaInt(r.Amount))
	}
	_, aerr, err := a.call(ctx, args...)
	if err != nil {
		res.State, res.Error = "unknown", "execution_unknown"
		return res, nil
	}
	if aerr != nil {
		res.Error = refErrorCode(aerr.Code)
		if !aerr.notDelivered() {
			res.State, res.Error = "unknown", "execution_unknown"
		}
		return res, nil
	}
	res.State, res.Executed = "executed", 1
	return res, nil
}

// inputRole matches the roles of elements that take typed text. Their text and title are withheld from reads,
// because what a user typed is not a label. secretRole covers password fields.
var inputRole = regexp.MustCompile(`(?i)text ?(field|area|box)|search ?field|combo ?box`)

// maxReadBytes bounds the engine's answer to one read. A larger answer is refused, never cut short.
const maxReadBytes = 64 << 10

// RefRead reads one property of one element with agent-desktop get or is. Neither command acts on the element.
// The role of a ref comes from the snapshot the adapter read, so an unknown ref is refused before the engine runs,
// and text or title of an input or password element never leaves the adapter.
func (a *AgentDesktop) RefRead(ctx context.Context, r RefReadExecution) (RefReadResult, error) {
	if err := validRefRead(r.SnapshotID, r.Ref, r.Op, r.Property); err != nil {
		return RefReadResult{}, err
	}
	cmd, ok := adCatalogTable.commandForOp("computer.ref_read", r.Op)
	if !ok {
		return RefReadResult{}, toolerr.Args("op")
	}
	a.mu.Lock()
	role, known := a.roles[r.SnapshotID][r.Ref]
	a.mu.Unlock()
	if !known {
		return RefReadResult{}, toolerr.New("conflict", "快照或 ref 已过期", "reason", "stale_ref")
	}
	if (r.Property == "text" || r.Property == "title") && (inputRole.MatchString(role) || secretRole.MatchString(role)) {
		return RefReadResult{}, toolerr.New("policy_denied", "输入框与密码框的文本不通过只读工具返回", "rule", "computer.input_text_withheld", "property", r.Property)
	}
	data, aerr, err := a.call(ctx, cmd, r.Ref, "--property", r.Property)
	if err != nil {
		return RefReadResult{}, err
	}
	if aerr != nil {
		switch aerr.Code {
		case "STALE_REF", "SNAPSHOT_NOT_FOUND":
			return RefReadResult{}, toolerr.New("conflict", "快照或 ref 已过期", "reason", "stale_ref")
		case "AMBIGUOUS_TARGET":
			return RefReadResult{}, toolerr.New("conflict", "目标不唯一，请重新快照", "reason", "ambiguous_target")
		}
		return RefReadResult{}, mapObserveError(aerr)
	}
	if len(data) > maxReadBytes {
		return RefReadResult{}, toolerr.New("too_large", "读取结果过大", "size", len(data), "limit", maxReadBytes)
	}
	var reply struct {
		Value      json.RawMessage `json:"value"`
		Applicable *bool           `json:"applicable"`
		Result     *bool           `json:"result"`
	}
	if json.Unmarshal(data, &reply) != nil {
		return RefReadResult{}, toolerr.New("desktop_unavailable", "引擎返回的读取结果无效", "reason", "invalid_read")
	}
	out := RefReadResult{Ref: r.Ref, Property: r.Property}
	if r.Op == "get" {
		if len(reply.Value) == 0 {
			return RefReadResult{}, toolerr.New("desktop_unavailable", "引擎返回的读取结果无效", "reason", "invalid_read")
		}
		out.Value = reply.Value
		return out, nil
	}
	if reply.Applicable == nil || reply.Result == nil {
		return RefReadResult{}, toolerr.New("desktop_unavailable", "引擎返回的读取结果无效", "reason", "invalid_read")
	}
	out.Applicable, out.Result = reply.Applicable, reply.Result
	return out, nil
}

func itoaInt(n int) string { return strconv.Itoa(n) }
