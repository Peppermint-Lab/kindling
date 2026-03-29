package macd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
)

// Server is the local Unix socket API server for kindling-mac.
type Server struct {
	mgr *Manager
	ln  net.Listener
}

// NewServer creates a server that will serve the kindling-mac API on the given socket path.
func NewServer(mgr *Manager, socketPath string) (*Server, error) {
	socketPath = os.ExpandEnv(socketPath)
	if err := os.RemoveAll(socketPath); err != nil {
		return nil, fmt.Errorf("remove stale socket: %w", err)
	}

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("listen unix %s: %w", socketPath, err)
	}

	if err := os.Chmod(socketPath, 0700); err != nil {
		ln.Close()
		return nil, fmt.Errorf("chmod socket: %w", err)
	}

	return &Server{mgr: mgr, ln: ln}, nil
}

// Serve runs the HTTP server until ctx is cancelled.
func (s *Server) Serve(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/vm.list", withVMManager(s.mgr, s.handleVMList))
	mux.HandleFunc("/vm.get", withVMManager(s.mgr, s.handleVMGet))
	mux.HandleFunc("/vm.start", withVMManager(s.mgr, s.handleVMStart))
	mux.HandleFunc("/vm.stop", withVMManager(s.mgr, s.handleVMStop))
	mux.HandleFunc("/vm.delete", withVMManager(s.mgr, s.handleVMDelete))
	mux.HandleFunc("/vm.shell", withVMManager(s.mgr, s.handleVMShell))
	mux.HandleFunc("/vm.exec", withVMManager(s.mgr, s.handleVMExec))
	mux.HandleFunc("/box.start", withVMManager(s.mgr, s.handleBoxStart))
	mux.HandleFunc("/box.stop", withVMManager(s.mgr, s.handleBoxStop))
	mux.HandleFunc("/box.status", withVMManager(s.mgr, s.handleBoxStatus))
	mux.HandleFunc("/temp.create", withVMManager(s.mgr, s.handleTempCreate))
	mux.HandleFunc("/temp.list", withVMManager(s.mgr, s.handleTempList))
	mux.HandleFunc("/temp.delete", withVMManager(s.mgr, s.handleTempDelete))
	mux.HandleFunc("/template.list", withVMManager(s.mgr, s.handleTemplateList))
	mux.HandleFunc("/template.capture", withVMManager(s.mgr, s.handleTemplateCapture))
	mux.HandleFunc("/template.delete", withVMManager(s.mgr, s.handleTemplateDelete))

	srv := &http.Server{Handler: mux}
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(s.ln)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return srv.Shutdown(context.Background())
	}
}

type contextKey string

const vmManagerKey contextKey = "vmManager"

func withVMManager(mgr *Manager, fn func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), vmManagerKey, mgr)
		fn(w, r.WithContext(ctx))
	}
}

func vmManagerFrom(ctx context.Context) *Manager {
	return ctx.Value(vmManagerKey).(*Manager)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, format string, args ...any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf(format, args...)})
}

func (s *Server) handleVMList(w http.ResponseWriter, r *http.Request) {
	mgr := vmManagerFrom(r.Context())
	vms, err := mgr.ListVMs()
	if err != nil {
		writeError(w, 500, "list vms: %v", err)
		return
	}
	writeJSON(w, vms)
}

func (s *Server) handleVMGet(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, 400, "missing id")
		return
	}
	mgr := vmManagerFrom(r.Context())
	vm, err := mgr.store.GetVM(id)
	if err != nil {
		writeError(w, 500, "get vm: %v", err)
		return
	}
	if vm == nil {
		writeError(w, 404, "vm not found")
		return
	}
	writeJSON(w, vm)
}

func (s *Server) handleVMStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "decode request: %v", err)
		return
	}
	// Not implemented for generic VMs — use box/temp endpoints.
	writeError(w, 400, "use /box.start or /temp.create instead")
}

func (s *Server) handleVMStop(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "decode request: %v", err)
		return
	}
	mgr := vmManagerFrom(r.Context())
	if err := mgr.Stop(req.ID); err != nil {
		writeError(w, 500, "stop vm: %v", err)
		return
	}
	writeJSON(w, map[string]string{"status": "stopped"})
}

func (s *Server) handleVMDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "decode request: %v", err)
		return
	}
	mgr := vmManagerFrom(r.Context())
	if err := mgr.Delete(req.ID); err != nil {
		writeError(w, 500, "delete vm: %v", err)
		return
	}
	writeJSON(w, map[string]string{"status": "deleted"})
}

