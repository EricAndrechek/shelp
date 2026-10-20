package bench

import (
	"os"
	"testing"

	"github.com/ericandrechek/shelp/internal/provider"
)

// TestMain turns on the structural guard against real backends (see
// provider/execx.go): nothing in this package may run the installed fm,
// shortcuts or claude. Fakes in a temp dir or testdata still work.
func TestMain(m *testing.M) {
	os.Setenv(provider.NoBackendsEnv, "1")
	os.Exit(m.Run())
}
