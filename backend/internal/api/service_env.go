package api

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"aihub-cloud/backend/internal/secret"
)

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

func (a *API) getServiceEnv(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !a.canManageService(r, id) {
		writeError(w, 404, "not_found", "Service not found")
		return
	}
	rows, err := a.db.QueryContext(r.Context(), `SELECT name FROM service_env WHERE service_id=$1 ORDER BY name`, id)
	if err != nil {
		writeError(w, 500, "database_error", "Unable to list environment variables")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var name string
		if rows.Scan(&name) == nil {
			items = append(items, map[string]any{"name": name, "configured": true})
		}
	}
	writeJSON(w, 200, map[string]any{"items": items, "values_hidden": true})
}

func (a *API) patchServiceEnv(w http.ResponseWriter, r *http.Request) {
	if currentUser(r).Role == "viewer" {
		writeError(w, 403, "forbidden", "Read-only role")
		return
	}
	id := r.PathValue("id")
	if !a.canManageService(r, id) {
		writeError(w, 404, "not_found", "Service not found")
		return
	}
	var in struct {
		Set    map[string]string `json:"set"`
		Delete []string          `json:"delete"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in) != nil {
		writeError(w, 400, "invalid_request", "Invalid JSON body")
		return
	}
	if len(in.Set)+len(in.Delete) > 100 {
		writeError(w, 400, "too_many_variables", "At most 100 variables can be changed per request")
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, 500, "database_error", "Unable to update variables")
		return
	}
	defer tx.Rollback()
	for name, value := range in.Set {
		if !envNamePattern.MatchString(name) || strings.Contains(name, "=") || strings.EqualFold(name, "PORT") || strings.EqualFold(name, "HOST") || strings.EqualFold(name, "HOME") || strings.EqualFold(name, "PATH") {
			writeError(w, 400, "invalid_variable_name", "Environment variable name is invalid or reserved")
			return
		}
		if len(value) > 16384 {
			writeError(w, 400, "variable_too_large", "Environment variable values are limited to 16 KiB")
			return
		}
		ciphertext, e := secret.Encrypt(value)
		if e != nil {
			writeError(w, 500, "encryption_error", "Environment encryption is not configured correctly")
			return
		}
		if _, e = tx.ExecContext(r.Context(), `INSERT INTO service_env(service_id,name,ciphertext) VALUES($1,$2,$3) ON CONFLICT(service_id,name) DO UPDATE SET ciphertext=EXCLUDED.ciphertext,updated_at=now()`, id, name, ciphertext); e != nil {
			writeError(w, 500, "database_error", "Unable to save environment variable")
			return
		}
	}
	for _, name := range in.Delete {
		if !envNamePattern.MatchString(name) {
			writeError(w, 400, "invalid_variable_name", "Environment variable name is invalid")
			return
		}
		if _, err = tx.ExecContext(r.Context(), `DELETE FROM service_env WHERE service_id=$1 AND name=$2`, id, name); err != nil {
			writeError(w, 500, "database_error", "Unable to remove environment variable")
			return
		}
	}
	if err = tx.Commit(); err != nil {
		writeError(w, 500, "database_error", "Unable to update variables")
		return
	}
	audit(r.Context(), a.db, currentUser(r).ID, "service.env.update", id)
	writeJSON(w, 200, map[string]any{"status": "updated", "redeploy_required": true})
}

func (a *API) canManageService(r *http.Request, id string) bool {
	var owner int64
	if err := a.db.QueryRowContext(r.Context(), `SELECT owner_id FROM services WHERE id=$1`, id).Scan(&owner); err != nil {
		return false
	}
	return currentUser(r).Role == "admin" || owner == currentUser(r).ID
}
