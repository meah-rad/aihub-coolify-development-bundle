package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

var emailPattern = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

func (a *API) users(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.QueryContext(r.Context(), `SELECT id,email,role,disabled,created_at FROM users ORDER BY created_at DESC`)
	if err != nil {
		writeError(w, 500, "database_error", "Unable to list users")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var email, role string
		var disabled bool
		var created any
		if rows.Scan(&id, &email, &role, &disabled, &created) == nil {
			items = append(items, map[string]any{"id": id, "email": email, "role": role, "disabled": disabled, "created_at": created})
		}
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (a *API) createUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in) != nil {
		writeError(w, 400, "invalid_request", "Invalid JSON body")
		return
	}
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	if !emailPattern.MatchString(in.Email) {
		writeError(w, 400, "invalid_email", "Enter a valid email address")
		return
	}
	if len(in.Password) < 12 {
		writeError(w, 400, "weak_password", "Initial password must be at least 12 characters")
		return
	}
	if in.Role != "admin" && in.Role != "developer" && in.Role != "viewer" {
		writeError(w, 400, "invalid_role", "Role must be admin, developer, or viewer")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, 500, "password_error", "Unable to create user")
		return
	}
	var id int64
	err = a.db.QueryRowContext(r.Context(), `INSERT INTO users(email,password_hash,role) VALUES($1,$2,$3) RETURNING id`, in.Email, string(hash), in.Role).Scan(&id)
	if err != nil {
		writeError(w, 409, "email_exists", "A user with this email already exists")
		return
	}
	audit(r.Context(), a.db, currentUser(r).ID, "user.create", in.Email)
	writeJSON(w, 201, map[string]any{"id": id, "email": in.Email, "role": in.Role, "disabled": false})
}

func (a *API) updateUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Role     *string `json:"role"`
		Disabled *bool   `json:"disabled"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in) != nil {
		writeError(w, 400, "invalid_request", "Invalid JSON body")
		return
	}
	if in.Role == nil && in.Disabled == nil {
		writeError(w, 400, "empty_patch", "No supported fields were provided")
		return
	}
	if in.Role != nil && *in.Role != "admin" && *in.Role != "developer" && *in.Role != "viewer" {
		writeError(w, 400, "invalid_role", "Role must be admin, developer, or viewer")
		return
	}
	var targetID int64
	if _, err := fmt.Sscan(r.PathValue("id"), &targetID); err != nil {
		writeError(w, 400, "invalid_user", "Invalid user ID")
		return
	}
	if targetID == currentUser(r).ID && in.Disabled != nil && *in.Disabled {
		writeError(w, 400, "self_disable", "You cannot disable your own account")
		return
	}
	result, err := a.db.ExecContext(r.Context(), `UPDATE users SET role=COALESCE($2,role),disabled=COALESCE($3,disabled) WHERE id=$1`, targetID, in.Role, in.Disabled)
	if err != nil {
		writeError(w, 500, "database_error", "Unable to update user")
		return
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		writeError(w, 404, "not_found", "User not found")
		return
	}
	audit(r.Context(), a.db, currentUser(r).ID, "user.update", r.PathValue("id"))
	writeJSON(w, 200, map[string]string{"status": "updated"})
}
