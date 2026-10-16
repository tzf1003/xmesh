//go:build !windows

package winbox

import "fmt"

func RunGuardKeeper([]string) error {
	return fmt.Errorf("AppContainer guard keeping requires Windows")
}
