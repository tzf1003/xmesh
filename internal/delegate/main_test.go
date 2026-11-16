package delegate

import (
	"os"
	"testing"

	"github.com/tzf1003/xmesh/internal/delegate/delegatetest"
)

// TestMain turns the test binary into the fake runner when the arguments select it, before the test framework starts.
func TestMain(m *testing.M) {
	delegatetest.Serve(os.Args)
	os.Exit(m.Run())
}
