//go:build !windows

package winbox

import "fmt"

func RunProcessSupervisor([]string) error {
	return fmt.Errorf("AppContainer process supervision requires Windows")
}
