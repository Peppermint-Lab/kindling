package deploy

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/shared/pguuid"
)

func (d *Deployer) healthCheck(addr string, port int) bool {
	client := &http.Client{Timeout: healthCheckClientTimeout}
	url := addr
	if !strings.Contains(addr, ":") {
		url = fmt.Sprintf("%s:%d", addr, port)
	}
	if !strings.HasPrefix(url, "http") {
		url = "http://" + url
	}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 400
}

// healthCheckVMFromHost probes a VM row. Workloads on this kindling server
// bind a TCP port on the host; the DB stores the public/advertised IP, which
// many hosts cannot reach via hairpin NAT. Use loopback when the VM belongs
// to this server.
func (d *Deployer) healthCheckVMFromHost(vm queries.Vm, port int) bool {
	if vm.ServerID.Valid && pguuid.FromPgtype(vm.ServerID) == d.serverID {
		return d.healthCheck("127.0.0.1", port)
	}
	return d.healthCheck(vm.IpAddress.String(), port)
}

// healthCheckLocalForwarded checks a runtime URL returned by the local runtime
// (public IP + host port). The forwarder always listens on all interfaces, so
// loopback reaches the same port.
func (d *Deployer) healthCheckLocalForwarded(runtimeURL string) bool {
	_, port, err := parseRuntimeAddress(runtimeURL)
	if err != nil {
		return false
	}
	return d.healthCheck("127.0.0.1", port)
}

// waitHealthCheckLocalForwarded retries until the workload listens (OCI / VM
// publish can take tens of seconds after the parent process returns).
func (d *Deployer) waitHealthCheckLocalForwarded(runtimeURL string, maxWait time.Duration) bool {
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		if d.healthCheckLocalForwarded(runtimeURL) {
			return true
		}
		time.Sleep(healthCheckPollInterval)
	}
	return false
}

func requiresExternalHealthCheck(runtimeName string) bool {
	return runtimeName != "apple-vz"
}

func shouldKeepRunningVM(vm queries.Vm, runtimeName string, hostHealthCheckOK bool) bool {
	if vm.DeletedAt.Valid || vm.Status != "running" {
		return false
	}
	if requiresExternalHealthCheck(runtimeName) && !hostHealthCheckOK {
		return false
	}
	return true
}
