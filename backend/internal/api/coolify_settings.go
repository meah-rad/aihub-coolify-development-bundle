package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"aihub-cloud/backend/internal/secret"
)

type coolifySettings struct {
	BaseURL          string `json:"base_url"`
	APIToken         string `json:"api_token"`
	ProjectUUID      string `json:"project_uuid"`
	EnvironmentUUID  string `json:"environment_uuid"`
	ServerUUID       string `json:"server_uuid"`
	RegistryPrefix   string `json:"registry_prefix"`
	RegistryUsername string `json:"registry_username"`
	RegistryPassword string `json:"registry_password"`
}

func (a *API) coolifyLinkSettings(ctx context.Context) (string, string, string) {
	rows, err := a.db.QueryContext(ctx, `SELECT key,ciphertext FROM integration_settings WHERE key IN ('coolify.base_url','coolify.project_uuid','coolify.environment_uuid')`)
	if err != nil {
		return "", "", ""
	}
	defer rows.Close()
	values := map[string]string{}
	for rows.Next() {
		var key string
		var ciphertext []byte
		if rows.Scan(&key, &ciphertext) != nil {
			return "", "", ""
		}
		value, err := secret.Decrypt(ciphertext)
		if err != nil {
			return "", "", ""
		}
		values[key] = value
	}
	if rows.Err() != nil {
		return "", "", ""
	}
	return values["coolify.base_url"], values["coolify.project_uuid"], values["coolify.environment_uuid"]
}

func coolifyAppURL(baseURL, projectUUID, environmentUUID, appUUID string) string {
	if baseURL == "" || projectUUID == "" || environmentUUID == "" || appUUID == "" {
		return ""
	}
	return strings.TrimRight(baseURL, "/") + "/project/" + url.PathEscape(projectUUID) + "/environment/" + url.PathEscape(environmentUUID) + "/application/" + url.PathEscape(appUUID)
}

func (a *API) readCoolifySettings() (coolifySettings, error) {
	settings := coolifySettings{}
	rows, err := a.db.Query(`SELECT key,ciphertext FROM integration_settings WHERE key LIKE 'coolify.%'`)
	if err != nil {
		return settings, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var ciphertext []byte
		if err = rows.Scan(&key, &ciphertext); err != nil {
			return settings, err
		}
		value, decryptErr := secret.Decrypt(ciphertext)
		if decryptErr != nil {
			return settings, decryptErr
		}
		switch strings.TrimPrefix(key, "coolify.") {
		case "base_url":
			settings.BaseURL = value
		case "api_token":
			settings.APIToken = value
		case "project_uuid":
			settings.ProjectUUID = value
		case "environment_uuid":
			settings.EnvironmentUUID = value
		case "server_uuid":
			settings.ServerUUID = value
		case "registry_prefix":
			settings.RegistryPrefix = value
		case "registry_username":
			settings.RegistryUsername = value
		case "registry_password":
			settings.RegistryPassword = value
		}
	}
	return settings, rows.Err()
}

func (a *API) getCoolifySettings(w http.ResponseWriter, r *http.Request) {
	s, err := a.readCoolifySettings()
	if err != nil {
		writeError(w, 500, "settings_error", "Unable to read Coolify settings")
		return
	}
	writeJSON(w, 200, map[string]any{
		"base_url": s.BaseURL, "project_uuid": s.ProjectUUID, "environment_uuid": s.EnvironmentUUID,
		"server_uuid": s.ServerUUID, "registry_prefix": s.RegistryPrefix,
		"api_token_configured": s.APIToken != "", "registry_username_configured": s.RegistryUsername != "",
		"registry_password_configured": s.RegistryPassword != "",
	})
}

