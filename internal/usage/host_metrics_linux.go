//go:build linux

package usage

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const linuxDiskSectorBytes = 512

type hostMetricsCollectorState struct {
	collectedAt    time.Time
	cpuActiveTicks uint64
	cpuTotalTicks  uint64
	diskReadBytes  uint64
	diskWriteBytes uint64
}

func HostMetricsSupported() bool { return true }

func collectHostMetrics(prev *hostMetricsCollectorState, stateDir string) (HostMetricsSample, hostMetricsCollectorState, error) {
	now := time.Now().UTC()

	activeTicks, totalTicks, err := readLinuxCPUTotals()
	if err != nil {
		return HostMetricsSample{}, hostMetricsCollectorState{}, err
	}
	load1, load5, load15, err := readLinuxLoadAverages()
	if err != nil {
		return HostMetricsSample{}, hostMetricsCollectorState{}, err
	}
	memTotal, memAvailable, err := readLinuxMemory()
	if err != nil {
		return HostMetricsSample{}, hostMetricsCollectorState{}, err
	}
	rootTotal, rootFree, rootUsed, err := readFilesystemUsage("/")
	if err != nil {
		return HostMetricsSample{}, hostMetricsCollectorState{}, err
	}
	readBytes, writeBytes, err := readLinuxDiskBytes()
	if err != nil {
		return HostMetricsSample{}, hostMetricsCollectorState{}, err
	}

	sample := HostMetricsSample{
		SampledAt:            now,
		LoadAvg1m:            load1,
		LoadAvg5m:            load5,
		LoadAvg15m:           load15,
		MemoryTotalBytes:     memTotal,
		MemoryAvailableBytes: memAvailable,
		MemoryUsedBytes:      maxInt64(memTotal-memAvailable, 0),
		DiskTotalBytes:       rootTotal,
		DiskFreeBytes:        rootFree,
		DiskUsedBytes:        rootUsed,
	}

	next := hostMetricsCollectorState{
		collectedAt:    now,
		cpuActiveTicks: activeTicks,
		cpuTotalTicks:  totalTicks,
		diskReadBytes:  readBytes,
		diskWriteBytes: writeBytes,
	}
	if prev != nil {
		sample.CPUPercent = deriveRatePercent(prev.cpuActiveTicks, prev.cpuTotalTicks, activeTicks, totalTicks)
		sample.DiskReadBytesPerSec = deriveBytesPerSecond(prev.collectedAt, now, prev.diskReadBytes, readBytes)
		sample.DiskWriteBytesPerSec = deriveBytesPerSecond(prev.collectedAt, now, prev.diskWriteBytes, writeBytes)
	}

	if path, sameDevice, err := resolveStateDiskPath(stateDir, "/"); err == nil && path != "" && !sameDevice {
		stateTotal, stateFree, stateUsed, statErr := readFilesystemUsage(path)
		if statErr == nil {
			sample.StateDiskPath = path
			sample.StateDiskTotalBytes = stateTotal
			sample.StateDiskFreeBytes = stateFree
			sample.StateDiskUsedBytes = stateUsed
		}
	}

	return sample, next, nil
}

func deriveRatePercent(prevActive, prevTotal, active, total uint64) float64 {
	if total <= prevTotal {
		return 0
	}
	totalDelta := total - prevTotal
	activeDelta := uint64(0)
	if active > prevActive {
		activeDelta = active - prevActive
	}
	return float64(activeDelta) / float64(totalDelta) * 100
}

func deriveBytesPerSecond(prevAt, now time.Time, prevBytes, bytes uint64) float64 {
	if now.Before(prevAt) || !now.After(prevAt) || bytes < prevBytes {
		return 0
	}
	return float64(bytes-prevBytes) / now.Sub(prevAt).Seconds()
}

func readLinuxCPUTotals() (active, total uint64, err error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, err
	}
	return parseLinuxCPUTotals(data)
}

func parseLinuxCPUTotals(data []byte) (active, total uint64, err error) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 8 {
			return 0, 0, fmt.Errorf("parse /proc/stat: short cpu line")
		}
		values := make([]uint64, 0, len(fields)-1)
		for _, field := range fields[1:] {
			v, parseErr := strconv.ParseUint(field, 10, 64)
			if parseErr != nil {
				return 0, 0, parseErr
			}
			values = append(values, v)
			total += v
		}
		idle := values[3]
		iowait := uint64(0)
		if len(values) > 4 {
			iowait = values[4]
		}
		active = total - idle - iowait
		return active, total, nil
	}
	if err := sc.Err(); err != nil {
		return 0, 0, err
	}
	return 0, 0, fmt.Errorf("parse /proc/stat: cpu line not found")
}

