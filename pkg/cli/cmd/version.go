package cmd

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

var (
	// Set via ldflags
	version   = "dev"
	gitCommit = "unknown"
	buildDate = "unknown"
)

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("kedge version %s\n", version)
			fmt.Printf("  git commit: %s\n", gitCommit)
			fmt.Printf("  build date: %s\n", buildDate)
			fmt.Printf("  go version: %s\n", runtime.Version())
			fmt.Printf("  platform:   %s/%s\n", runtime.GOOS, runtime.GOARCH)
		},
	}
}
