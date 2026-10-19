package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"reflect"
	"time"

	"github.com/tzf1003/xmesh/meshadmin"
)

// The JSON envelope may contain both a policy and a region, each up to 256 KiB,
// with six-byte JSON escapes. The decoded document has its own shared byte cap.
const policyDocumentLimit = 4 * 1024 * 1024

// Document operations keep draft text off argv and perform no cloud writes.
// Publish uses the existing validation, version, snapshot and confirmation path.
func meshPolicyDocument(op string, args []string, out io.Writer, in io.Reader) error {
	var c meshCommon
	fs := flag.NewFlagSet("mesh policy "+op, flag.ContinueOnError)
	c.register(fs)
	apply := fs.Bool("apply", false, "应用完整策略草稿到云端")
	yes := fs.Bool("yes", false, "已在界面确认")
	expect := fs.String("expect-version", "", "草稿来源的云端版本")
	snapDir := fs.String("snapshot-dir", "", "快照目录")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	body, err := io.ReadAll(io.LimitReader(in, policyDocumentLimit+1))
	if err != nil {
		return err
	}
	if len(body) > policyDocumentLimit {
		return fmt.Errorf("%w: policy input envelope exceeds 4 MiB", meshadmin.ErrInvalid)
	}
	var edit meshadmin.DocumentEdit
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&edit); err != nil {
		return fmt.Errorf("%w: invalid policy document input", meshadmin.ErrInvalid)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("%w: trailing policy document input", meshadmin.ErrInvalid)
	}
	if op == "document" {
		if *apply {
			return fmt.Errorf("%w: document edits are local", meshadmin.ErrInvalid)
		}
		doc, err := meshadmin.EditPolicyDocument(edit)
		if err != nil {
			return err
		}
		return emit(out, c.json, doc, func(w io.Writer) { fmt.Fprint(w, doc.Text) })
	}
	if *expect == "" {
		return fmt.Errorf("%w: --expect-version is required", meshadmin.ErrInvalid)
	}
	if edit.Template != nil || edit.Region != nil || edit.RemoveID != nil || edit.OmitDefault != nil || len(edit.Regions) > 0 {
		return fmt.Errorf("%w: publish accepts only the final policy text", meshadmin.ErrInvalid)
	}
	if _, err := meshadmin.InspectPolicyDocument(edit.Text); err != nil {
		return err
	}
	access := meshadmin.AccessRead
	if *apply {
		access = meshadmin.AccessWrite
	}
	p, _, _, err := c.provider(access)
	if err != nil {
		return err
	}
	ctx, cancel := ctx30()
	defer cancel()
	plan, err := meshadmin.PlanPolicy(ctx, p, "policy document", func(string) (string, []string, error) { return edit.Text, nil, nil })
	if err != nil {
		return err
	}
	baseVersion := plan.BaseVersion
	if !plan.BaseExists {
		baseVersion = "absent"
	}
	if baseVersion != *expect {
		return fmt.Errorf("%w: 云端策略已变化，请保留草稿并重新加载比较", meshadmin.ErrConflict)
	}
	probes, err := meshadmin.ChangedDocumentRelays(plan.Before, plan.After)
	if err != nil {
		return err
	}
	caps, err := p.Capabilities(ctx)
	if err != nil {
		return err
	}
	if len(probes) > 0 && !caps.DERPViaPolicy {
		return fmt.Errorf("%w: this control plane requires DERP regions in its server configuration", meshadmin.ErrUnsupported)
	}
	if len(probes) > 0 && !plan.BaseExists {
		return fmt.Errorf("%w: create a base policy before adding DERP regions so failed probes can restore it", meshadmin.ErrUnsupported)
	}
	if len(probes) > 8 {
		return fmt.Errorf("%w: at most 8 changed DERP nodes may be published at once", meshadmin.ErrInvalid)
	}
	store, err := c.snapshots(*snapDir)
	if err != nil {
		return err
	}
	var result bytes.Buffer
	if !*apply {
		return finishPlan(out, &c, p, store, plan, false, *yes, *expect, "")
	}
	if err := confirm(out, "应用完整策略草稿到云端", *yes); err != nil {
		return err
	}
	writeCtx, writeCancel := ctx30()
	defer writeCancel()
	res, err := plan.Apply(writeCtx, p, store, true)
	if err != nil {
		return err
	}
	for _, target := range probes {
		// Each node has its own five-second probe budget. Control-plane calls
		// cannot consume the time reserved for later nodes.
		probe := meshadmin.ProbeDERP(context.Background(), target, meshadmin.ProbeOptions{})
		if probe.OK {
			continue
		}
		// Restore only while the applied version is still current, so a probe
		// failure cannot overwrite another administrator's subsequent changes.
		restoreCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		current, rerr := p.GetPolicy(restoreCtx)
		if rerr != nil {
			return fmt.Errorf("DERP 探测失败；读取恢复条件失败：%w", rerr)
		}
		if current.Version != res.Version {
			return fmt.Errorf("DERP 探测失败；云端策略已再次变化，请通过快照人工比较恢复")
		}
		restore := &meshadmin.PolicyPlan{Provider: p.Kind(), Label: "restore after policy DERP probe failure", BaseVersion: current.Version, BaseExists: true, Before: current.Text, After: plan.Before, Changed: true, Writable: plan.Writable}
		if _, rerr = restore.Apply(restoreCtx, p, store, true); rerr != nil {
			return fmt.Errorf("DERP 探测失败，恢复原策略失败：%w", rerr)
		}
		return fmt.Errorf("%w: 新增或修改的中继不可用，已恢复写入前的策略；本地草稿已保留", meshadmin.ErrProbeFailed)
	}
	if err := emit(&result, c.json, map[string]any{"plan": plan, "applied": true, "result": res}, func(w io.Writer) { fmt.Fprint(w, plan.Diff) }); err != nil {
		return err
	}
	if *apply && plan.Changed {
		readCtx, readCancel := ctx30()
		defer readCancel()
		current, err := p.GetPolicy(readCtx)
		if err != nil {
			return fmt.Errorf("策略已写入，但云端回读失败；请重新读取确认：%w", err)
		}
		var actual, wanted any
		actualJSON, err := meshadmin.StandardJSON(current.Text)
		if err != nil {
			return fmt.Errorf("策略已写入，但云端回读格式无效：%w", err)
		}
		wantedJSON, err := meshadmin.StandardJSON(plan.After)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(actualJSON, &actual); err != nil {
			return err
		}
		if err := json.Unmarshal(wantedJSON, &wanted); err != nil {
			return err
		}
		if !reflect.DeepEqual(actual, wanted) {
			return fmt.Errorf("策略已写入，但云端回读内容与提交内容不一致；请保留草稿并重新读取比较")
		}
	}
	_, err = out.Write(result.Bytes())
	return err
}
