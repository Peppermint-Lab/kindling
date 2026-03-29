//go:build darwin

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
)

// localAPIClient is a client for the kindling-mac daemon's Unix socket HTTP API.
type localAPIClient struct {
	socketPath string
	transport  *http.Transport
}

func newLocalAPI(socketPath string) *localAPIClient {
	return &localAPIClient{
		socketPath: socketPath,
		transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
	}
}

func (a *localAPIClient) do(ctx context.Context, method, path string, body, out any) error {
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
	client := &http.Client{Transport: a.transport}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connect to kindling-mac daemon (is it running?): %w", err)
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

func (a *localAPIClient) doContext(ctx context.Context, method, path string, body, out any) error {
	return a.do(ctx, method, path, body, out)
}

func (a *localAPIClient) Status(ctx context.Context) error {
	// Get box status.
	var box map[string]any
	if err := a.do(ctx, http.MethodGet, "/box.status", nil, &box); err != nil {
		fmt.Printf("box: not configured\n")
	} else {
		fmt.Printf("box: %s (id=%v)\n", box["status"], box["id"])
	}

	// Get temp list.
	var temps []map[string]any
	if err := a.do(ctx, http.MethodGet, "/temp.list", nil, &temps); err != nil {
		fmt.Printf("temp list: %v\n", err)
	} else {
		fmt.Printf("temp VMs: %d running\n", len(temps))
		for _, t := range temps {
			fmt.Printf("  %s  %s  %s\n", t["id"], t["name"], t["status"])
		}
	}

	// Get all VMs.
	var vms []map[string]any
	if err := a.do(ctx, http.MethodGet, "/vm.list", nil, &vms); err != nil {
		fmt.Printf("vm list: %v\n", err)
	} else {
		fmt.Printf("total VMs: %d\n", len(vms))
	}

	return nil
}

func (a *localAPIClient) ListVMs(ctx context.Context) error {
	var vms []map[string]any
	if err := a.do(ctx, http.MethodGet, "/vm.list", nil, &vms); err != nil {
		return err
	}
	if len(vms) == 0 {
		fmt.Println("no VMs")
		return nil
	}
	fmt.Printf("%-36s  %-12s  %-8s  %s\n", "ID", "HOST GROUP", "STATUS", "NAME")
	for _, vm := range vms {
		fmt.Printf("%-36s  %-12s  %-8s  %s\n", vm["id"], vm["host_group"], vm["status"], vm["name"])
	}
	return nil
}
