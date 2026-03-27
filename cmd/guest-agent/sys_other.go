//go:build !linux

package main

func mountGuestBootstrap()    {}
func mountWorkloadVirtioApp() {}

func mountEssentialFS() {}

func setHostname(name string) {
	// no-op on non-Linux
}

func chrootIntoApp(cfg *ConfigResponse) {
	_ = cfg
	// no-op on non-Linux
}
