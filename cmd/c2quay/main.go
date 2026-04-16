package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Kaikei-e/c2quay/internal/cli"
)

func main() {
	os.Exit(run())
}

// run is separated from main so `defer cancel()` actually executes before we
// call os.Exit. gocritic flags defers-before-os.Exit in main, and rightly so.
func run() int {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	rc := cli.Execute(ctx)
	if rc != 0 {
		fmt.Fprintf(os.Stderr, "c2quay exited with code %d\n", rc)
	}
	return rc
}
