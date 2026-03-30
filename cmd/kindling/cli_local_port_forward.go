//go:build darwin

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

const localPortForwardDialTimeout = 10 * time.Second

type localPortForwardOptions struct {
	out       io.Writer
	errOut    io.Writer
	host      string
	hostPort  int
	guestPort int

	listen    func(network, address string) (net.Listener, error)
	ensureBox func(context.Context) (string, error)
	openTCP   func(context.Context, string, int) (io.ReadWriteCloser, error)
}

func cliLocalBoxPortForwardCmd() *cobra.Command {
	var guestPort int
	var hostPort int

	cmd := &cobra.Command{
		Use:   "port-forward",
		Short: "Forward a TCP port from the box VM to localhost",
		RunE: func(cmd *cobra.Command, args []string) error {
			api, err := localAPI()
			if err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			return runLocalPortForward(ctx, localPortForwardOptions{
				out:       cmd.OutOrStdout(),
				errOut:    cmd.ErrOrStderr(),
				host:      "127.0.0.1",
				hostPort:  hostPort,
				guestPort: guestPort,
				listen:    net.Listen,
				ensureBox: api.ensureBoxRunning,
				openTCP:   api.openTCP,
			})
		},
	}
	cmd.Flags().IntVar(&guestPort, "guest-port", 5432, "Guest TCP port to forward")
	cmd.Flags().IntVar(&hostPort, "host-port", 5432, "Local localhost TCP port to bind (0 chooses an ephemeral port)")
	return cmd
}

func runLocalPortForward(ctx context.Context, opts localPortForwardOptions) error {
	if err := validateLocalPortForwardPorts(opts.guestPort, opts.hostPort); err != nil {
		return err
	}
	if opts.listen == nil {
		opts.listen = net.Listen
	}
	if opts.out == nil {
		opts.out = io.Discard
	}
	if opts.errOut == nil {
		opts.errOut = io.Discard
	}
	if strings.TrimSpace(opts.host) == "" {
		opts.host = "127.0.0.1"
	}
	if opts.ensureBox == nil {
		return fmt.Errorf("ensure box callback is required")
	}
	if opts.openTCP == nil {
		return fmt.Errorf("open tcp callback is required")
	}

	boxID, err := opts.ensureBox(ctx)
	if err != nil {
		return err
	}

	ln, err := opts.listen("tcp", net.JoinHostPort(opts.host, strconv.Itoa(opts.hostPort)))
	if err != nil {
		return fmt.Errorf("listen on %s:%d: %w", opts.host, opts.hostPort, err)
	}
	defer ln.Close()

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	actualPort := 0
	if tcpAddr, ok := ln.Addr().(*net.TCPAddr); ok {
		actualPort = tcpAddr.Port
	}
	fmt.Fprintf(opts.out, "Forwarding %s:%d -> guest 127.0.0.1:%d (box %s)\n", opts.host, actualPort, opts.guestPort, boxID)
	if opts.guestPort == 5432 {
		fmt.Fprintf(opts.out, "Postgres DSN: postgres://kindling:kindling@%s:%d/kindling?sslmode=disable\n", opts.host, actualPort)
	}

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errorsIsUseOfClosedNetworkConn(err) {
				return nil
			}
			return fmt.Errorf("accept forwarded connection: %w", err)
		}
		go handleLocalPortForwardConn(ctx, opts, boxID, conn)
	}
}

func handleLocalPortForwardConn(ctx context.Context, opts localPortForwardOptions, boxID string, conn net.Conn) {
	defer conn.Close()

	dialCtx, cancel := context.WithTimeout(ctx, localPortForwardDialTimeout)
	defer cancel()

	stream, err := opts.openTCP(dialCtx, boxID, opts.guestPort)
	if err != nil {
		fmt.Fprintf(opts.errOut, "port-forward connect failed: %v\n", err)
		return
	}
	defer stream.Close()

	copyBidirectional(conn, stream)
}

func copyBidirectional(a net.Conn, b io.ReadWriteCloser) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(b, a)
		_ = b.Close()
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(a, b)
		_ = a.Close()
	}()
	wg.Wait()
}

func validateLocalPortForwardPorts(guestPort, hostPort int) error {
	if guestPort <= 0 || guestPort > 65535 {
		return fmt.Errorf("invalid guest port %d", guestPort)
	}
	if hostPort < 0 || hostPort > 65535 {
		return fmt.Errorf("invalid host port %d", hostPort)
	}
	return nil
}

func errorsIsUseOfClosedNetworkConn(err error) bool {
	return err != nil && strings.Contains(err.Error(), "use of closed network connection")
}

func (a *localAPIClient) ensureBoxRunning(ctx context.Context) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	if err := a.do(ctx, http.MethodPost, "/box.start", nil, &out); err != nil {
		return "", err
	}
	if strings.TrimSpace(out.ID) == "" {
		return "", fmt.Errorf("box start returned no id")
	}
	return out.ID, nil
}

func (a *localAPIClient) openTCP(ctx context.Context, id string, port int) (io.ReadWriteCloser, error) {
	if err := validateLocalPortForwardPorts(port, 0); err != nil {
		return nil, err
	}
	if err := a.ensureDaemon(ctx); err != nil {
		return nil, err
	}

	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", a.socketPath)
	if err != nil {
		return nil, fmt.Errorf("connect to kindling-mac daemon (is it running?): %w", err)
	}

	reqBody, err := json.Marshal(map[string]any{
		"id":   id,
		"port": port,
	})
	if err != nil {
		conn.Close()
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost/vm.tcp", bytes.NewReader(reqBody))
	if err != nil {
		conn.Close()
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "kindling-tcp-v1")
	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, err
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(resp.Body)
		conn.Close()
		return nil, fmt.Errorf("open tcp stream: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return &localUpgradedConn{Conn: conn, reader: reader}, nil
}
