package release

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/Kaikei-e/c2quay/internal/config"
)

// RunSmoke executes the configured smoke command. Returns nil if the command
// is empty. TargetEnv is injected as TARGET_ENV unless the user already set it.
func RunSmoke(ctx context.Context, cfg config.SmokeConfig, targetEnv string, progress io.Writer) error {
	if cfg.Command == "" {
		return nil
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	subCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(subCtx, "sh", "-c", cfg.Command)
	cmd.Stdout = progress
	cmd.Stderr = progress
	cmd.Env = os.Environ()

	hasTarget := false
	for k, v := range cfg.Env {
		if k == "TARGET_ENV" {
			hasTarget = true
		}
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if !hasTarget {
		cmd.Env = append(cmd.Env, "TARGET_ENV="+targetEnv)
	}

	if err := cmd.Run(); err != nil {
		if subCtx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("smoke exceeded %s: %w", timeout, err)
		}
		return fmt.Errorf("smoke failed: %w", err)
	}
	return nil
}