func readLinuxLoadAverages() (float64, float64, float64, error) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0, err
	}
	return parseLinuxLoadAverages(data)
}

func parseLinuxLoadAverages(data []byte) (float64, float64, float64, error) {
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return 0, 0, 0, fmt.Errorf("parse /proc/loadavg: short")
	}
	load1, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, 0, 0, err
	}
	load5, err := strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return 0, 0, 0, err
	}
	load15, err := strconv.ParseFloat(fields[2], 64)
	if err != nil {
		return 0, 0, 0, err
	}
	return load1, load5, load15, nil
}

func readLinuxMemory() (total, available int64, err error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, err
	}
	return parseLinuxMemory(data)
}

func parseLinuxMemory(data []byte) (total, available int64, err error) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "MemTotal:"):
			total = parseMemInfoLine(line)
		case strings.HasPrefix(line, "MemAvailable:"):
			available = parseMemInfoLine(line)
		}
	}
	if err := sc.Err(); err != nil {
		return 0, 0, err
	}
	if total == 0 {
		return 0, 0, fmt.Errorf("parse /proc/meminfo: MemTotal missing")
	}
	if available == 0 {
		available = total
	}
	return total, available, nil
}

func parseMemInfoLine(line string) int64 {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0
	}
	v, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return v * 1024
}

func readFilesystemUsage(path string) (total, free, used int64, err error) {
	resolved, err := nearestExistingPath(path)
	if err != nil {
		return 0, 0, 0, err
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(resolved, &stat); err != nil {
		return 0, 0, 0, err
	}
	total = int64(stat.Blocks) * int64(stat.Bsize)
	free = int64(stat.Bavail) * int64(stat.Bsize)
	used = maxInt64(total-free, 0)
	return total, free, used, nil
}

func resolveStateDiskPath(stateDir, rootPath string) (string, bool, error) {
	stateDir = strings.TrimSpace(stateDir)
	if stateDir == "" {
		return "", false, nil
	}
	statePath, err := nearestExistingPath(stateDir)
	if err != nil {
		return "", false, err
	}
	rootExisting, err := nearestExistingPath(rootPath)
	if err != nil {
		return "", false, err
	}
	same, err := sameDevice(statePath, rootExisting)
	if err != nil {
		return "", false, err
	}
	return statePath, same, nil
}

func nearestExistingPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("empty path")
	}
	path = filepath.Clean(path)
	for {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
		parent := filepath.Dir(path)
		if parent == path {
			break
		}
		path = parent
	}
	return "", fmt.Errorf("path not found")
}

func sameDevice(a, b string) (bool, error) {
	aInfo, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	bInfo, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	aStat, ok := aInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("stat %s: unexpected type", a)
	}
	bStat, ok := bInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("stat %s: unexpected type", b)
	}
	return aStat.Dev == bStat.Dev, nil
}

func readLinuxDiskBytes() (readBytes, writeBytes uint64, err error) {
	data, err := os.ReadFile("/proc/diskstats")
	if err != nil {
		return 0, 0, err
	}
	return parseLinuxDiskBytes(data)
}

func parseLinuxDiskBytes(data []byte) (readBytes, writeBytes uint64, err error) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 10 {
			continue
		}
		name := fields[2]
		if !isWholeDiskDevice(name) {
			continue
		}
		readSectors, parseErr := strconv.ParseUint(fields[5], 10, 64)
		if parseErr != nil {
			return 0, 0, parseErr
		}
		writeSectors, parseErr := strconv.ParseUint(fields[9], 10, 64)
		if parseErr != nil {
			return 0, 0, parseErr
		}
		readBytes += readSectors * linuxDiskSectorBytes
		writeBytes += writeSectors * linuxDiskSectorBytes
	}
	if err := sc.Err(); err != nil {
		return 0, 0, err
	}
	return readBytes, writeBytes, nil
}

func isWholeDiskDevice(name string) bool {
	switch {
	case strings.HasPrefix(name, "loop"),
		strings.HasPrefix(name, "ram"),
		strings.HasPrefix(name, "zram"),
		strings.HasPrefix(name, "dm-"):
		return false
	case strings.HasPrefix(name, "nvme"):
		return strings.Count(name, "p") == 0
	case strings.HasPrefix(name, "mmcblk"):
		return strings.Count(name, "p") == 0
	case strings.HasPrefix(name, "md"):
		return true
	case strings.HasPrefix(name, "sd"),
		strings.HasPrefix(name, "vd"),
		strings.HasPrefix(name, "xvd"),
		strings.HasPrefix(name, "hd"):
		last := name[len(name)-1]
		return last < '0' || last > '9'
	default:
		return false
	}
}

func maxInt64(v, min int64) int64 {
	if v < min {
		return min
	}
	return v
}
