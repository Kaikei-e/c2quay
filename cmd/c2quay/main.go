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
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	rc := cli.Execute(ctx)
	if rc != 0 {
		fmt.Fprintf(os.Stderr, "c2quay exited with code %d\n", rc)
	}
	os.Exit(rc)
}
