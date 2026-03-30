package rpc

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
	kruntime "github.com/kindlingvm/kindling/internal/runtime"
	"github.com/kindlingvm/kindling/internal/shellwire"
)

func (a *API) sandboxShell(w http.ResponseWriter, r *http.Request) {
	a.sandboxShellWS(w, r)
}

func (a *API) sandboxShellWS(w http.ResponseWriter, r *http.Request) {
	if !a.requireValidSandboxProxyIfPresent(w, r) {
		return
	}
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	sb, ok := a.requireSandboxAccess(w, r, p)
	if !ok {
		return
	}
	if a.proxySandboxWebsocket(w, r, sb) {
		return
	}
	access, ok := a.sandboxStreamAccess(w, sb)
	if !ok {
		return
	}
	shellPath := strings.TrimSpace(r.URL.Query().Get("shell"))
	if shellPath == "" {
		shellPath = "/bin/sh"
	}
	cwd := strings.TrimSpace(r.URL.Query().Get("cwd"))
	env := append([]string(nil), r.URL.Query()["env"]...)
	stream, err := access.StreamGuest(r.Context(), uuid.UUID(sb.ID.Bytes), []string{shellPath}, cwd, env)
	if err != nil {
		a.recordRemoteVMAccessEvent(r.Context(), uuid.UUID(sb.ID.Bytes), p.UserID, "shell_ws", "failed", nil, err.Error())
		writeAPIErrorFromErr(w, http.StatusConflict, "sandbox_shell", err)
		return
	}
	defer stream.Close()

	ws, err := a.sandboxWebsocketUpgrader().Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()

	a.recordRemoteVMAccessEvent(r.Context(), uuid.UUID(sb.ID.Bytes), p.UserID, "shell_ws", "started", nil, "")
	runSandboxAccessKeepalive(r.Context(), a.q, sb.ID)

	var (
		exitCode *int
		exitMu   sync.Mutex
	)
	done := make(chan error, 2)

	go func() {
		dec := shellwire.NewDecoder(stream)
		for {
			frame, err := dec.Decode()
			if err != nil {
				done <- err
				return
			}
			if frame.Type == "exit" && frame.ExitCode != nil {
				exitMu.Lock()
				v := *frame.ExitCode
				exitCode = &v
				exitMu.Unlock()
			}
			if err := ws.WriteJSON(frame); err != nil {
				done <- err
				return
			}
		}
	}()

	go func() {
		enc := shellwire.NewEncoder(stream)
		for {
			var frame shellwire.Frame
			if err := ws.ReadJSON(&frame); err != nil {
				done <- err
				return
			}
			if err := enc.Encode(frame); err != nil {
				done <- err
				return
			}
			_ = a.q.RemoteVMUpdateLastUsedAt(context.Background(), sb.ID)
		}
	}()

	err = <-done
	exitMu.Lock()
	finalExit := exitCode
	exitMu.Unlock()
	_ = a.q.RemoteVMUpdateLastUsedAt(context.Background(), sb.ID)
	if isExpectedSocketClose(err) || errors.Is(err, io.EOF) {
		a.recordRemoteVMAccessEvent(context.Background(), uuid.UUID(sb.ID.Bytes), p.UserID, "shell_ws", "ended", finalExit, "")
		return
	}
	a.recordRemoteVMAccessEvent(context.Background(), uuid.UUID(sb.ID.Bytes), p.UserID, "shell_ws", "failed", finalExit, err.Error())
}

func (a *API) sandboxSSHWS(w http.ResponseWriter, r *http.Request) {
	if !a.requireValidSandboxProxyIfPresent(w, r) {
		return
	}
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	sb, ok := a.requireSandboxAccess(w, r, p)
	if !ok {
		return
	}
	if a.proxySandboxWebsocket(w, r, sb) {
		return
	}
	access, ok := a.sandboxTCPAccess(w, sb)
	if !ok {
		return
	}
	stream, err := access.ConnectGuestTCP(r.Context(), uuid.UUID(sb.ID.Bytes), 22)
	if err != nil {
		a.recordRemoteVMAccessEvent(r.Context(), uuid.UUID(sb.ID.Bytes), p.UserID, "ssh", "failed", nil, err.Error())
		writeAPIErrorFromErr(w, http.StatusConflict, "sandbox_ssh", err)
		return
	}
	defer stream.Close()

	ws, err := a.sandboxWebsocketUpgrader().Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()

	a.recordRemoteVMAccessEvent(r.Context(), uuid.UUID(sb.ID.Bytes), p.UserID, "ssh", "started", nil, "")
	runSandboxAccessKeepalive(r.Context(), a.q, sb.ID)

	done := make(chan error, 2)
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := stream.Read(buf)
			if n > 0 {
				if werr := ws.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					done <- werr
					return
				}
			}
			if err != nil {
				done <- err
				return
			}
		}
	}()
	go func() {
		for {
			mt, payload, err := ws.ReadMessage()
			if err != nil {
				done <- err
				return
			}
			if mt != websocket.BinaryMessage && mt != websocket.TextMessage {
				continue
			}
			if len(payload) > 0 {
				if _, err := stream.Write(payload); err != nil {
					done <- err
					return
				}
			}
			_ = a.q.RemoteVMUpdateLastUsedAt(context.Background(), sb.ID)
		}
	}()

	err = <-done
	_ = a.q.RemoteVMUpdateLastUsedAt(context.Background(), sb.ID)
	if isExpectedSocketClose(err) || errors.Is(err, io.EOF) {
		a.recordRemoteVMAccessEvent(context.Background(), uuid.UUID(sb.ID.Bytes), p.UserID, "ssh", "ended", nil, "")
		return
	}
	a.recordRemoteVMAccessEvent(context.Background(), uuid.UUID(sb.ID.Bytes), p.UserID, "ssh", "failed", nil, err.Error())
}

func (a *API) sandboxTCPAccess(w http.ResponseWriter, sb queries.RemoteVm) (kruntime.GuestTCPAccess, bool) {
	rt, ok := a.sandboxLocalRuntime(w, sb)
	if !ok {
		return nil, false
	}
	access, ok := rt.(kruntime.GuestTCPAccess)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "sandbox_runtime", "guest TCP access is not implemented for this remote VM runtime")
		return nil, false
	}
	return access, true
}

func runSandboxAccessKeepalive(ctx context.Context, q *queries.Queries, sandboxID pgtype.UUID) {
	if q == nil || !sandboxID.Valid {
		return
	}
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = q.RemoteVMUpdateLastUsedAt(context.Background(), sandboxID)
			}
		}
	}()
}

func isExpectedSocketClose(err error) bool {
	if err == nil {
		return true
	}
	return websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway)
}
