//go:build !linux

package runtime

func ensureBundleCrunIsolation(bundleDir string) error {
	_ = bundleDir
	return nil
}
