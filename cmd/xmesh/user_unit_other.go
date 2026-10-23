//go:build !linux

package main

func userUnitExists(string) bool { return false }

func removeUserUnit(string) error { return nil }
