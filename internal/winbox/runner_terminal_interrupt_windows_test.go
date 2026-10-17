package winbox

import (
	"bufio"
	"fmt"
	"os"
	osexec "os/exec"
	"os/signal"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Signal("INT") reaches a managed terminal as Ctrl-C console input. Each case
// runs a different foreground program inside the AppContainer shell, interrupts
// it and then requires the shell to answer a fresh command, which separates "the
// foreground program stopped" from "the whole session was torn down". ping.exe
// cannot be used here: an AppContainer has no IP driver access, so ping is
// covered under the host ConPTY in internal/pty.
func Test_WindowsManagedRunnerTerminalInterrupt(t *testing.T) {
	const (
		helperCommand = `helper.exe -test.run=^Test_WindowsManagedRunnerInterruptHelper$ -- `
		sleeper       = `Write-Output ('SLEEP_'+'READY'); Start-Sleep 300; Write-Output ('SLEEP_'+'FINISHED')`
		quiet         = ` -NoLogo -NoProfile -NoExit -Command "Set-PSReadLineOption -HistorySaveStyle SaveNothing; Write-Output ('PS_'+'READY')"`
	)
	cases := []struct {
		name    string
		require string // executable that must be on the host PATH, if any
		start   string // typed into the AppContainer cmd shell
		ready   string // output proving the program started
		busy    string // typed into the program to leave it running a command
		running string // output proving that command runs
		stopped string // output proving the interrupt stopped the program
		prompt  string // prompt the program shows again once the interrupt is handled
		probe   string // typed afterwards; the program must still answer it
		answer  string
		exit    string // typed to leave a program that survived the interrupt
	}{
		{name: "cmd", start: "cmd /k echo NES^TED-READY", ready: `NESTED-READY`, busy: helperCommand + "--xmesh-interrupt-sleep", running: `SLEEPER_RUNNING`,
			probe: "echo NES^TED-ALIVE", answer: `NESTED-ALIVE`, exit: "exit"},
		{name: "sleeper", start: helperCommand + "--xmesh-interrupt-sleep", ready: `SLEEPER_RUNNING`},
		{name: "powershell", start: "powershell" + quiet, ready: `PS_READY`,
			busy: sleeper, running: `SLEEP_READY`, prompt: `PS [^\r\n]*>`, probe: "Write-Output ('INTERRUPT_'+'SURVIVED')", answer: `INTERRUPT_SURVIVED`, exit: "exit"},
		{name: "pwsh", require: "pwsh", start: "pwsh" + quiet, ready: `PS_READY`,
			busy: sleeper, running: `SLEEP_READY`, prompt: `PS [^\r\n]*>`, probe: "Write-Output ('INTERRUPT_'+'SURVIVED')", answer: `INTERRUPT_SURVIVED`, exit: "exit"},
		{name: "loop", start: helperCommand + "--xmesh-interrupt-loop", ready: `LOOP_READY`,
			stopped: `LOOP_INTERRUPTED-1`, probe: "first", answer: `LOOP_ECHO-first`, exit: "quit"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.require != "" {
				if _, err := osexec.LookPath(c.require); err != nil {
					t.Skip(c.require, "is not installed")
				}
			}
			f := newRunnerFixture(t)
			before, control := kernelJournalState(t, f.root)
			m, reg := newTerminalManager(t)
			o := f.openTerminal(t, m, reg, "interrupt-"+c.name)
			// Enter is a lone CR; a following LF would stay in the input queue and
			// reach a program started from the shell as a stray line feed.
			write := func(data string) { o.call("pty.write", map[string]any{"session_id": o.session, "data": data + "\r"}) }
			write("echo REA^DY")
			o.until(terminalReadyLine)
			write(c.start)
			o.until(c.ready)
			if c.busy != "" {
				write(c.busy)
				o.until(c.running)
				time.Sleep(500 * time.Millisecond)
			} else {
				time.Sleep(500 * time.Millisecond)
			}
			// Console programs flush pending input when Ctrl-C arrives, so nothing
			// further is typed until the interrupt has visibly been handled.
			interrupted := o.text.Len()
			o.call("pty.signal", map[string]any{"session_id": o.session, "signal": "INT"})
			if c.stopped != "" {
				o.until(c.stopped)
			}
			if c.prompt != "" {
				o.untilAfter(interrupted, c.prompt)
			}
			if c.probe != "" {
				write(c.probe)
				o.until(c.answer)
			}
			if c.exit != "" {
				// Input typed while the program is still leaving may be read ahead
				// by it; wait for the shell's own prompt before typing again.
				time.Sleep(500 * time.Millisecond)
				leaving := o.text.Len()
				write(c.exit)
				o.untilAfter(leaving, `workspace>`)
			}
			write("echo SURV^IVED-" + c.name)
			o.until(`SURVIVED-` + c.name)
			// The reply is printed before the prompt is drawn; typing "exit" into that redraw has
			// left a session that never ended.
			survived := o.text.Len()
			o.untilAfter(survived-len(`SURVIVED-`+c.name), `SURVIVED-`+c.name+`[^>]*workspace>`)
			write("exit")
			o.drain()
			waitRunnerIdle(t, f, 1)
			f.requireReclaimed(t, before, control, 1)
		})
	}
}

