//go:build !linux

package runtime

func patchBundleHostNetwork(bundleDir string) error {
	return nil
}
