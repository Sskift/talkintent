package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/Sskift/talkintent/internal/cli"
)

var (
	// Version is injected during build or defaults to 1.0.0.
	Version = "1.0.0"
	// BuildTime is injected during build or defaults to the implementation date.
	BuildTime = "2026-09-20"
)

func main() {
	cli.Version = Version
	cli.BuildTime = BuildTime

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	exitCode := cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}