// untilAfter waits for expression in the output that arrived after offset.
func (o *ptyOutput) untilAfter(offset int, expression string) {
	o.t.Helper()
	re := regexp.MustCompile(expression)
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
		if re.MatchString(o.text.String()[offset:]) {
			return
		}
		if o.eof {
			break
		}
		result := o.call("pty.read", map[string]any{"session_id": o.session, "cursor": o.cursor, "wait_ms": 100})
		o.text.WriteString(result["data"].(string))
		o.cursor = result["next_cursor"].(uint64)
		o.eof = result["eof"] == true
	}
	o.t.Fatalf("terminal output did not match %q after the interrupt (eof=%v): %q", expression, o.eof, o.text.String()[offset:])
}

// Test_WindowsManagedRunnerInterruptHelper runs inside the AppContainer. With
// --xmesh-interrupt-loop it survives Ctrl-C, reports it and keeps reading lines;
// with --xmesh-interrupt-sleep it only waits, so Ctrl-C ends it as for any
// program that does not handle the signal.
func Test_WindowsManagedRunnerInterruptHelper(t *testing.T) {
	var loop, sleep bool
	for _, arg := range os.Args {
		loop = loop || arg == "--xmesh-interrupt-loop"
		sleep = sleep || arg == "--xmesh-interrupt-sleep"
	}
	if sleep {
		fmt.Println("SLEEPER_RUNNING")
		time.Sleep(10 * time.Minute)
		os.Exit(0)
	}
	if !loop {
		t.Skip("AppContainer interrupt helper")
	}
	interrupts := make(chan os.Signal, 8)
	signal.Notify(interrupts, os.Interrupt)
	lines := make(chan string)
	go func() {
		// Ctrl-C aborts a pending console read, which surfaces as a read error.
		// Only a lasting end of input stops the loop.
		reader := bufio.NewReader(os.Stdin)
		failures := 0
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				if failures++; failures > 100 {
					close(lines)
					return
				}
				time.Sleep(20 * time.Millisecond)
				continue
			}
			failures = 0
			lines <- strings.TrimSpace(line)
		}
	}()
	fmt.Println("LOOP_READY")
	count := 0
	for {
		select {
		case <-interrupts:
			count++
			fmt.Printf("LOOP_INTERRUPTED-%d\n", count)
		case line, ok := <-lines:
			if !ok || line == "quit" {
				fmt.Println("LOOP_DONE")
				os.Exit(0)
			}
			fmt.Printf("LOOP_ECHO-%s\n", line)
		}
	}
}
