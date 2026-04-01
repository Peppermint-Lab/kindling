//go:build !linux

package usage

import "fmt"

type hostMetricsCollectorState struct{}

func HostMetricsSupported() bool { return false }

func collectHostMetrics(_ *hostMetricsCollectorState, _ string) (HostMetricsSample, hostMetricsCollectorState, error) {
	return HostMetricsSample{}, hostMetricsCollectorState{}, fmt.Errorf("host metrics unsupported on this platform")
}