func (s *Server) handleVMShell(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID   string   `json:"id"`
		Argv []string `json:"argv"`
		Cwd  string   `json:"cwd"`
		Env  []string `json:"env"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "decode request: %v", err)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		writeError(w, 500, "shell: hijacking not supported")
		return
	}
	mgr := vmManagerFrom(r.Context())
	stream, err := mgr.OpenShell(r.Context(), req.ID, req.Argv, req.Cwd, req.Env)
	if err != nil {
		writeError(w, 500, "shell: %v", err)
		return
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		stream.Close()
		return
	}
	client := &upgradedConn{Conn: conn, reader: rw.Reader}
	resp := &http.Response{
		StatusCode: http.StatusSwitchingProtocols,
		Status:     fmt.Sprintf("%d %s", http.StatusSwitchingProtocols, http.StatusText(http.StatusSwitchingProtocols)),
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     make(http.Header),
	}
	resp.Header.Set("Connection", "Upgrade")
	resp.Header.Set("Upgrade", "kindling-shell-v1")
	if err := resp.Write(rw); err != nil {
		stream.Close()
		conn.Close()
		return
	}
	if err := rw.Flush(); err != nil {
		stream.Close()
		conn.Close()
		return
	}
	proxyBidirectional(client, stream)
}

func (s *Server) handleVMExec(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID   string   `json:"id"`
		Argv []string `json:"argv"`
		Cwd  string   `json:"cwd"`
		Env  []string `json:"env"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "decode request: %v", err)
		return
	}
	mgr := vmManagerFrom(r.Context())
	code, output, err := mgr.Exec(r.Context(), req.ID, req.Argv, req.Cwd, req.Env)
	if err != nil {
		writeError(w, 500, "exec: %v", err)
		return
	}
	writeJSON(w, map[string]any{"exit_code": code, "output": output})
}

func (s *Server) handleBoxStart(w http.ResponseWriter, r *http.Request) {
	mgr := vmManagerFrom(r.Context())
	vm, err := mgr.StartBox(r.Context())
	if err != nil {
		writeError(w, 500, "start box: %v", err)
		return
	}
	writeJSON(w, vm)
}

func (s *Server) handleBoxStop(w http.ResponseWriter, r *http.Request) {
	mgr := vmManagerFrom(r.Context())
	if err := mgr.StopBox(r.Context()); err != nil {
		writeError(w, 500, "stop box: %v", err)
		return
	}
	writeJSON(w, map[string]string{"status": "stopped"})
}

func (s *Server) handleBoxStatus(w http.ResponseWriter, r *http.Request) {
	mgr := vmManagerFrom(r.Context())
	vm, err := mgr.GetBox()
	if err != nil {
		writeError(w, 500, "get box: %v", err)
		return
	}
	if vm == nil {
		writeJSON(w, map[string]string{"status": "not_configured"})
		return
	}
	writeJSON(w, vm)
}

func (s *Server) handleTempCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Template string `json:"template"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	mgr := vmManagerFrom(r.Context())
	vm, err := mgr.StartTemp(r.Context(), req.Template)
	if err != nil {
		writeError(w, 500, "create temp: %v", err)
		return
	}
	writeJSON(w, vm)
}

func (s *Server) handleTempList(w http.ResponseWriter, r *http.Request) {
	mgr := vmManagerFrom(r.Context())
	vms, err := mgr.store.ListVMs("temp")
	if err != nil {
		writeError(w, 500, "list temps: %v", err)
		return
	}
	writeJSON(w, vms)
}

func (s *Server) handleTempDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "decode request: %v", err)
		return
	}
	mgr := vmManagerFrom(r.Context())
	if err := mgr.Delete(req.ID); err != nil {
		writeError(w, 500, "delete temp: %v", err)
		return
	}
	writeJSON(w, map[string]string{"status": "deleted"})
}

func (s *Server) handleTemplateList(w http.ResponseWriter, r *http.Request) {
	mgr := vmManagerFrom(r.Context())
	templates, err := mgr.store.ListTemplates("")
	if err != nil {
		writeError(w, 500, "list templates: %v", err)
		return
	}
	writeJSON(w, templates)
}

func (s *Server) handleTemplateCapture(w http.ResponseWriter, r *http.Request) {
	var req struct {
		VMID string `json:"vm_id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "decode request: %v", err)
		return
	}
	// TODO: Implement template capture using Apple VZ CreateTemplate.
	_ = vmManagerFrom(r.Context()) // temporarily unused
	writeError(w, 500, "template capture not yet implemented")
}

