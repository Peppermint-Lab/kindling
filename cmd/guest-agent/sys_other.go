//go:build !linux

package main

func mountEssentialFS() {
	// no-op on non-Linux
}

func setHostname(name string) {
	// no-op on non-Linux
}

func chrootIntoApp() {
	// no-op on non-Linux
}
