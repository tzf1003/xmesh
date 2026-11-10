package computer

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tzf1003/xmesh/internal/toolerr"
)

// maxCapturePixels matches the native executor's 32 Mi pixel frame bound.
const maxCapturePixels = 32 << 20

// Runner executes one agent-desktop command and returns its stdout envelope.
// The CLI prints a JSON envelope for failures too, so a non-zero exit with a
// parseable envelope is not a Runner error.
type Runner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

// ExecRunner runs the pinned agent-desktop binary as a child of the Host, so it
// inherits the user's graphical session and accessibility grant.
type ExecRunner struct {
	Path string
	// TempDir must be owned by the user with mode 0700 (agent-desktop requires it on macOS).
	TempDir string
}

func (r ExecRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, r.Path, args...)
	cmd.Env = append(os.Environ(), "TMPDIR="+r.TempDir)
	var out limitedBuffer
	out.limit = 16 << 20
	cmd.Stdout = &out
	// stderr is dropped: diagnostics must not reach the audit stream or logs.
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if out.over {
		return nil, errors.New("agent-desktop output too large")
	}
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return out.Bytes(), nil
}

type limitedBuffer struct {
	bytes.Buffer
	limit int
	over  bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		b.over = true
		return 0, io.ErrShortWrite
	}
	return b.Buffer.Write(p)
}

type adEnvelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error *adError        `json:"error"`
}

type adError struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	Disposition struct {
		Delivery string `json:"delivery"`
		Retry    string `json:"retry"`
	} `json:"disposition"`
}

func (e *adError) Error() string { return e.Code }

// notDelivered reports whether agent-desktop proved nothing reached the application.
func (e *adError) notDelivered() bool { return e.Disposition.Delivery == "not_delivered" }

// AgentDesktop is a Provider backed by the agent-desktop CLI.
//
// Coordinate and keyboard input use agent-desktop's physical (--headed) path,
// which is equivalent to the foreground interaction of the native executor: it
// shares the system pointer and focus. Fallback, when set (macOS), keeps serving
// background interaction — agent-desktop has no per-process pointer delivery, so
// pixel-level background input stays on the proven native module.
type AgentDesktop struct {
	run      Runner
	fallback Provider
	tmp      string
	epoch    string
	mu       sync.Mutex
	// roles maps snapshot id -> ref -> accessibility role, bounded to recent snapshots.
	roles map[string]map[string]string
	order []string
}

// NewAgentDesktop builds the adapter. tmp is a private (0700) scratch directory
// for screenshot files; fallback may be nil.
func NewAgentDesktop(run Runner, fallback Provider, tmp string) *AgentDesktop {
	return &AgentDesktop{run: run, fallback: fallback, tmp: tmp, epoch: "ad-" + randHex(8), roles: map[string]map[string]string{}}
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (a *AgentDesktop) call(ctx context.Context, args ...string) (json.RawMessage, *adError, error) {
	// Default deny: only a command the catalog maps to an xmesh tool may reach the CLI.
	if _, ok := adCatalogTable.mapped(args); !ok {
		return nil, nil, errUnmappedCommand
	}
	raw, err := a.run.Run(ctx, args...)
	if err != nil {
		return nil, nil, err
	}
	var env adEnvelope
	if json.Unmarshal(raw, &env) != nil {
		return nil, nil, errors.New("agent-desktop returned an invalid envelope")
	}
	if !env.OK {
		if env.Error == nil {
			return nil, nil, errors.New("agent-desktop returned an invalid error")
		}
		return nil, env.Error, nil
	}
	return env.Data, nil, nil
}

type adDisplay struct {
	ID     json.RawMessage `json:"id"`
	Bounds struct {
		X      float64 `json:"x"`
		Y      float64 `json:"y"`
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
	} `json:"bounds"`
	Scale float64 `json:"scale"`
}

func (d adDisplay) id(index int) string {
	s := strings.Trim(string(d.ID), `" `)
	if s == "" || s == "null" {
		return strconv.Itoa(index)
	}
	return s
}

func decodeList(data json.RawMessage, key string, out any) error {
	var wrapped map[string]json.RawMessage
	if json.Unmarshal(data, &wrapped) == nil {
		if v, ok := wrapped[key]; ok {
			return json.Unmarshal(v, out)
		}
	}
	return json.Unmarshal(data, out)
}

func (a *AgentDesktop) displays(ctx context.Context) ([]adDisplay, error) {
	data, aerr, err := a.call(ctx, "list-displays")
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return nil, mapObserveError(aerr)
	}
	var ds []adDisplay
	if err = decodeList(data, "displays", &ds); err != nil || len(ds) == 0 {
		return nil, toolerr.New("desktop_unavailable", "没有可用显示器", "reason", "no_display")
	}
	return ds, nil
}

