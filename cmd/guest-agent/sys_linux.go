package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

var defaultAppRootCandidates = []string{"/app", "/mnt/rootfs", "/mnt/rootfs/rootfs"}

// mountGuestBootstrap mounts proc/sys/dev/tmp so vsock and HTTP to the host work before workload/builder-specific mounts.
func mountGuestBootstrap() {
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
}

// mountWorkloadVirtioApp mounts the virtiofs "app" share used by normal deployments.
func mountWorkloadVirtioApp() {
	err := syscall.Mount("app", "/app", "virtiofs", 0, "")
	if err != nil {
		log.Printf("virtiofs mount failed: %v, trying 9p", err)
		err = syscall.Mount("app", "/app", "9p", 0, "trans=virtio,version=9p2000.L")
		if err != nil {
			log.Printf("9p mount also failed: %v", err)
		}
	}

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

func mountEssentialFS() {
	mountGuestBootstrap()
	mountWorkloadVirtioApp()
}

func setHostname(name string) {
	if name != "" {
		syscall.Sethostname([]byte(name))
	}
}

func chrootIntoApp(cfg *ConfigResponse) {
	if root := mountDiskRootfs(); root != "" {
		log.Printf("mounted block device rootfs at %s", root)
		if cfg != nil {
			mountPersistentVolume(root, cfg.VolumeMountPath)
		}
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

func mountPersistentVolume(root, mountPath string) {
	mountPath = strings.TrimSpace(mountPath)
	if mountPath == "" {
		return
	}
	if !strings.HasPrefix(mountPath, "/") {
		log.Printf("persistent volume mount path must be absolute: %q", mountPath)
		return
	}
	clean := filepath.Clean(mountPath)
	if clean == "/" {
		log.Printf("persistent volume mount path cannot be root")
		return
	}
	target := filepath.Join(root, strings.TrimPrefix(clean, "/"))
	if err := os.MkdirAll(target, 0o755); err != nil {
		log.Printf("create persistent volume mountpoint %s: %v", target, err)
		return
	}
	for _, dev := range []string{"/dev/vdb", "/dev/vdb1"} {
		if err := syscall.Mount(dev, target, "ext4", 0, ""); err == nil {
			log.Printf("mounted persistent volume %s at %s", dev, target)
			if err := growPersistentVolumeFilesystem(dev); err != nil {
				log.Printf("persistent volume grow skipped for %s: %v", dev, err)
			}
			return
		}
	}
	log.Printf("persistent volume mount failed for %s", clean)
}

func growPersistentVolumeFilesystem(device string) error {
	device = strings.TrimSpace(device)
	if device == "" {
		return nil
	}
	if parent, partition, ok := splitBlockDevicePartition(device); ok {
		if out, err := exec.Command("growpart", parent, partition).CombinedOutput(); err != nil {
			log.Printf("growpart %s %s failed: %s (%v)", parent, partition, strings.TrimSpace(string(out)), err)
		}
	}
	if out, err := exec.Command("resize2fs", device).CombinedOutput(); err != nil {
		return fmt.Errorf("resize2fs %s: %s: %w", device, strings.TrimSpace(string(out)), err)
	}
	return nil
}

func splitBlockDevicePartition(device string) (string, string, bool) {
	device = strings.TrimSpace(device)
	if device == "" {
		return "", "", false
	}
	cut := len(device)
	for cut > 0 {
		ch := device[cut-1]
		if ch < '0' || ch > '9' {
			break
		}
		cut--
	}
	if cut == len(device) {
		return "", "", false
	}
	parent := device[:cut]
	if strings.HasSuffix(parent, "p") {
		parent = strings.TrimSuffix(parent, "p")
	}
	if parent == "" {
		return "", "", false
	}
	return parent, device[cut:], true
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
