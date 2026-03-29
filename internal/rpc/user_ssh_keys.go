package rpc

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"golang.org/x/crypto/ssh"
)

type userSSHKeyOut struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	PublicKey string `json:"public_key"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func userSSHKeyToOut(row queries.UserSshKey) userSSHKeyOut {
	return userSSHKeyOut{
		ID:        uuid.UUID(row.ID.Bytes).String(),
		Name:      row.Name,
		PublicKey: row.PublicKey,
		CreatedAt: row.CreatedAt.Time.UTC().Format(time.RFC3339Nano),
		UpdatedAt: row.UpdatedAt.Time.UTC().Format(time.RFC3339Nano),
	}
}

func (a *API) listUserSSHKeys(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	rows, err := a.q.UserSSHKeyListByUser(r.Context(), pgtype.UUID{Bytes: p.UserID, Valid: p.UserID != uuid.Nil})
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "list_user_ssh_keys", err)
		return
	}
	out := make([]userSSHKeyOut, 0, len(rows))
	for _, row := range rows {
		out = append(out, userSSHKeyToOut(row))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) createUserSSHKey(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	var req struct {
		Name      string `json:"name"`
		PublicKey string `json:"public_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "invalid json body")
		return
	}
	publicKey := strings.TrimSpace(req.PublicKey)
	if publicKey == "" {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "public_key is required")
		return
	}
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(publicKey)); err != nil {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "public_key must be a valid authorized_keys entry")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "SSH key"
	}
	row, err := a.q.UserSSHKeyCreate(r.Context(), queries.UserSSHKeyCreateParams{
		ID:        pgtype.UUID{Bytes: uuid.New(), Valid: true},
		UserID:    pgtype.UUID{Bytes: p.UserID, Valid: p.UserID != uuid.Nil},
		Name:      name,
		PublicKey: publicKey,
	})
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "create_user_ssh_key", err)
		return
	}
	_ = a.q.SandboxTouchRunningByOrg(r.Context(), p.OrganizationID)
	writeJSON(w, http.StatusCreated, userSSHKeyToOut(row))
}

func (a *API) deleteUserSSHKey(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	keyID, err := parseUUID(r.PathValue("key_id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid ssh key id")
		return
	}
	if err := a.q.UserSSHKeyDeleteByIDAndUser(r.Context(), queries.UserSSHKeyDeleteByIDAndUserParams{
		ID:     keyID,
		UserID: pgtype.UUID{Bytes: p.UserID, Valid: p.UserID != uuid.Nil},
	}); err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "delete_user_ssh_key", err)
		return
	}
	_ = a.q.SandboxTouchRunningByOrg(r.Context(), p.OrganizationID)
	w.WriteHeader(http.StatusNoContent)
}
