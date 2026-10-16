package meshadmin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// PolicyPlan is a previewed policy change. Nothing is written until Apply is
// called with confirmed=true (RUL-21: preview, validate, version check,
// snapshot, confirmation).
type PolicyPlan struct {
	Provider    Kind     `json:"provider"`
	Label       string   `json:"label"`
	BaseVersion string   `json:"base_version"`
	BaseExists  bool     `json:"base_exists"`
	Before      string   `json:"before"`
	After       string   `json:"after"`
	Diff        string   `json:"diff"`
	Changed     bool     `json:"changed"`
	Warnings    []string `json:"warnings,omitempty"`
	// Writable is false when the control plane cannot take a policy write
	// (Headscale policy.mode=file). After then holds the text to install by hand.
	Writable bool `json:"writable"`
}

// Edit turns the current policy text into the desired text and may return warnings.
type Edit func(current string) (next string, warnings []string, err error)

// PlanPolicy reads the policy, applies edit, validates the result with the
// control plane and returns the preview.
func PlanPolicy(ctx context.Context, p Provider, label string, edit Edit) (*PolicyPlan, error) {
	caps, err := p.Capabilities(ctx)
	if err != nil {
		return nil, err
	}
	cur, err := p.GetPolicy(ctx)
	if err != nil {
		return nil, err
	}
	next, warns, err := edit(cur.Text)
	if err != nil {
		return nil, err
	}
	if err := p.ValidatePolicy(ctx, next); err != nil {
		return nil, err
	}
	return &PolicyPlan{
		Provider: p.Kind(), Label: label, BaseVersion: cur.Version, BaseExists: cur.Exists,
		Before: cur.Text, After: next, Diff: UnifiedDiff(cur.Text, next, 3), Changed: cur.Text != next,
		Warnings: warns, Writable: caps.PolicyWrite,
	}, nil
}

// ApplyResult describes a completed write.
type ApplyResult struct {
	SnapshotID string `json:"snapshot_id,omitempty"`
	Version    string `json:"version"`
	Changed    bool   `json:"changed"`
}

// Apply writes the planned policy. It snapshots the previous text first so
// Rollback can restore it, and the version check makes a concurrent edit fail
// with ErrConflict instead of being overwritten.
func (pl *PolicyPlan) Apply(ctx context.Context, p Provider, store SnapshotStore, confirmed bool) (ApplyResult, error) {
	if !confirmed {
		return ApplyResult{}, ErrNotConfirmed
	}
	if p.Kind() != pl.Provider {
		return ApplyResult{}, fmt.Errorf("%w: plan was made for %s", ErrInvalid, pl.Provider)
	}
	if !pl.Changed {
		return ApplyResult{Version: pl.BaseVersion}, nil
	}
	if !pl.Writable {
		return ApplyResult{}, fmt.Errorf("%w: policy writes are disabled on this control plane", ErrUnsupported)
	}
	var id string
	if pl.BaseExists && store != nil {
		var err error
		id, err = store.Save(Snapshot{Provider: p.Kind(), Label: pl.Label, Text: pl.Before, Version: pl.BaseVersion})
		if err != nil {
			return ApplyResult{}, fmt.Errorf("meshadmin: snapshot before write: %w", err)
		}
	}
	doc, err := p.SetPolicy(ctx, pl.After, pl.BaseVersion)
	if err != nil {
		return ApplyResult{SnapshotID: id}, err
	}
	return ApplyResult{SnapshotID: id, Version: doc.Version, Changed: true}, nil
}

// Rollback restores a snapshot. The policy being replaced is itself snapshotted
// first, so a rollback can be rolled back.
func Rollback(ctx context.Context, p Provider, store SnapshotStore, id string) (ApplyResult, error) {
	snap, err := store.Load(id)
	if err != nil {
		return ApplyResult{}, err
	}
	if snap.Provider != p.Kind() {
		return ApplyResult{}, fmt.Errorf("%w: snapshot %s belongs to %s", ErrInvalid, id, snap.Provider)
	}
	pl, err := PlanPolicy(ctx, p, "rollback to "+id, func(string) (string, []string, error) { return snap.Text, nil, nil })
	if err != nil {
		return ApplyResult{}, err
	}
	return pl.Apply(ctx, p, store, true)
}

// UnifiedDiff renders a unified diff of two texts with ctx lines of context.
func UnifiedDiff(a, b string, ctxLines int) string {
	if a == b {
		return ""
	}
	al, bl := splitLines(a), splitLines(b)
	ops := diffOps(al, bl)
	var out strings.Builder
	out.WriteString("--- current\n+++ proposed\n")
	i := 0
	for i < len(ops) {
		if ops[i].kind == ' ' {
			i++
			continue
		}
		start := i
		for start > 0 && i-start < ctxLines && ops[start-1].kind == ' ' {
			start--
		}
		// extend the hunk while changes are within 2*ctx lines of each other
		end := i
		for end < len(ops) {
			if ops[end].kind != ' ' {
				end++
				continue
			}
			gap := 0
			for k := end; k < len(ops) && ops[k].kind == ' '; k++ {
				gap++
			}
			if end+gap >= len(ops) || gap > 2*ctxLines {
				break
			}
			end += gap
		}
		tail := end
		for n := 0; tail < len(ops) && ops[tail].kind == ' ' && n < ctxLines; n++ {
			tail++
		}
		aStart, bStart, aLen, bLen := 0, 0, 0, 0
		for k := 0; k < start; k++ {
			if ops[k].kind != '+' {
				aStart++
			}
			if ops[k].kind != '-' {
				bStart++
			}
		}
		for k := start; k < tail; k++ {
			if ops[k].kind != '+' {
				aLen++
			}
			if ops[k].kind != '-' {
				bLen++
			}
		}
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", aStart+1, aLen, bStart+1, bLen)
		for k := start; k < tail; k++ {
			out.WriteByte(ops[k].kind)
			out.WriteString(ops[k].text)
			out.WriteByte('\n')
		}
		i = tail
	}
	return out.String()
}

type diffOp struct {
	kind byte // ' ', '-', '+'
	text string
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// diffOps is a longest-common-subsequence diff; policy files are small, and
// beyond the cap it degrades to replace-everything, which is still correct.
func diffOps(a, b []string) []diffOp {
	if len(a)*len(b) > 4_000_000 {
		ops := make([]diffOp, 0, len(a)+len(b))
		for _, l := range a {
			ops = append(ops, diffOp{'-', l})
		}
		for _, l := range b {
			ops = append(ops, diffOp{'+', l})
		}
		return ops
	}
	n, m := len(a), len(b)
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var ops []diffOp
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{' ', a[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, diffOp{'-', a[i]})
			i++
		default:
			ops = append(ops, diffOp{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{'-', a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{'+', b[j]})
	}
	return ops
}

// IsConflict reports whether err is a version-check failure.
func IsConflict(err error) bool { return errors.Is(err, ErrConflict) }

// Snapshot is a saved policy text.
type Snapshot struct {
	ID       string    `json:"id"`
	Provider Kind      `json:"provider"`
	Time     time.Time `json:"time"`
	Label    string    `json:"label"`
	Version  string    `json:"version"`
	Text     string    `json:"text,omitempty"`
}
