//go:build linux

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// guestStatsVsockPort must match internal/runtime.GuestStatsVsockPort.
const guestStatsVsockPort uint32 = 1026

type appRef struct {
	mu  sync.Mutex
	cmd *exec.Cmd
}

func (r *appRef) set(cmd *exec.Cmd) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cmd = cmd
}

func (r *appRef) pid() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd == nil || r.cmd.Process == nil {
		return 0
	}
	return r.cmd.Process.Pid
}

func startStatsServer(ref *appRef) {
	ln, err := listenVsock(guestStatsVsockPort)
	if err != nil {
		log.Printf("stats vsock listen: %v", err)
		return
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go handleStatsConn(c, ref)
		}
	}()
}

func handleStatsConn(c net.Conn, ref *appRef) {
	defer c.Close()
	buf := make([]byte, 512)
	n, _ := c.Read(buf)
	if n == 0 || !bytes.Contains(bytes.ToUpper(buf[:n]), []byte("GET /STATS")) {
		fmt.Fprintf(c, "HTTP/1.0 404 Not Found\r\nContent-Length: 0\r\n\r\n")
		return
	}
	st := collectGuestStats(ref.pid())
	b, err := json.Marshal(st)
	if err != nil {
		fmt.Fprintf(c, "HTTP/1.0 500\r\nContent-Length: 0\r\n\r\n")
		return
	}
	fmt.Fprintf(c, "HTTP/1.0 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(b), b)
}

type statsPayload struct {
	CPUNanosCumulative int64     `json:"cpu_nanos_cumulative"`
	MemoryRSSBytes     int64     `json:"memory_rss_bytes"`
	DiskReadBytes      int64     `json:"disk_read_bytes"`
	DiskWriteBytes     int64     `json:"disk_write_bytes"`
	CollectedAt        time.Time `json:"collected_at"`
}

func collectGuestStats(pid int) statsPayload {
	now := time.Now().UTC()
	if pid <= 0 {
		return statsPayload{CollectedAt: now}
	}
	// Linux USER_HZ is almost always 100; avoids cgo/x-sys quirks in some cross-builds.
	const clk int64 = 100
	statData, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return statsPayload{CollectedAt: now}
	}
	utime, stime, err := parseProcStat(statData)
	if err != nil {
		return statsPayload{CollectedAt: now}
	}
	cpuNanos := int64(float64(utime+stime) * float64(1e9) / float64(clk))

	statusData, _ := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	rss := parseRSS(statusData)

	var rb, wb int64
	if ioData, err := os.ReadFile(fmt.Sprintf("/proc/%d/io", pid)); err == nil {
		rb, wb = parseIO(ioData)
	}
	return statsPayload{
		CPUNanosCumulative: cpuNanos,
		MemoryRSSBytes:     rss,
		DiskReadBytes:      rb,
		DiskWriteBytes:     wb,
		CollectedAt:        now,
	}
}

func parseProcStat(data []byte) (utime, stime uint64, err error) {
	i := bytes.LastIndexByte(data, ')')
	if i < 0 {
		return 0, 0, fmt.Errorf("stat")
	}
	rest := strings.Fields(string(data[i+2:]))
	if len(rest) < 15 {
		return 0, 0, fmt.Errorf("short")
	}
	utime, err = strconv.ParseUint(rest[11], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	stime, err = strconv.ParseUint(rest[12], 10, 64)
	return utime, stime, err
}

func parseRSS(status []byte) int64 {
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

func parseIO(data []byte) (rb, wb int64) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "read_bytes:") {
			fs := strings.Fields(line)
			if len(fs) >= 2 {
				rb, _ = strconv.ParseInt(fs[1], 10, 64)
			}
		}
		if strings.HasPrefix(line, "write_bytes:") {
			fs := strings.Fields(line)
			if len(fs) >= 2 {
				wb, _ = strconv.ParseInt(fs[1], 10, 64)
			}
		}
	}
	return rb, wb
}
