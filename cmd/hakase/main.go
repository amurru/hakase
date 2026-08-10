// cmd/hakase is the skeleton entry point for the hakase binary backed by the
// internal/cli command dispatcher.
//
// Skeleton phase (plan task 3): all commands are registered as stubs. The
// root package main binary (`go run .`) remains the active entry point and
// keeps the real CLI implementations until the migration phase (plan tasks 12
// and 13) rewires this binary to the real handlers.
package main

import (
	"os"

	"amurru/hakase/internal/cli"
)

func main() {
	os.Exit(cli.Dispatch(os.Args[1:]))
}
