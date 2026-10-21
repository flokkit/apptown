package portainer

// StackName is the project stack AppTown makes on an intranet target,
// under the deployment's recorded host prefix.
func StackName(prefix, slug string) string { return prefix + slug }
