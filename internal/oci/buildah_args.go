package oci

// BuildahBudArgs returns arguments for `buildah` (after the buildah binary name) to run a Dockerfile build from the current directory.
// When localLayerCache is true, intermediate layers are preserved (buildah --layers).
func BuildahBudArgs(imageRef, dockerfilePath string, localLayerCache bool) []string {
	args := []string{"bud"}
	if localLayerCache {
		args = append(args, "--layers")
	}
	args = append(args, "-t", imageRef, "-f", dockerfilePath, ".")
	return args
}

// BuildahPushArgs returns argv slice for buildah push (after the binary name): args..., imageRef, docker://imageRef.
// When authFilePath is non-empty, --authfile is used instead of passing credentials on the command line.
func BuildahPushArgs(imageRef string, authFilePath string) []string {
	args := []string{"push"}
	if authFilePath != "" {
		args = append(args, "--authfile", authFilePath)
	}
	args = append(args, imageRef, "docker://"+imageRef)
	return args
}
