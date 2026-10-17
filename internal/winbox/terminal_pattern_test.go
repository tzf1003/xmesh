package winbox

import (
	"regexp"
	"testing"
)

// terminalReadyLine matches READY printed at the start of a line by `echo REA^DY` in a ConPTY
// shell. The renderer decides when to emit the window-title update (OSC) and cursor
// visibility switches (CSI), so they can sit between the line break and the output; a bare
// "\r\nREADY" then never matches (seen as a 60 s wait on a screen that did show READY). The
// title also carries the typed command, "echo  READY", so READY inside an OSC string must not
// count, and the line start has to be a line break or a cursor move to column 1.
const terminalReadyLine = `(?:\r\n|\x1b\[\d+;1H)(?:\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\))*READY`

func Test_TerminalReadyLinePattern(t *testing.T) {
	re := regexp.MustCompile(terminalReadyLine)
	for name, output := range map[string]string{
		"plain":            "echo REA^DY\r\nREADY\r\n",
		"title then ready": "echo REA^DY\r\n\x1b]0;Administrator: C:\\Windows\\system32\\cmd.exe - echo  READY\a\x1b[?25h\x1b[?25lREADY\x1b[8;1H",
		"cursor move":      "echo REA^DY\x1b[7;1H\x1b[?25lREADY",
		"st terminator":    "\r\n\x1b]0;title\x1b\\READY",
	} {
		if !re.MatchString(output) {
			t.Errorf("%s: no match in %q", name, output)
		}
	}
	for name, output := range map[string]string{
		"title only":      "echo REA^DY\r\n\x1b]0;Administrator: cmd.exe - echo  READY\a",
		"echoed command":  "echo READY\r\n",
		"mid line":        "echo REA^DY NOTREADY\r\n",
		"not at line end": "echo REA^DY\r\n\x1b[?25lREA",
	} {
		if re.MatchString(output) {
			t.Errorf("%s: unexpected match in %q", name, output)
		}
	}
}
