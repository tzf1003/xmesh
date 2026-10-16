//go:build !windows && !linux && !darwin

package pty

func sessionGroups(int) []int { return nil }