// Lists runs one of the two read-only listings and returns the data as the engine wrote it. Application and
// window names come from other applications, so callers must treat the result as untrusted.
func (a *AgentDesktop) Lists(ctx context.Context, command string) (json.RawMessage, error) {
	if command != "list-apps" && command != "list-windows" {
		return nil, errUnmappedCommand
	}
	data, aerr, err := a.call(ctx, command)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return nil, mapObserveError(aerr)
	}
	return data, nil
}

func layoutOf(ds []adDisplay) string {
	h := sha256.New()
	for i, d := range ds {
		fmt.Fprintf(h, "%d|%s|%v|%v|%v|%v|%v\n", i, d.id(i), d.Bounds.X, d.Bounds.Y, d.Bounds.Width, d.Bounds.Height, d.Scale)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func mapObserveError(e *adError) error {
	switch e.Code {
	case "PERM_DENIED":
		return toolerr.New("permission_required", "需要系统桌面权限", "reason", "agent_desktop_permission")
	case "INVALID_ARGS":
		return toolerr.Args("arguments")
	case "TIMEOUT", "APP_UNRESPONSIVE":
		return toolerr.New("desktop_unavailable", "桌面引擎超时", "reason", "agent_desktop_timeout")
	}
	return toolerr.New("desktop_unavailable", "桌面引擎失败", "reason", "agent_desktop_failed")
}

type adPermissions struct {
	Accessibility   struct{ State string } `json:"accessibility"`
	ScreenRecording struct{ State string } `json:"screen_recording"`
}

func (a *AgentDesktop) own(ctx context.Context) (Info, error) {
	info := Info{ProtocolVersion: ProtocolVersion, State: "ready", Epoch: a.epoch, DesktopID: "desktop", Capabilities: []string{}, Displays: []Display{}}
	data, aerr, err := a.call(ctx, "permissions")
	if err != nil {
		return Info{}, err
	}
	if aerr != nil {
		return Info{}, mapObserveError(aerr)
	}
	var p adPermissions
	_ = json.Unmarshal(data, &p)
	if p.Accessibility.State == "denied" || p.ScreenRecording.State == "denied" {
		info.State, info.Reason = "permission_required", "agent_desktop_permission"
		return info, nil
	}
	ds, err := a.displays(ctx)
	if err != nil {
		return Info{}, err
	}
	for i, d := range ds {
		scale := d.Scale
		if scale <= 0 {
			scale = 1
		}
		info.Displays = append(info.Displays, Display{ID: d.id(i), Name: "display " + strconv.Itoa(i), X: int(d.Bounds.X), Y: int(d.Bounds.Y), Width: int(d.Bounds.Width), Height: int(d.Bounds.Height), Scale: scale})
	}
	info.Available = true
	info.LayoutVersion = layoutOf(ds)
	info.Capabilities = []string{"screenshot", "pointer", "keyboard", "refs", "engine:agent-desktop"}
	return info, nil
}

// Info reports the engine's own desktop identity. With a fallback, the fallback
// owns identity (epoch, layout, targets) so frames from both paths agree.
// unmapped_commands always comes from this engine's catalog, whichever path answered.
func (a *AgentDesktop) Info(ctx context.Context) (Info, error) {
	info, err := a.engineInfo(ctx)
	if err == nil {
		info.UnmappedCommands = adCatalogTable.unmapped()
	}
	return info, err
}

func (a *AgentDesktop) engineInfo(ctx context.Context) (Info, error) {
	own, ownErr := a.own(ctx)
	if a.fallback == nil {
		return own, ownErr
	}
	fb, fbErr := a.fallback.Info(ctx)
	return combineInfo(own, ownErr, fb, fbErr)
}

// combineInfo merges the engine's own identity with the native fallback's. The native executor judges the
// desktop session: when it reports session_inactive, the engine's own ready state must not stand in for that
// judgement, because the engine's grant drives the global pointer, which a session off the console must not reach.
func combineInfo(own Info, ownErr error, fb Info, fbErr error) (Info, error) {
	if fbErr != nil {
		if ownErr != nil || !own.Available {
			return own, ownErr
		}
		return own, nil // background path unavailable; foreground still works
	}
	if ownErr != nil {
		return Info{}, ownErr
	}
	if !own.Available {
		// The engine lacks its own grant: expose only what the fallback can do in background.
		fb.Capabilities = slices.DeleteFunc(slices.Clone(fb.Capabilities), func(c string) bool { return c == "pointer" || c == "keyboard" })
		fb.Capabilities = append(fb.Capabilities, "engine:agent-desktop")
		return fb, nil
	}
	if !fb.Available {
		if fb.State == "session_inactive" {
			return fb, nil
		}
		return own, nil
	}
	fb.Capabilities = appendUnique(fb.Capabilities, "engine:agent-desktop", "refs")
	return fb, nil
}

func appendUnique(s []string, v ...string) []string {
	for _, x := range v {
		if !slices.Contains(s, x) {
			s = append(s, x)
		}
	}
	return s
}

func background(interaction, target string) bool { return interaction == "background" || target != "" }

// identity returns the epoch and layout every foreground frame must carry.
func (a *AgentDesktop) identity(ctx context.Context) (Info, error) {
	info, err := a.Info(ctx)
	if err != nil {
		return Info{}, err
	}
	if !info.Available {
		return Info{}, toolerr.New("desktop_unavailable", "桌面当前不可用", "reason", info.Reason, "state", info.State)
	}
	return info, nil
}

func (a *AgentDesktop) Capture(ctx context.Context, r CaptureRequest) (Frame, error) {
	if r.TargetID != "" {
		if a.fallback == nil {
			return Frame{}, toolerr.Args("target_id")
		}
		return a.fallback.Capture(ctx, r)
	}
	info, err := a.identity(ctx)
	if err != nil {
		return Frame{}, err
	}
	ds, err := a.displays(ctx)
	if err != nil {
		return Frame{}, err
	}
	index := 0
	if r.DisplayID != "" {
		index = -1
		for i, d := range ds {
			if d.id(i) == r.DisplayID {
				index = i
			}
		}
		if index < 0 {
			return Frame{}, toolerr.Args("display_id")
		}
	}
	d := ds[index]
	path := filepath.Join(a.tmp, "shot-"+randHex(8)+".png")
	defer os.Remove(path)
	_, aerr, err := a.call(ctx, "screenshot", "--screen", strconv.Itoa(index), path)
	if err != nil {
		return Frame{}, err
	}
	if aerr != nil {
		return Frame{}, mapObserveError(aerr)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Frame{}, toolerr.New("desktop_unavailable", "截图失败", "reason", "no_screenshot")
	}
	region := Region{Width: int(d.Bounds.Width), Height: int(d.Bounds.Height)}
	if r.Region != nil {
		region = *r.Region
		if region.X+region.Width > int(d.Bounds.Width) || region.Y+region.Height > int(d.Bounds.Height) {
			return Frame{}, toolerr.Args("region")
		}
	}
	img, w, h, err := shape(raw, region, d, r.MaxWidth)
	if err != nil {
		return Frame{}, err
	}
	return Frame{Epoch: info.Epoch, DisplayID: d.id(index), LayoutVersion: info.LayoutVersion, Width: w, Height: h, Region: region, MIMEType: "image/png", Image: img}, nil
}

// shape crops the display screenshot to region and scales it down to maxWidth.
// The screenshot may be in device pixels (Retina) while Region is in desktop
// units, so the crop uses the actual image-to-display ratio.
func shape(raw []byte, region Region, d adDisplay, maxWidth int) ([]byte, int, int, error) {
	cfg, err := png.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width*cfg.Height > maxCapturePixels || d.Bounds.Width < 1 || d.Bounds.Height < 1 {
		return nil, 0, 0, toolerr.New("desktop_unavailable", "截图无效", "reason", "invalid_frame")
	}
	src, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, 0, 0, toolerr.New("desktop_unavailable", "截图无效", "reason", "invalid_frame")
	}
	sx, sy := float64(cfg.Width)/d.Bounds.Width, float64(cfg.Height)/d.Bounds.Height
	crop := image.Rect(int(float64(region.X)*sx), int(float64(region.Y)*sy), int(float64(region.X+region.Width)*sx+0.5), int(float64(region.Y+region.Height)*sy+0.5)).Intersect(src.Bounds())
	if crop.Empty() {
		return nil, 0, 0, toolerr.Args("region")
	}
	w, h := crop.Dx(), crop.Dy()
	if maxWidth > 0 && w > maxWidth {
		h = max(1, h*maxWidth/w)
		w = maxWidth
	}
	if w != crop.Dx() || h != crop.Dy() || crop != src.Bounds() {
		src = boxScale(src, crop, w, h)
	} else if _, ok := src.(*image.NRGBA); !ok {
		src = boxScale(src, crop, w, h)
	}
	var out bytes.Buffer
	if err = png.Encode(&out, src); err != nil {
		return nil, 0, 0, err
	}
	return out.Bytes(), w, h, nil
}

// boxScale resamples crop of src to w×h by averaging source pixels per cell.
func boxScale(src image.Image, crop image.Rectangle, w, h int) image.Image {
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	cw, ch := crop.Dx(), crop.Dy()
	for y := 0; y < h; y++ {
		y0, y1 := crop.Min.Y+y*ch/h, crop.Min.Y+(y+1)*ch/h
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < w; x++ {
			x0, x1 := crop.Min.X+x*cw/w, crop.Min.X+(x+1)*cw/w
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var r, g, b, n uint64
			for yy := y0; yy < y1; yy++ {
				for xx := x0; xx < x1; xx++ {
					pr, pg, pb, _ := src.At(xx, yy).RGBA()
					r, g, b, n = r+uint64(pr>>8), g+uint64(pg>>8), b+uint64(pb>>8), n+1
				}
			}
			dst.SetNRGBA(x, y, color.NRGBA{uint8(r / n), uint8(g / n), uint8(b / n), 255})
		}
	}
	return dst
}

func (a *AgentDesktop) Execute(ctx context.Context, r Execution) (ActionResult, error) {
	if background(r.Interaction, r.TargetID) {
		if a.fallback == nil {
			return ActionResult{ActionID: r.ActionID, State: "failed", Error: "background_unsupported"}, nil
		}
		return a.fallback.Execute(ctx, r)
	}
	res := ActionResult{ActionID: r.ActionID, State: "failed"}
	info, err := a.identity(ctx)
	if err != nil {
		res.Error = errorCode(err)
		return res, nil
	}
	if info.Epoch != r.Epoch || (r.Frame != nil && info.LayoutVersion != r.Frame.LayoutVersion) {
		res.Error = "stale_frame"
		return res, nil
	}
	ds, err := a.displays(ctx)
	if err != nil {
		res.Error = errorCode(err)
		return res, nil
	}
	for i, act := range r.Actions {
		args, err := foregroundCommand(act, r.Frame, ds)
		if err == nil && act.Type == "wait" {
			select {
			case <-time.After(time.Duration(act.DurationMS) * time.Millisecond):
			case <-ctx.Done():
				res.Executed, res.State, res.Error = i, "unknown", "cancelled"
				return res, nil
			}
			continue
		}
		if err != nil {
			res.Executed, res.Error = i, errorCode(err)
			return res, nil
		}
		for _, step := range args {
			_, aerr, err := a.call(ctx, step...)
			if err != nil {
				// The command may have been delivered before the runner failed or was cancelled.
				res.Executed, res.State, res.Error = i, "unknown", "execution_unknown"
				return res, nil
			}
			if aerr != nil {
				res.Executed, res.Error = i, strings.ToLower(aerr.Code)
				if !aerr.notDelivered() {
					res.State, res.Error = "unknown", "execution_unknown"
				}
				return res, nil
			}
		}
	}
	res.State, res.Executed = "executed", len(r.Actions)
	return res, nil
}

func (a *AgentDesktop) Release(ctx context.Context, r Execution) error {
	if a.fallback != nil {
		return a.fallback.Release(ctx, r)
	}
	// Every foreground action is a self-contained CLI call; nothing is held.
	return nil
}

// Exited lets the Host replace a failed fallback child.
func (a *AgentDesktop) Exited() bool {
	if c, ok := a.fallback.(interface{ Exited() bool }); ok {
		return c.Exited()
	}
	return false
}

func (a *AgentDesktop) Close() error {
	if a.fallback != nil {
		return a.fallback.Close()
	}
	return nil
}

// desktopPoint converts image pixel coordinates of frame into desktop coordinates:
// display origin + capture-region origin + image coordinate × region size / image size.
func desktopPoint(f *Frame, ds []adDisplay, x, y int) (int, int, error) {
	if f == nil || f.Width < 1 || f.Height < 1 {
		return 0, 0, toolerr.Args("frame_id")
	}
	var d *adDisplay
	for i := range ds {
		if ds[i].id(i) == f.DisplayID {
			d = &ds[i]
		}
	}
	if d == nil {
		return 0, 0, toolerr.New("conflict", "画面所属显示器已变化", "reason", "stale_frame")
	}
	if x < 0 || y < 0 || x >= f.Width || y >= f.Height {
		return 0, 0, toolerr.Args("actions.coordinates")
	}
	px := int(d.Bounds.X) + f.Region.X + x*f.Region.Width/f.Width
	py := int(d.Bounds.Y) + f.Region.Y + y*f.Region.Height/f.Height
	return px, py, nil
}

func xy(x, y int) string { return strconv.Itoa(x) + "," + strconv.Itoa(y) }

var keyAliases = map[string]string{"control": "ctrl", "option": "alt", "command": "cmd", "super": "cmd", "win": "cmd", "enter": "return", "esc": "escape", "backspace": "delete", "del": "delete", " ": "space"}

func normalizeKey(k string) string {
	l := strings.ToLower(k)
	if v, ok := keyAliases[l]; ok {
		return v
	}
	if len(k) == 1 && k[0] >= 'A' && k[0] <= 'Z' {
		return "shift+" + l
	}
	return l
}

const typeable = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 ,.;'/-=[]\\`"

// foregroundCommand translates one action into agent-desktop invocations.
// Text without an element ref has no agent-desktop equivalent beyond key presses,
// so only plain ASCII is accepted; richer input belongs to the ref-based tools.
func foregroundCommand(a Action, f *Frame, ds []adDisplay) ([][]string, error) {
	switch a.Type {
	case "wait":
		return nil, nil
	case "move", "click", "scroll":
		x, y, err := desktopPoint(f, ds, *a.X, *a.Y)
		if err != nil {
			return nil, err
		}
		switch a.Type {
		case "move":
			return [][]string{{"--headed", "mouse-move", "--xy", xy(x, y)}}, nil
		case "click":
			n := max(a.Clicks, 1)
			b := a.Button
			if b == "" {
				b = "left"
			}
			return [][]string{{"--headed", "mouse-click", "--xy", xy(x, y), "--button", b, "--count", strconv.Itoa(n)}}, nil
		}
		// Wheel deltas are discrete steps: positive is right/down.
		return [][]string{{"--headed", "mouse-wheel", "--x", strconv.Itoa(x), "--y", strconv.Itoa(y), "--dx", strconv.Itoa(a.DeltaX), "--dy", strconv.Itoa(a.DeltaY)}}, nil
	case "drag":
		x, y, err := desktopPoint(f, ds, *a.X, *a.Y)
		if err != nil {
			return nil, err
		}
		tx, ty, err := desktopPoint(f, ds, *a.ToX, *a.ToY)
		if err != nil {
			return nil, err
		}
		args := []string{"--headed", "drag", "--from-xy", xy(x, y), "--to-xy", xy(tx, ty)}
		if a.DurationMS > 0 {
			args = append(args, "--duration", strconv.Itoa(a.DurationMS), "--timeout-ms", strconv.Itoa(a.DurationMS+5000))
		}
		return [][]string{args}, nil
	case "key":
		parts := make([]string, len(a.Keys))
		for i, k := range a.Keys {
			parts[i] = normalizeKey(k)
			if len(a.Keys) > 1 {
				// In a combination the modifiers are explicit; a capital letter adds no shift.
				parts[i] = strings.TrimPrefix(parts[i], "shift+")
			}
		}
		return [][]string{{"press", strings.Join(parts, "+")}}, nil
	case "text":
		var steps [][]string
		for _, c := range a.Text {
			if c > 0x7f || !strings.ContainsRune(typeable, c) {
				return nil, toolerr.New("conflict", "此引擎的坐标文本输入仅支持基础 ASCII；请使用 ref 输入", "reason", "text_requires_ref")
			}
			steps = append(steps, []string{"press", normalizeKey(string(c))})
		}
		return steps, nil
	}
	return nil, toolerr.Args("actions.type")
}
