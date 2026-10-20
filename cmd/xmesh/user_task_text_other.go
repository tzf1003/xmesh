//go:build !windows

package main

func taskConsoleText(raw []byte) string { return string(raw) }
