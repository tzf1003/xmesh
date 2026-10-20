//go:build !windows

package main

func installedSystemService(string) (bool, error) { return false, nil }
