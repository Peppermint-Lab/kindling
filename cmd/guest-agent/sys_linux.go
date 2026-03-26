package main

import (
	"log"
	"os"
	"syscall"
)

var defaultAppRootCandidates = []string{"/app", "/mnt/rootfs", "/mnt/rootfs/rootfs"}

func mountEssentialFS() {
	// Set PATH first so ip/sh are found.
	os.Setenv("PATH", "/bin:/sbin:/usr/bin:/usr/sbin:/usr/local/bin")

	os.MkdirAll("/proc", 0o755)
	os.MkdirAll("/sys", 0o755)
	os.MkdirAll("/dev", 0o755)
	os.MkdirAll("/tmp", 0o1777)
	os.MkdirAll("/app", 0o755)

	syscall.Mount("proc", "/proc", "proc", 0, "")
	syscall.Mount("sysfs", "/sys", "sysfs", 0, "")
	syscall.Mount("devtmpfs", "/dev", "devtmpfs", 0, "")
	syscall.Mount("tmpfs", "/tmp", "tmpfs", 0, "")

	// Try mounting shared app directory.
	// Apple VZ uses virtiofs, Cloud Hypervisor also uses virtiofs.
	// The tag "app" matches what the host configures.
	err := syscall.Mount("app", "/app", "virtiofs", 0, "")
	if err != nil {
		log.Printf("virtiofs mount failed: %v, trying 9p", err)
		// Fallback to 9p (some configurations use this).
		err = syscall.Mount("app", "/app", "9p", 0, "trans=virtio,version=9p2000.L")
		if err != nil {
			log.Printf("9p mount also failed: %v", err)
		}
	}

	// Log what's in /app for debugging.
	entries, err := os.ReadDir("/app")
	if err != nil {
		log.Printf("/app readdir: %v", err)
	} else {
		log.Printf("/app contains %d entries", len(entries))
		for _, e := range entries {
			log.Printf("  /app/%s", e.Name())
		}
	}
}

func setHostname(name string) {
	if name != "" {
		syscall.Sethostname([]byte(name))
	}
}

func chrootIntoApp() {
	if root := mountDiskRootfs(); root != "" {
		log.Printf("mounted block device rootfs at %s", root)
	}
	root := resolveAppRoot(defaultAppRootCandidates, pathExistsMap(defaultAppRootCandidates))
	if root == "" {
		return
	}
	log.Printf("chrooting into container rootfs at %s", root)
	if err := syscall.Chroot(root); err != nil {
		log.Printf("chroot failed: %v", err)
		return
	}
	os.Chdir("/")
	os.MkdirAll("/proc", 0o755)
	os.MkdirAll("/sys", 0o755)
	os.MkdirAll("/dev", 0o755)
	os.MkdirAll("/tmp", 0o1777)
	syscall.Mount("proc", "/proc", "proc", 0, "")
	syscall.Mount("sysfs", "/sys", "sysfs", 0, "")
	syscall.Mount("devtmpfs", "/dev", "devtmpfs", 0, "")
	syscall.Mount("tmpfs", "/tmp", "tmpfs", 0, "")
}

func mountDiskRootfs() string {
	const mountPoint = "/mnt/rootfs"
	os.MkdirAll("/mnt", 0o755)
	os.MkdirAll(mountPoint, 0o755)

	for _, dev := range []string{"/dev/vda", "/dev/vda1"} {
		if err := syscall.Mount(dev, mountPoint, "ext4", 0, ""); err != nil {
			continue
		}
		root := resolveAppRoot([]string{mountPoint, mountPoint + "/rootfs"}, pathExistsMap([]string{mountPoint, mountPoint + "/rootfs"}))
		if root != "" {
			return root
		}
		_ = syscall.Unmount(mountPoint, 0)
	}
	return ""
}

func pathExistsMap(candidates []string) map[string]bool {
	out := make(map[string]bool, len(candidates))
	for _, root := range candidates {
		if _, err := os.Stat(root + "/bin/sh"); err == nil {
			out[root+"/bin/sh"] = true
		}
	}
	return out
}

func resolveAppRoot(candidates []string, exists map[string]bool) string {
	for _, root := range candidates {
		if exists[root+"/bin/sh"] {
			return root
		}
	}
	return ""
}
