//go:build linux

package usage

import (
	"testing"
	"time"
)

func TestParseLinuxCPUTotals(t *testing.T) {
	t.Parallel()

	active, total, err := parseLinuxCPUTotals([]byte("cpu  100 20 30 400 50 0 10 5 0 0\ncpu0 1 2 3 4 5 6 7 8 9 10\n"))
	if err != nil {
		t.Fatalf("parseLinuxCPUTotals error = %v", err)
	}
	if active != 165 {
		t.Fatalf("active = %d, want 165", active)
	}
	if total != 615 {
		t.Fatalf("total = %d, want 615", total)
	}
}

func TestParseLinuxLoadAverages(t *testing.T) {
	t.Parallel()

	load1, load5, load15, err := parseLinuxLoadAverages([]byte("0.40 1.20 2.55 1/123 456\n"))
	if err != nil {
		t.Fatalf("parseLinuxLoadAverages error = %v", err)
	}
	if load1 != 0.40 || load5 != 1.20 || load15 != 2.55 {
		t.Fatalf("unexpected loads: %v %v %v", load1, load5, load15)
	}
}

func TestParseLinuxMemory(t *testing.T) {
	t.Parallel()

	total, available, err := parseLinuxMemory([]byte("MemTotal:       16384 kB\nMemAvailable:    4096 kB\n"))
	if err != nil {
		t.Fatalf("parseLinuxMemory error = %v", err)
	}
	if total != 16384*1024 {
		t.Fatalf("total = %d", total)
	}
	if available != 4096*1024 {
		t.Fatalf("available = %d", available)
	}
}

func TestParseLinuxDiskBytes(t *testing.T) {
	t.Parallel()

	readBytes, writeBytes, err := parseLinuxDiskBytes([]byte(
		"   8       0 sda 1 0 100 0 2 0 200 0 0 0 0 0 0 0 0 0 0\n" +
			" 259       0 nvme0n1 1 0 300 0 2 0 400 0 0 0 0 0 0 0 0 0 0\n" +
			" 259       1 nvme0n1p1 1 0 999 0 2 0 999 0 0 0 0 0 0 0 0 0 0\n",
	))
	if err != nil {
		t.Fatalf("parseLinuxDiskBytes error = %v", err)
	}
	if readBytes != (100+300)*linuxDiskSectorBytes {
		t.Fatalf("readBytes = %d", readBytes)
	}
	if writeBytes != (200+400)*linuxDiskSectorBytes {
		t.Fatalf("writeBytes = %d", writeBytes)
	}
}

func TestRateDerivationHelpers(t *testing.T) {
	t.Parallel()

	if got := deriveRatePercent(100, 200, 190, 300); got != 90 {
		t.Fatalf("deriveRatePercent = %v, want 90", got)
	}

	prev := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	now := prev.Add(2 * time.Second)
	if got := deriveBytesPerSecond(prev, now, 100, 500); got != 200 {
		t.Fatalf("deriveBytesPerSecond = %v, want 200", got)
	}
}

func TestIsWholeDiskDevice(t *testing.T) {
	t.Parallel()

	cases := map[string]bool{
		"sda":       true,
		"sda1":      false,
		"nvme0n1":   true,
		"nvme0n1p1": false,
		"mmcblk0":   true,
		"mmcblk0p1": false,
		"loop0":     false,
		"dm-0":      false,
		"xvda":      true,
		"xvda1":     false,
	}
	for name, want := range cases {
		if got := isWholeDiskDevice(name); got != want {
			t.Fatalf("isWholeDiskDevice(%q) = %v, want %v", name, got, want)
		}
	}
}
