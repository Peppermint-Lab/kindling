//go:build linux

package runtime

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// LinuxPIDResourceStats reads /proc for a Linux process (crun workloads on the host).
func LinuxPIDResourceStats(pid int) (ResourceStats, error) {
	if pid <= 0 {
		return ResourceStats{}, fmt.Errorf("invalid pid %d", pid)
	}
	// USER_HZ is 100 on essentially all Linux setups we target.
	const clk int64 = 100

	statPath := fmt.Sprintf("/proc/%d/stat", pid)
	statData, err := os.ReadFile(statPath)
	if err != nil {
		return ResourceStats{}, err
	}
	utime, stime, err := parseProcStatTimes(statData)
	if err != nil {
		return ResourceStats{}, err
	}
	cpuNanos := int64(float64(utime+stime) * float64(1e9) / float64(clk))

	statusData, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return ResourceStats{}, err
	}
	rss := parseVmRSSBytes(statusData)

	var readB, writeB int64
	if ioData, err := os.ReadFile(fmt.Sprintf("/proc/%d/io", pid)); err == nil {
		readB, writeB = parseProcIO(ioData)
	}

	return ResourceStats{
		CPUNanosCumulative: cpuNanos,
		MemoryRSSBytes:     rss,
		DiskReadBytes:      readB,
		DiskWriteBytes:     writeB,
		CollectedAt:        time.Now().UTC(),
	}, nil
}

func parseProcStatTimes(data []byte) (utime, stime uint64, err error) {
	i := bytes.LastIndexByte(data, ')')
	if i < 0 {
		return 0, 0, fmt.Errorf("parse stat: no comm")
	}
	rest := strings.Fields(string(data[i+2:])) // after ") "
	if len(rest) < 15 {
		return 0, 0, fmt.Errorf("parse stat: short")
	}
	utime, err = strconv.ParseUint(rest[11], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	stime, err = strconv.ParseUint(rest[12], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	return utime, stime, nil
}

func parseVmRSSBytes(status []byte) int64 {
	sc := bufio.NewScanner(bytes.NewReader(status))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "VmRSS:") {
			fs := strings.Fields(line)
			if len(fs) >= 2 {
				kb, err := strconv.ParseInt(fs[1], 10, 64)
				if err != nil {
					return 0
				}
				return kb * 1024
			}
		}
	}
	return 0
}

func parseProcIO(data []byte) (readB, writeB int64) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "read_bytes:") {
			fs := strings.Fields(line)
			if len(fs) >= 2 {
				readB, _ = strconv.ParseInt(fs[1], 10, 64)
			}
		}
		if strings.HasPrefix(line, "write_bytes:") {
			fs := strings.Fields(line)
			if len(fs) >= 2 {
				writeB, _ = strconv.ParseInt(fs[1], 10, 64)
			}
		}
	}
	return readB, writeB
}
