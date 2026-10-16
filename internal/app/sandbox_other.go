//go:build !windows

package app

import "context"

func platformL2Runner(context.Context, string) (l2Runner, error) { return nil, errL2Unsupported }
