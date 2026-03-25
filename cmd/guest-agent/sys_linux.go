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

	syscall.Mount("proc", "/proc", "proc", 0, "")
	syscall.Mount("sysfs", "/sys", "sysfs", 0, "")
	syscall.Mount("devtmpfs", "/dev", "devtmpfs", 0, "")
	syscall.Mount("tmpfs", "/tmp", "tmpfs", 0, "")
}

func setHostname(name string) {
	if name != "" {
		syscall.Sethostname([]byte(name))
	}
}
