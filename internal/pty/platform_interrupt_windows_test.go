package pty

import (
	"os/exec"
	"testing"
	"time"
)

// Signal("INT") is console input, so what stops depends on the foreground
// program. Each case interrupts a different one under the PowerShell session and
// requires the shell to answer afterwards.
func Test_WindowsPTYInterruptForegroundPrograms(t *testing.T) {
	for _, c := range []struct {
		name    string
		require string
	}{
		{name: "ping", require: "ping"},
		{name: "python", require: "python"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := exec.LookPath(c.require); err != nil {
				t.Skip(c.require, "is not installed")
			}
			term, output := nativeTerminal(t)
			switch c.name {
			case "ping":
				windowsWrite(t, term, "ping -t 127.0.0.1")
				output.until(t, `Pinging 127\.0\.0\.1`)
				time.Sleep(500 * time.Millisecond)
				interrupted := output.mark()
				if err := term.Signal("INT"); err != nil {
					t.Fatal(err)
				}
				output.until(t, `Ping statistics for 127\.0\.0\.1`)
				output.untilAfter(t, interrupted, `PS [^\r\n]*>`)
			case "python":
				windowsWrite(t, term, `python -q -i -c "print('PY_'+'READY')"`)
				output.until(t, `PY_READY`)
				windowsWrite(t, term, "import time")
				windowsWrite(t, term, "print('LOOP_'+'RUNNING'); time.sleep(300)")
				output.until(t, `LOOP_RUNNING`)
				time.Sleep(500 * time.Millisecond)
				if err := term.Signal("INT"); err != nil {
					t.Fatal(err)
				}
				output.until(t, `KeyboardInterrupt`)
				windowsWrite(t, term, "print('PY_'+'ALIVE')")
				output.until(t, `PY_ALIVE`)
				exited := output.mark()
				windowsWrite(t, term, "exit()")
				output.untilAfter(t, exited, `PS [^\r\n]*>`)
			}
			windowsWrite(t, term, "Write-Output ('SHELL_'+'BACK')")
			output.until(t, `SHELL_BACK`)
		})
	}
}
