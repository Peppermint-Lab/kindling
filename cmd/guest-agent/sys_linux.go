package main

import (
	"os"
	"syscall"
)

func mountEssentialFS() {
	os.MkdirAll("/proc", 0o755)
	os.MkdirAll("/sys", 0o755)
	os.MkdirAll("/dev", 0o755)
	os.MkdirAll("/tmp", 0o1777)
	os.MkdirAll("/app", 0o755)

	syscall.Mount("proc", "/proc", "proc", 0, "")
	syscall.Mount("sysfs", "/sys", "sysfs", 0, "")
	syscall.Mount("devtmpfs", "/dev", "devtmpfs", 0, "")
	syscall.Mount("tmpfs", "/tmp", "tmpfs", 0, "")

	// Mount shared app directory (virtio-fs / 9p from host).
	syscall.Mount("app", "/app", "virtiofs", 0, "")

	// Ensure PATH includes busybox.
	os.Setenv("PATH", "/bin:/sbin:/usr/bin:/usr/sbin:/usr/local/bin")
}

func setHostname(name string) {
	if name != "" {
		syscall.Sethostname([]byte(name))
	}
}
