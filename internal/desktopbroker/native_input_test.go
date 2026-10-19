package desktopbroker

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/computer"
)

type fixturePoint struct{ X, Y float64 }
type fixtureRectangle struct{ X, Y, Width, Height float64 }
type inputFixture struct {
	Click, Text, DragPoint, DragTo, Scroll fixturePoint
	Content, Display                       fixtureRectangle
	Cursor                                 fixturePoint
	Value                                  string
	Clicks                                 int
	DragComplete                           bool `json:"dragComplete"`
	ScrollTop                              int
	Keys                                   [][]any
	Wheels                                 [][]float64
}

func TestNativeInputFixture(t *testing.T) {
	helper, endpoint, token := os.Getenv("XMESH_TEST_COMPUTER_HELPER"), os.Getenv("XMESH_TEST_COMPUTER_FIXTURE"), os.Getenv("XMESH_TEST_COMPUTER_FIXTURE_TOKEN")
	if helper == "" || endpoint == "" {
		t.Skip("native input acceptance requires the isolated Electron fixture and native executor")
	}
	if !strings.HasPrefix(endpoint, "http://127.0.0.1:") || token == "" {
		t.Fatal("fixture must be an authenticated local test window")
	}
	read := func() inputFixture {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		if r.StatusCode != 200 {
			t.Fatal("fixture unavailable", r.StatusCode)
		}
		var v inputFixture
		if err = json.NewDecoder(r.Body).Decode(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	b, err := Start(Options{StateDir: t.TempDir(), HelperPath: helper})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	c := computer.NewClient(b.Endpoint)
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	var info computer.Info
	for n := 0; n < 30; n++ {
		info, err = c.Info(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(info.Capabilities) > 1 && (os.Getenv("XMESH_TEST_COMPUTER_MONITOR") != "1" || info.UserInputMonitor) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	m := computer.New(c, computer.Settings{Enabled: true, PauseOnUserInput: info.UserInputMonitor})
	defer m.Close()
	s, err := m.Open(ctx, "native-fixture/session", computer.OpenRequest{Mode: "control"})
	if err != nil {
		t.Fatal(err)
	}
	identity := computer.SessionRequest{SessionID: s.ID, Epoch: s.Epoch, Token: s.Token}
	interrupted := func() {
		t.Helper()
		latest, e := c.Info(ctx)
		if e == nil && info.UserInputMonitor && latest.UserInputSerial != info.UserInputSerial {
			if os.Getenv("CI") != "" {
				t.Fatal("desktop input fixture received external local input")
			}
			t.Skip("native input acceptance interrupted by local user activity; input authority was revoked")
		}
	}
	v := read()
	var display computer.Display
	for _, d := range info.Displays {
		if d.X == int(v.Display.X) && d.Y == int(v.Display.Y) {
			display = d
			break
		}
	}
	if display.ID == "" {
		t.Fatal("fixture display not found", info.Displays)
	}
	frame, err := m.Observe(ctx, "native-fixture/session", computer.ObserveRequest{SessionRequest: identity, CaptureRequest: computer.CaptureRequest{DisplayID: display.ID, MaxWidth: 1600}})
	if err != nil {
		interrupted()
		t.Fatal(err)
	}
	point := func(p fixturePoint) (*int, *int) {
		x := (v.Content.X + p.X - v.Display.X) * float64(frame.Width) / v.Display.Width
		y := (v.Content.Y + p.Y - v.Display.Y) * float64(frame.Height) / v.Display.Height
		ix, iy := int(x), int(y)
		return &ix, &iy
	}
	run := func(id string, actions ...computer.Action) {
		t.Helper()
		r, e := m.Act(ctx, "native-fixture/session", computer.ActRequest{SessionRequest: identity, ActionID: id, FrameID: frame.ID, Actions: actions})
		if e != nil || r.State != "executed" {
			interrupted()
			t.Fatal(id, r, e)
		}
	}
	x, y := point(v.Click)
	run("click", computer.Action{Type: "click", X: x, Y: y})
	x, y = point(v.Text)
	run("text", computer.Action{Type: "click", X: x, Y: y}, computer.Action{Type: "text", Text: "XmeSh 中文桌面"})
	modifier := "Control"
	if runtime.GOOS == "darwin" {
		modifier = "Meta"
	}
	run("key", computer.Action{Type: "key", Keys: []string{modifier, "a"}}, computer.Action{Type: "text", Text: "验证完成"})
	x, y = point(v.DragPoint)
	tx, ty := point(v.DragTo)
	run("drag", computer.Action{Type: "drag", X: x, Y: y, ToX: tx, ToY: ty, DurationMS: 300})
	x, y = point(v.Scroll)
	run("scroll", computer.Action{Type: "scroll", X: x, Y: y, DeltaY: 3})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got := read()
		if got.Clicks == 1 && got.Value == "验证完成" && got.DragComplete && got.ScrollTop > 0 {
			t.Log("native click, Chinese text, keyboard shortcut, drag and scroll verified in isolated window")
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	interrupted()
	t.Fatal("native input did not match fixture state", read())
}
