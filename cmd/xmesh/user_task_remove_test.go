package main

import (
	"encoding/binary"
	"testing"
	"unicode/utf16"
)

func Test_IsUserTaskOf(t *testing.T) {
	const mine, other = "S-1-5-21-1-2-3-1001", "S-1-5-21-1-2-3-1002"
	name := userTaskName("xmesh-office", mine)
	if !isUserTaskOf(name, mine) {
		t.Fatal("the user's own node task must match:", name)
	}
	if isUserTaskOf(name, other) {
		t.Fatal("another user's task must not match:", name)
	}
	for _, bad := range []string{`\xmesh-`, `\other-office-` + name[len(name)-13:], `xmesh-office` + name[len(name)-13:]} {
		if isUserTaskOf(bad, mine) {
			t.Fatal("unrelated task name matched:", bad)
		}
	}
}

func Test_ProgramUnder(t *testing.T) {
	install := `C:\Users\u\AppData\Local\Programs\xmesh`
	cases := []struct {
		program string
		want    bool
	}{
		{`C:\Users\u\AppData\Local\Programs\xmesh\resources\bin\xmesh.exe`, true},
		{`c:\users\U\appdata\local\programs\XMESH\resources\bin\xmesh.exe`, true},
		// A test node in a sibling folder shares the prefix but is not inside the install.
		{`C:\Users\u\AppData\Local\Programs\xmesh-test\xmesh.exe`, false},
		{`C:\Users\u\Desktop\xmesh.exe`, false},
		{install, false},
	}
	for _, c := range cases {
		if got := programUnder(c.program, install); got != c.want {
			t.Fatalf("programUnder(%q) = %t, want %t", c.program, got, c.want)
		}
	}
	if !programUnder(`C:\x\xmesh\xmesh.exe`, `C:\x\xmesh\`) {
		t.Fatal("a dir given with its trailing separator must work too")
	}
}

func Test_TaskDocumentRoundTrip(t *testing.T) {
	const doc = `<?xml version="1.0" encoding="UTF-16"?>` + "\r\n" +
		`<Task version="1.2"><Actions Context="CurrentUser"><Exec><Command>C:\Users\u\AppData\Local\Programs\xmesh\resources\bin\xmesh.exe</Command><Arguments>serve --service</Arguments></Exec></Actions></Task>`
	units := utf16.Encode([]rune(doc))
	withBOM := []byte{0xff, 0xfe}
	for _, u := range units {
		withBOM = binary.LittleEndian.AppendUint16(withBOM, u)
	}
	program, err := taskProgram(taskDocumentText(withBOM))
	if err != nil {
		t.Fatal(err)
	}
	if want := `C:\Users\u\AppData\Local\Programs\xmesh\resources\bin\xmesh.exe`; program != want {
		t.Fatal("program:", program)
	}
	// UTF-16 without a byte order mark (NUL bytes give it away) and plain text read the same way.
	var noBOM []byte
	for _, u := range units {
		noBOM = binary.LittleEndian.AppendUint16(noBOM, u)
	}
	if got, err := taskProgram(taskDocumentText(noBOM)); err != nil || got != program {
		t.Fatal("UTF-16 without BOM:", got, err)
	}
	if got, err := taskProgram(taskDocumentText([]byte(doc))); err != nil || got != program {
		t.Fatal("plain text:", got, err)
	}
}
