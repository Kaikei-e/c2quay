package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// starterConfigYAML is written by `c2quay init`. It is a trimmed, commented
// adaptation of docs/config.example.yml — the full reference with every
// option explained lives there; this is meant to be edited immediately,
// not read as documentation.
const starterConfigYAML = `# c2quay starter configuration, written by ` + "`c2quay init`" + `.
# Every value below is a placeholder — edit it for your project before
# running ` + "`c2quay deploy`" + `. Full reference with every option explained:
# https://github.com/Kaikei-e/c2quay/blob/main/docs/config.example.yml

compose:
  files:
    - compose.yaml
  project_name: myapp

broker:
  base_url: https://pact-broker.example.com
  # Authentication MUST NOT be written here.
  # Use environment variables instead:
  #   PACT_BROKER_USERNAME / PACT_BROKER_PASSWORD  (Basic auth)
  #   PACT_BROKER_TOKEN                            (Bearer token)

versioning:
  strategy: manifest_file          # or: resolved_image_digest | git_sha
  options:
    path: .c2quay/versions.json

deploy:
  wait: true
  wait_timeout: 180s
  pull: never   # never (default) | always | missing — see ADR 0010
  # smoke:
  #   command: ./scripts/smoke.sh
  #   timeout: 30s

environments:
  production:
    all_or_nothing: false
    services:
      api:
        pacticipant: api
      # Add one entry per service whose contracts should gate this
      # environment's deploys. gate_only: true marks a mapped service that
      # is NOT declared in compose.yaml (e.g. it runs on a separate host) —
      # see ADR 0013 and docs/config.example.yml.
      # worker:
      #   pacticipant: worker
      #   gate_only: true
`

func newInitCommand(rt *runtimeCtx) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Scaffold a c2quay.yml in the current directory",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInit(rt, rt.flags.configPath, force)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing config file")
	return cmd
}

// runInit writes the starter config to path (defaulting to "c2quay.yml"
// when empty), refusing to overwrite an existing file unless force is set.
func runInit(rt *runtimeCtx, path string, force bool) error {
	if path == "" {
		path = "c2quay.yml"
	}
	if !force {
		if _, err := os.Stat(path); err == nil {
			return &ExitError{Code: ExitOperatorError, Err: fmt.Errorf(
				"%s already exists; refusing to overwrite it (pass --force to replace it)", path)}
		} else if !os.IsNotExist(err) {
			return &ExitError{Code: ExitOperatorError, Err: fmt.Errorf("check existing config: %w", err)}
		}
	}
	if err := os.WriteFile(path, []byte(starterConfigYAML), 0o644); err != nil { //nolint:gosec // config file is meant to be human-readable/editable.
		return &ExitError{Code: ExitOperatorError, Err: fmt.Errorf("write %s: %w", path, err)}
	}

	fmt.Fprintf(rt.stdout, "Wrote %s\n\n", path)
	fmt.Fprintln(rt.stdout, "Next steps:")
	fmt.Fprintln(rt.stdout, "  1. Edit broker.base_url and environments.<env>.services for your project.")
	fmt.Fprintln(rt.stdout, "  2. Set PACT_BROKER_TOKEN (or _USERNAME / _PASSWORD) in your environment.")
	fmt.Fprintln(rt.stdout, "  3. Run `c2quay doctor` to check local prerequisites (Docker, Compose version).")
	fmt.Fprintln(rt.stdout, "  4. Run `c2quay verify --env <env>` to test the gate without deploying anything.")
	return nil
}
