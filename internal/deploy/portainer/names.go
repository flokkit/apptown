package portainer

// StackName is the project stack created by Forge on an intranet target.
func StackName(slug string) string { return "forge-" + slug }
