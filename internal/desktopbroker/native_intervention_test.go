package desktopbroker

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/computer"
)

func TestNativeUserIntervention(t *testing.T) {
	if (runtime.GOOS != "darwin" && runtime.GOOS != "windows") || os.Getenv("XMESH_TEST_COMPUTER_FIXTURE") == "" {
		t.Skip("native local input monitor acceptance requires an isolated supported desktop fixture")
	}
	helper := os.Getenv("XMESH_TEST_COMPUTER_HELPER")
	if helper == "" {
		t.Skip("native executor required")
	}
	b, err := Start(Options{StateDir: t.TempDir(), HelperPath: helper})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	c := computer.NewClient(b.Endpoint)
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		info, err := c.Info(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if info.UserInputMonitor {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("input monitor unavailable")
		case <-time.After(50 * time.Millisecond):
		}
	}
	m := computer.New(c, computer.Settings{Enabled: true, PauseOnUserInput: true})
	defer m.Close()
	s, err := m.Open(ctx, "native-intervention/session", computer.OpenRequest{Mode: "control"})
	if err != nil {
		t.Fatal(err)
	}
	// A different local process supplies a modifier press, carrying no Agent input
	// marker and no text. The event stays in the disposable acceptance window.
	program := `import ctypes,time
cg=ctypes.CDLL('/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics')
cf=ctypes.CDLL('/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation')
cg.CGEventCreateKeyboardEvent.argtypes=[ctypes.c_void_p,ctypes.c_uint16,ctypes.c_bool]
cg.CGEventCreateKeyboardEvent.restype=ctypes.c_void_p
cg.CGEventPost.argtypes=[ctypes.c_uint32,ctypes.c_void_p]
cf.CFRelease.argtypes=[ctypes.c_void_p]
for down in [True,False]:
 e=cg.CGEventCreateKeyboardEvent(None,56,down)
 if not e: raise RuntimeError('event unavailable')
 cg.CGEventPost(0,e);cf.CFRelease(e);time.sleep(0.02)
`
	python := "python3"
	if runtime.GOOS == "windows" {
		python = "python"
		program = `import ctypes
from ctypes import wintypes as w
class Mouse(ctypes.Structure):
 _fields_=[('dx',w.LONG),('dy',w.LONG),('data',w.DWORD),('flags',w.DWORD),('time',w.DWORD),('extra',ctypes.c_size_t)]
class Keyboard(ctypes.Structure):
 _fields_=[('vk',w.WORD),('scan',w.WORD),('flags',w.DWORD),('time',w.DWORD),('extra',ctypes.c_size_t)]
class Data(ctypes.Union):
 _fields_=[('mouse',Mouse),('key',Keyboard)]
class Input(ctypes.Structure):
 _fields_=[('type',w.DWORD),('data',Data)]
events=(Input*2)()
for event,flags in zip(events,[0,2]):
 event.type=1
 event.data.key=Keyboard(0xA0,0,flags,0,0)
send=ctypes.windll.user32.SendInput
send.argtypes=[w.UINT,ctypes.POINTER(Input),ctypes.c_int]
send.restype=w.UINT
if send(2,events,ctypes.sizeof(Input))!=2: raise RuntimeError('modifier input rejected')
`
	}
	if output, err := exec.CommandContext(ctx, python, "-c", program).CombinedOutput(); err != nil {
		t.Fatal("local modifier fixture", err, string(output))
	}
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		_, err = m.Info(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(m.Sessions(s.Owner)) == 0 {
			t.Log("local user input revoked native desktop control")
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("local modifier input retained Agent control")
}
