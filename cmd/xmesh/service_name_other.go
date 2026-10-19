//go:build !darwin

package main

func resolveUserServiceName(name string) (string, error) { return name, nil }
