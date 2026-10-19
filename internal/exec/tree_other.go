//go:build !windows && !linux && !darwin

package exec

import "fmt"

func watchTree(int) (func() error, error) {
	return nil, fmt.Errorf("managed backend process trees are unsupported on this platform")
}
