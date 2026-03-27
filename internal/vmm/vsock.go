package vmm

import (
	"bufio"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"

	"github.com/google/uuid"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/shared/pguuid"
)

const (
	// vsockPort is the port the guest agent connects to for config/logs.
	vsockPort = 1024
)

// ConfigResponse is the JSON config sent to the guest agent.
type ConfigResponse struct {
	Env      []string `json:"env"`
	IPAddr   string   `json:"ip_addr"`
	IPGW     string   `json:"ip_gw"`
	Hostname string   `json:"hostname"`
}

// vmState holds pre-loaded config for a VM's guest agent.
type vmState struct {
	vmID     uuid.UUID
	envVars  []string
	ipAddr   string
	ipGW     string
	hostname string
}

// VsockManager manages per-VM HTTP servers over vsock UDS sockets.
type VsockManager struct {
	mu        sync.Mutex
	q         *queries.Queries
	vms       map[uuid.UUID]*vmState
	listeners map[uuid.UUID]net.Listener
}

// NewVsockManager creates a new vsock manager.
func NewVsockManager(q *queries.Queries) *VsockManager {
	return &VsockManager{
		q:         q,
		vms:       make(map[uuid.UUID]*vmState),
		listeners: make(map[uuid.UUID]net.Listener),
	}
}

// RegisterVM sets up the UDS listener for a VM's guest agent communication.
// Must be called before starting the Cloud Hypervisor process.
func (m *VsockManager) RegisterVM(vmID uuid.UUID, envVars []string, ipAddr, ipGW, hostname string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	state := &vmState{
		vmID:     vmID,
		envVars:  envVars,
		ipAddr:   ipAddr,
		ipGW:     ipGW,
		hostname: hostname,
	}
	m.vms[vmID] = state

	// Cloud Hypervisor maps guest AF_VSOCK CID=2:<port> to host UDS at <socket>_<port>.
	udsPath := VsockGuestPath(vmID, vsockPort)
	os.Remove(udsPath) // remove stale socket

	lis, err := net.Listen("unix", udsPath)
	if err != nil {
		return err
	}
	m.listeners[vmID] = lis

	mux := http.NewServeMux()
	mux.HandleFunc("GET /config", m.handleConfig(state))
	mux.HandleFunc("POST /logs", m.handleLogs(state))

	srv := &http.Server{Handler: mux}
	go func() {
		if err := srv.Serve(lis); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Debug("vsock server ended", "vm_id", vmID, "error", err)
		}
	}()

	slog.Info("vsock registered", "vm_id", vmID, "uds", udsPath)
	return nil
}

// UnregisterVM stops the listener and cleans up state.
func (m *VsockManager) UnregisterVM(vmID uuid.UUID) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if lis, ok := m.listeners[vmID]; ok {
		lis.Close()
		delete(m.listeners, vmID)
	}
	delete(m.vms, vmID)

	os.Remove(VsockPath(vmID))
	os.Remove(VsockGuestPath(vmID, vsockPort))

	slog.Info("vsock unregistered", "vm_id", vmID)
}

// Stop cleans up all listeners.
func (m *VsockManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for vmID, lis := range m.listeners {
		lis.Close()
		os.Remove(VsockPath(vmID))
		os.Remove(VsockGuestPath(vmID, vsockPort))
	}
}

func (m *VsockManager) handleConfig(state *vmState) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slog.Info("served config via vsock", "vm_id", state.vmID, "env_count", len(state.envVars))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ConfigResponse{
			Env:      state.envVars,
			IPAddr:   state.ipAddr,
			IPGW:     state.ipGW,
			Hostname: state.hostname,
		})
	}
}

func (m *VsockManager) handleLogs(state *vmState) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slog.Info("log stream started", "vm_id", state.vmID)

		scanner := bufio.NewScanner(r.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		for scanner.Scan() {
			m.q.VMLogCreate(r.Context(), queries.VMLogCreateParams{
				ID:      pguuid.ToPgtype(uuid.New()),
				VmID:    pguuid.ToPgtype(state.vmID),
				Message: scanner.Text(),
				Level:   "info",
			})
		}

		if err := scanner.Err(); err != nil {
			slog.Debug("log stream ended with error", "vm_id", state.vmID, "error", err)
		}

		w.WriteHeader(http.StatusOK)
	}
}