func (a *API) updateCoolifySettings(w http.ResponseWriter, r *http.Request) {
	var incoming coolifySettings
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&incoming) != nil {
		writeError(w, 400, "invalid_request", "Invalid JSON body")
		return
	}
	current, err := a.readCoolifySettings()
	if err != nil {
		writeError(w, 500, "settings_error", "Unable to read Coolify settings")
		return
	}
	if incoming.APIToken == "" {
		incoming.APIToken = current.APIToken
	}
	if incoming.RegistryUsername == "" {
		incoming.RegistryUsername = current.RegistryUsername
	}
	if incoming.RegistryPassword == "" {
		incoming.RegistryPassword = current.RegistryPassword
	}
	incoming.BaseURL = strings.TrimRight(strings.TrimSpace(incoming.BaseURL), "/")
	u, parseErr := url.ParseRequestURI(incoming.BaseURL)
	localHTTP := u != nil && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "host.docker.internal")
	if parseErr != nil || (u.Scheme != "https" && !(u.Scheme == "http" && localHTTP)) || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		writeError(w, 400, "invalid_coolify_url", "Coolify URL must use HTTPS (HTTP is allowed only for localhost development)")
		return
	}
	if incoming.APIToken == "" || incoming.ProjectUUID == "" || incoming.EnvironmentUUID == "" || incoming.ServerUUID == "" || incoming.RegistryPrefix == "" || incoming.RegistryUsername == "" || incoming.RegistryPassword == "" {
		writeError(w, 400, "incomplete_coolify_settings", "Coolify target and OCI registry push credentials are required")
		return
	}
	incoming.RegistryPrefix = strings.TrimRight(strings.TrimSpace(incoming.RegistryPrefix), "/")
	registryURL, registryErr := url.Parse("https://" + incoming.RegistryPrefix)
	if registryErr != nil || registryURL.Host == "" || registryURL.User != nil || registryURL.RawQuery != "" || registryURL.Fragment != "" || strings.Contains(incoming.RegistryPrefix, "://") || strings.ContainsAny(incoming.RegistryPrefix, " \t\r\n") {
		writeError(w, 400, "invalid_registry_prefix", "Registry prefix must be a hostname with an optional namespace path, without a URL scheme")
		return
	}
	values := map[string]string{"base_url": incoming.BaseURL, "api_token": incoming.APIToken, "project_uuid": strings.TrimSpace(incoming.ProjectUUID), "environment_uuid": strings.TrimSpace(incoming.EnvironmentUUID), "server_uuid": strings.TrimSpace(incoming.ServerUUID), "registry_prefix": incoming.RegistryPrefix, "registry_username": incoming.RegistryUsername, "registry_password": incoming.RegistryPassword}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, 500, "settings_error", "Unable to save Coolify settings")
		return
	}
	defer tx.Rollback()
	for key, value := range values {
		ciphertext, encryptErr := secret.Encrypt(value)
		if encryptErr != nil {
			writeError(w, 500, "encryption_error", "Unable to encrypt integration credentials")
			return
		}
		if _, err = tx.ExecContext(r.Context(), `INSERT INTO integration_settings(key,ciphertext,updated_at) VALUES($1,$2,now()) ON CONFLICT(key) DO UPDATE SET ciphertext=EXCLUDED.ciphertext,updated_at=now()`, "coolify."+key, ciphertext); err != nil {
			writeError(w, 500, "settings_error", "Unable to save Coolify settings")
			return
		}
	}
	if err = tx.Commit(); err != nil {
		writeError(w, 500, "settings_error", "Unable to save Coolify settings")
		return
	}
	audit(r.Context(), a.db, currentUser(r).ID, "integration.coolify.update", "coolify")
	writeJSON(w, 200, map[string]string{"status": "saved"})
}

func (a *API) testCoolifySettings(w http.ResponseWriter, r *http.Request) {
	s, err := a.readCoolifySettings()
	if err != nil || s.BaseURL == "" || s.APIToken == "" {
		writeError(w, 400, "coolify_not_configured", "Save complete Coolify settings first")
		return
	}
	client := &http.Client{Timeout: 10 * time.Second}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, s.BaseURL+"/api/v1/projects", nil)
	if err != nil {
		writeError(w, 400, "invalid_coolify_url", "Invalid Coolify URL")
		return
	}
	request.Header.Set("Authorization", "Bearer "+s.APIToken)
	response, err := client.Do(request)
	if err != nil {
		writeError(w, 502, "coolify_unreachable", "Unable to connect to Coolify")
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		writeError(w, 502, "coolify_auth_failed", "Coolify rejected the API token or project access")
		return
	}
	response.Body.Close()
	targets := []struct {
		name, path, code string
	}{
		{"project", "/projects/" + url.PathEscape(s.ProjectUUID), "coolify_project_invalid"},
		{"environment", "/projects/" + url.PathEscape(s.ProjectUUID) + "/" + url.PathEscape(s.EnvironmentUUID), "coolify_environment_invalid"},
		{"server", "/servers/" + url.PathEscape(s.ServerUUID), "coolify_server_invalid"},
	}
	for _, target := range targets {
		path := target.path
		request, err = http.NewRequestWithContext(r.Context(), http.MethodGet, s.BaseURL+"/api/v1"+path, nil)
		if err != nil {
			writeError(w, 400, "invalid_coolify_target", "Invalid Coolify target identifier")
			return
		}
		request.Header.Set("Authorization", "Bearer "+s.APIToken)
		response, err = client.Do(request)
		if err != nil {
			writeError(w, 502, "coolify_unreachable", "Unable to verify the configured Coolify project, environment, and server")
			return
		}
		response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			writeError(w, 502, target.code, fmt.Sprintf("Coolify API could not access the configured %s (HTTP %d); check its UUID, team scope, and token read ability", target.name, response.StatusCode))
			return
		}
	}
	writeJSON(w, 200, map[string]string{"status": "connected"})
}
