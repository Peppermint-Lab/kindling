package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"
)

type guestStatsJSON struct {
	CPUNanosCumulative int64     `json:"cpu_nanos_cumulative"`
	MemoryRSSBytes     int64     `json:"memory_rss_bytes"`
	DiskReadBytes      int64     `json:"disk_read_bytes"`
	DiskWriteBytes     int64     `json:"disk_write_bytes"`
	CollectedAt        time.Time `json:"collected_at"`
}

// resourceStatsFromGuestHTTP performs GET /stats over an already-connected guest vsock (or bridged) conn.
func resourceStatsFromGuestHTTP(ctx context.Context, c net.Conn) (ResourceStats, error) {
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(deadline)
	} else {
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	}
	if _, err := fmt.Fprintf(c, "GET /stats HTTP/1.0\r\nHost: localhost\r\n\r\n"); err != nil {
		return ResourceStats{}, err
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		return ResourceStats{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ResourceStats{}, fmt.Errorf("guest stats: HTTP %d", resp.StatusCode)
	}
	var j guestStatsJSON
	if err := json.NewDecoder(resp.Body).Decode(&j); err != nil {
		return ResourceStats{}, err
	}
	t := j.CollectedAt
	if t.IsZero() {
		t = time.Now().UTC()
	}
	return ResourceStats{
		CPUNanosCumulative: j.CPUNanosCumulative,
		MemoryRSSBytes:     j.MemoryRSSBytes,
		DiskReadBytes:      j.DiskReadBytes,
		DiskWriteBytes:     j.DiskWriteBytes,
		CollectedAt:        t.UTC(),
	}, nil
}
