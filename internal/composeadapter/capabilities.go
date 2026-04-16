package composeadapter

// AllServicesHealthy returns true when every container is either running
// (healthy or without a healthcheck) or has exited cleanly with code 0.
// Init containers that exit 0 are treated as success so that `docker compose
// up --wait` false-positives (docker/compose#10596) don't mark the deploy as
// failed when it actually succeeded.
func AllServicesHealthy(statuses []ContainerStatus) bool {
	if len(statuses) == 0 {
		return false
	}
	for _, s := range statuses {
		switch s.State {
		case "running":
			if s.Health != "" && s.Health != "healthy" && s.Health != "none" {
				return false
			}
		case "exited":
			if s.ExitCode != 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}
