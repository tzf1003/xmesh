package winbox

import (
	"bufio"
	"fmt"
	"os"
	osexec "os/exec"
	"os/signal"
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
		probe   string // typed afterwards; the program must still answer it
		answer  string
		exit    string // typed to leave a program that survived the interrupt
	}{
		{name: "cmd", start: "cmd /k echo NES^TED-READY", ready: `NESTED-READY`, busy: helperCommand + "--xmesh-interrupt-sleep", running: `SLEEPER_RUNNING`,
			probe: "echo NES^TED-ALIVE", answer: `NESTED-ALIVE`, exit: "exit"},
		{name: "sleeper", start: helperCommand + "--xmesh-interrupt-sleep", ready: `SLEEPER_RUNNING`},
		{name: "powershell", start: "powershell" + quiet, ready: `PS_READY`,
			busy: sleeper, running: `SLEEP_READY`, probe: "Write-Output ('INTERRUPT_'+'SURVIVED')", answer: `INTERRUPT_SURVIVED`, exit: "exit"},
		{name: "pwsh", require: "pwsh", start: "pwsh" + quiet, ready: `PS_READY`,
			busy: sleeper, running: `SLEEP_READY`, probe: "Write-Output ('INTERRUPT_'+'SURVIVED')", answer: `INTERRUPT_SURVIVED`, exit: "exit"},
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
			o.until(`\r\nREADY`)
			write(c.start)
			o.until(c.ready)
			if c.busy != "" {
				write(c.busy)
				o.until(c.running)
				time.Sleep(500 * time.Millisecond)
			} else {
				time.Sleep(500 * time.Millisecond)
			}
			o.call("pty.signal", map[string]any{"session_id": o.session, "signal": "INT"})
			if c.stopped != "" {
				o.until(c.stopped)
			}
			if c.probe != "" {
				write(c.probe)
				o.until(c.answer)
			}
			if c.exit != "" {
				write(c.exit)
				// Input typed while the program is still leaving may be read ahead
				// by it; let the shell take the console back first.
				time.Sleep(time.Second)
			}
			write("echo SURV^IVED-" + c.name)
			o.until(`SURVIVED-` + c.name)
			write("exit")
			o.drain()
			waitRunnerIdle(t, f, 1)
			f.requireReclaimed(t, before, control, 1)
		})
	}
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
