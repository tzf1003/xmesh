//go:build !darwin && !linux && !windows

package computer

import (
	"context"
	"fmt"
	"net"
)

func DefaultEndpoint() string        { return "" }
func ValidEndpoint(string) bool      { return false }
func EndpointForState(string) string { return "" }
func dialDesktop(context.Context, string) (net.Conn, error) {
	return nil, fmt.Errorf("desktop unsupported on this platform")
}
