//go:build !linux

package runtime

// LinuxPIDResourceStats is only available on Linux hosts.
func LinuxPIDResourceStats(pid int) (ResourceStats, error) {
	_ = pid
	return ResourceStats{}, ErrProcStatsUnsupported
}
