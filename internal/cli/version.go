package cli

import (
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// Version is set by the linker at release time via -ldflags.
var Version = "dev"

func newVersionCommand(rt *runtimeCtx) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print c2quay version",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintf(rt.stdout, "c2quay %s\n", resolveVersion())
			return nil
		},
	}
}

func resolveVersion() string {
	if Version != "dev" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if info.Main.Version != "" && info.Main.Version != "(devel)" {
			return info.Main.Version
		}
	}
	return Version
}