func (s *Server) handleTemplateDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "decode request: %v", err)
		return
	}
	mgr := vmManagerFrom(r.Context())
	if err := mgr.store.DeleteTemplate(req.ID); err != nil {
		writeError(w, 500, "delete template: %v", err)
		return
	}
	writeJSON(w, map[string]string{"status": "deleted"})
}

// API is the client for the kindling-mac daemon API.
type API struct {
	socketPath string
	httpClient *http.Client
	transport  *http.Transport
}

// NewAPI creates an API client that connects to the kindling-mac daemon at socketPath.
func NewAPI(socketPath string) *API {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", os.ExpandEnv(socketPath))
		},
	}
	return &API{
		socketPath: socketPath,
		httpClient: &http.Client{Transport: transport},
	}
}

func (a *API) do(ctx context.Context, method, path string, body, out any) error {
	var reqBody io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reqBody = strings.NewReader(string(data))
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://localhost"+path, reqBody)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		var errResp map[string]string
		json.NewDecoder(resp.Body).Decode(&errResp)
		if errMsg, ok := errResp["error"]; ok {
			return fmt.Errorf("%s", errMsg)
		}
		return fmt.Errorf("API error %d", resp.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (a *API) SlicerStatus(ctx context.Context) (*VM, error) {
	var out VM
	err := a.do(ctx, http.MethodGet, "/box.status", nil, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (a *API) SlicerStart(ctx context.Context) (*VM, error) {
	var out VM
	err := a.do(ctx, http.MethodPost, "/box.start", nil, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (a *API) SlicerStop(ctx context.Context) error {
	return a.do(ctx, http.MethodPost, "/box.stop", nil, nil)
}

func (a *API) SBoxCreate(ctx context.Context, template string) (*VM, error) {
	var out VM
	err := a.do(ctx, http.MethodPost, "/temp.create", map[string]string{"template": template}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (a *API) SBoxList(ctx context.Context) ([]VM, error) {
	var out []VM
	err := a.do(ctx, http.MethodGet, "/temp.list", nil, &out)
	return out, err
}

func (a *API) SBoxDelete(ctx context.Context, id string) error {
	return a.do(ctx, http.MethodPost, "/temp.delete", map[string]string{"id": id}, nil)
}

func (a *API) VMList(ctx context.Context) ([]VM, error) {
	var out []VM
	err := a.do(ctx, http.MethodGet, "/vm.list", nil, &out)
	return out, err
}

func (a *API) VMStop(ctx context.Context, id string) error {
	return a.do(ctx, http.MethodPost, "/vm.stop", map[string]string{"id": id}, nil)
}

func (a *API) VMDelete(ctx context.Context, id string) error {
	return a.do(ctx, http.MethodPost, "/vm.delete", map[string]string{"id": id}, nil)
}

func (a *API) VMExec(ctx context.Context, id string, argv []string, cwd string, env []string) (int, string, error) {
	var req = struct {
		ID   string   `json:"id"`
		Argv []string `json:"argv"`
		Cwd  string   `json:"cwd"`
		Env  []string `json:"env"`
	}{id, argv, cwd, env}
	var out struct {
		ExitCode int    `json:"exit_code"`
		Output   string `json:"output"`
	}
	err := a.do(ctx, http.MethodPost, "/vm.exec", req, &out)
	return out.ExitCode, out.Output, err
}

func (a *API) TemplateList(ctx context.Context) ([]Template, error) {
	var out []Template
	err := a.do(ctx, http.MethodGet, "/template.list", nil, &out)
	return out, err
}

func proxyBidirectional(client *upgradedConn, upstream io.ReadWriteCloser) {
	done := make(chan struct{}, 2)
	go func() {
		defer func() { done <- struct{}{} }()
		_, _ = io.Copy(upstream, client)
		_ = upstream.Close()
	}()
	go func() {
		defer func() { done <- struct{}{} }()
		reader := bufio.NewReader(upstream)
		_, _ = io.Copy(client, reader)
		_ = client.Close()
	}()
	<-done
	_ = upstream.Close()
	_ = client.Close()
	<-done
}
