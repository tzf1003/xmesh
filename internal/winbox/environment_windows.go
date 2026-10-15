package winbox

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/windows"
)

// Environment supplies the current token's LocalAppData directory, which
// CreateProcess uses to resolve the AppContainer profile. Other entries come
// only from the caller's filtered environment. The child still needs explicit
// filesystem grants to access any directory named by an environment variable.
func Environment(env []string) ([]string, error) {
	local, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData, 0)
	if err != nil {
		return nil, fmt.Errorf("AppContainer LocalAppData: %w", err)
	}
	if !filepath.IsAbs(local) || strings.ContainsRune(local, 0) {
		return nil, fmt.Errorf("AppContainer LocalAppData is not an absolute directory")
	}
	result := make([]string, 0, len(env)+1)
	for _, entry := range env {
		name, _, found := strings.Cut(entry, "=")
		if !found || name == "" || strings.ContainsRune(entry, 0) {
			return nil, fmt.Errorf("invalid AppContainer environment entry")
		}
		if !strings.EqualFold(name, "LOCALAPPDATA") {
			result = append(result, entry)
		}
	}
	result = append(result, "LOCALAPPDATA="+local)
	sort.Slice(result, func(i, j int) bool { return strings.ToUpper(result[i]) < strings.ToUpper(result[j]) })
	return result, nil
}
