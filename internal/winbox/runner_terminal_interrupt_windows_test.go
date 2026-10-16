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
// foreground program stopped" from "the whole session was torn down".
func Test_WindowsManagedRunnerTerminalInterrupt(t *testing.T) {
	const sleeper = `Write-Output ('SLEEP_'+'READY'); Start-Sleep 300; Write-Output ('SLEEP_'+'FINISHED')`
	cases := []struct {
		name    string
		require string // executable that must be on the host PATH, if any
		start   string // typed into the AppContainer cmd shell
		ready   string // output proving the program started
		busy    string // typed into the program to leave it running a command
		running string // output proving that command runs
		stopped string // output proving the interrupt stopped the command
		probe   string // typed afterwards; the program must still answer it
		answer  string
		exit    string // typed to leave a program that survived the interrupt
	}{
		{name: "cmd", start: "cmd /k echo NES^TED-READY", ready: `NESTED-READY`, busy: "ping -t 127.0.0.1", running: `Pinging 127\.0\.0\.1`,
			stopped: `Ping statistics for 127\.0\.0\.1`, probe: "echo NES^TED-ALIVE", answer: `NESTED-ALIVE`, exit: "exit"},
		{name: "ping", start: "ping -t 127.0.0.1", ready: `Pinging 127\.0\.0\.1`, stopped: `Ping statistics for 127\.0\.0\.1`},
		{name: "powershell", start: "powershell -NoLogo -NoProfile -NoExit -Command Write-Output ('PS_'+'READY')", ready: `PS_READY`,
			busy: sleeper, running: `SLEEP_READY`, probe: "Write-Output ('INTERRUPT_'+'SURVIVED')", answer: `INTERRUPT_SURVIVED`, exit: "exit"},
		{name: "pwsh", require: "pwsh", start: "pwsh -NoLogo -NoProfile -NoExit -Command Write-Output ('PS_'+'READY')", ready: `PS_READY`,
			busy: sleeper, running: `SLEEP_READY`, probe: "Write-Output ('INTERRUPT_'+'SURVIVED')", answer: `INTERRUPT_SURVIVED`, exit: "exit"},
		{name: "loop", start: `helper.exe -test.run=^Test_WindowsManagedRunnerInterruptHelper$ -- --xmesh-interrupt-loop`, ready: `LOOP_READY`,
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
			write := func(data string) { o.call("pty.write", map[string]any{"session_id": o.session, "data": data + "\r\n"}) }
			write("echo REA^DY")
			o.until(`\r\nREADY`)
			write(c.start)
			o.until(c.ready)
			if c.busy != "" {
				write(c.busy)
				o.until(c.running)
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

// Test_WindowsManagedRunnerInterruptHelper is the interactive loop run inside the
// AppContainer: it survives Ctrl-C, reports it and keeps reading lines.
func Test_WindowsManagedRunnerInterruptHelper(t *testing.T) {
	var loop bool
	for _, arg := range os.Args {
		loop = loop || arg == "--xmesh-interrupt-loop"
	}
	if !loop {
		t.Skip("AppContainer interrupt helper")
	}
	interrupts := make(chan os.Signal, 8)
	signal.Notify(interrupts, os.Interrupt)
	lines := make(chan string)
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			lines <- strings.TrimSpace(scanner.Text())
		}
		close(lines)
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
				os.Exit(0)
			}
			fmt.Printf("LOOP_ECHO-%s\n", line)
		}
	}
}
