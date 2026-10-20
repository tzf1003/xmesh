package main

import "testing"

func Test_TaskConsoleCodePage(t *testing.T) {
	// GBK output from schtasks on Chinese Windows with redirected stdout.
	raw := []byte(`<?xml version="1.0" encoding="UTF-16"?><Task><RegistrationInfo><Description>`)
	raw = append(raw, 0xd6, 0xd0, 0xce, 0xc4) // 中文
	raw = append(raw, []byte(`</Description></RegistrationInfo><Actions><Exec><Arguments>serve --state-dir C:\state</Arguments></Exec></Actions></Task>`)...)
	task, err := parseUserTask(taskConsoleCodePage(raw, 936))
	if err != nil || task.Description != "中文" || task.Actions.Exec.Arguments != `serve --state-dir C:\state` {
		t.Fatal(task, err)
	}
	for _, text := range []string{"", "ASCII", "UTF-8 中文"} {
		if got := taskConsoleCodePage([]byte(text), 936); got != text {
			t.Fatal(got, text)
		}
	}
}
