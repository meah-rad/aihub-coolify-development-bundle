package worker

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"aihub-cloud/backend/internal/secret"
)

type coolifyConfig struct {
	BaseURL, Token, ProjectUUID, EnvironmentUUID, ServerUUID string
	RegistryPrefix, RegistryUsername, RegistryPassword       string
}

type coolifyClient struct {
	baseURL, token string
	client         *http.Client
}

func loadCoolifyConfig(ctx context.Context, db *sql.DB) (coolifyConfig, error) {
	c := coolifyConfig{}
	rows, err := db.QueryContext(ctx, `SELECT key,ciphertext FROM integration_settings WHERE key LIKE 'coolify.%'`)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var encrypted []byte
		if err = rows.Scan(&key, &encrypted); err != nil {
			return c, err
		}
		value, err := secret.Decrypt(encrypted)
		if err != nil {
			return c, err
		}
		switch strings.TrimPrefix(key, "coolify.") {
		case "base_url":
			c.BaseURL = value
		case "api_token":
			c.Token = value
		case "project_uuid":
			c.ProjectUUID = value
		case "environment_uuid":
			c.EnvironmentUUID = value
		case "server_uuid":
			c.ServerUUID = value
		case "registry_prefix":
			c.RegistryPrefix = value
		case "registry_username":
			c.RegistryUsername = value
		case "registry_password":
			c.RegistryPassword = value
		}
	}
	if err = rows.Err(); err != nil {
		return c, err
	}
	if c.BaseURL == "" || c.Token == "" || c.ProjectUUID == "" || c.EnvironmentUUID == "" || c.ServerUUID == "" || c.RegistryPrefix == "" || c.RegistryUsername == "" || c.RegistryPassword == "" {
		return c, fmt.Errorf("Coolify integration is not fully configured in platform settings")
	}
	return c, nil
}

func (c coolifyConfig) clientFor() *coolifyClient {
	return &coolifyClient{baseURL: strings.TrimRight(c.BaseURL, "/"), token: c.Token, client: &http.Client{Timeout: 30 * time.Second}}
}

func (c *coolifyClient) request(ctx context.Context, method, path string, payload any, out any) error {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+"/api/v1"+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("Coolify request failed: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Coolify API %s returned %d: %s", path, resp.StatusCode, tail(string(data), 1200))
	}
	if out != nil && len(data) > 0 {
		if err = json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode Coolify response: %w", err)
		}
	}
	return nil
}

func (c coolifyConfig) imageRef(slug, deploymentID string) (string, string, error) {
	if strings.Contains(c.RegistryPrefix, "://") {
		return "", "", fmt.Errorf("registry prefix must not include a URL scheme")
	}
	u, err := url.Parse("https://" + c.RegistryPrefix)
	if err != nil || u.Host == "" {
		return "", "", fmt.Errorf("registry prefix must be a hostname/path, got invalid value")
	}
	name := strings.Trim(c.RegistryPrefix, "/") + "/" + slug
	return name, deploymentID, nil
}

func writeDockerConfig(dir, username, password, host string) (string, error) {
	encoded := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	config := []byte(fmt.Sprintf(`{"auths":{"%s":{"auth":"%s"}}}`, host, encoded))
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, config, 0600); err != nil {
		return "", err
	}
	return dir, nil
}

func (c coolifyConfig) publish(ctx context.Context, work, imageName, tag string) (string, error) {
	u, _ := url.Parse("https://" + c.RegistryPrefix)
	configDir, err := os.MkdirTemp("", "aihub-docker-auth-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(configDir)
	dockerConfig, err := writeDockerConfig(configDir, c.RegistryUsername, c.RegistryPassword, u.Host)
	if err != nil {
		return "", err
	}
	image := imageName + ":" + tag
	buildCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(buildCtx, "buildctl", "--addr", env("BUILDKIT_ADDR", "tcp://buildkit:1234"), "build", "--frontend", "dockerfile.v0", "--local", "context="+work, "--local", "dockerfile="+work, "--opt", "filename=Dockerfile", "--output", "type=image,name="+image+",push=true")
	cmd.Env = append(os.Environ(), "DOCKER_CONFIG="+dockerConfig)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("image build/push failed: %s", tail(string(output), 1200))
	}
	return image, nil
}

func (c coolifyConfig) createOrUpdateAndDeploy(ctx context.Context, db *sql.DB, j job, imageName, tag string, port int) (string, string, error) {
	client := c.clientFor()
	var applicationUUID string
	if err := db.QueryRowContext(ctx, `SELECT coolify_application_uuid FROM services WHERE id=$1`, j.ServiceID).Scan(&applicationUUID); err != nil {
		return "", "", err
	}
	if applicationUUID == "" {
		var created struct {
			UUID    string `json:"uuid"`
			Domains any    `json:"domains"`
		}
		payload := map[string]any{"project_uuid": c.ProjectUUID, "environment_uuid": c.EnvironmentUUID, "server_uuid": c.ServerUUID, "docker_registry_image_name": imageName, "docker_registry_image_tag": tag, "ports_exposes": fmt.Sprint(port), "name": j.Name, "description": "Managed by AIHub source deployment", "autogenerate_domain": true, "instant_deploy": false}
		if err := client.request(ctx, http.MethodPost, "/applications/dockerimage", payload, &created); err != nil {
			return "", "", err
		}
		applicationUUID = created.UUID
		if applicationUUID == "" {
			return "", "", fmt.Errorf("Coolify did not return an application UUID")
		}
		if _, err := db.ExecContext(ctx, `UPDATE services SET coolify_application_uuid=$2 WHERE id=$1`, j.ServiceID, applicationUUID); err != nil {
			return "", "", err
		}
	} else {
		payload := map[string]any{"docker_registry_image_name": imageName, "docker_registry_image_tag": tag, "ports_exposes": fmt.Sprint(port)}
		if err := client.request(ctx, http.MethodPatch, "/applications/"+url.PathEscape(applicationUUID), payload, nil); err != nil {
			return "", "", err
		}
	}
	var queued struct {
		DeploymentUUID string `json:"deployment_uuid"`
		Message        string `json:"message"`
	}
	if err := client.request(ctx, http.MethodPost, "/applications/"+url.PathEscape(applicationUUID)+"/start", map[string]any{"instant_deploy": true}, &queued); err != nil {
		return applicationUUID, "", err
	}
	if queued.DeploymentUUID == "" {
		return applicationUUID, "", fmt.Errorf("Coolify did not queue a deployment: %s", queued.Message)
	}
	return applicationUUID, queued.DeploymentUUID, nil
}

func (c coolifyConfig) waitDeployment(ctx context.Context, deploymentUUID string) (string, error) {
	client := c.clientFor()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	deadline := time.NewTimer(15 * time.Minute)
	defer deadline.Stop()
	for {
		var state struct {
			Status        string `json:"status"`
			Error         string `json:"error"`
			Logs          string `json:"logs"`
			StatusMessage string `json:"status_message"`
		}
		if err := client.request(ctx, http.MethodGet, "/deployments/"+url.PathEscape(deploymentUUID), nil, &state); err != nil {
			return "", err
		}
		switch strings.ToLower(state.Status) {
		case "finished", "succeeded", "success":
			return state.StatusMessage, nil
		case "failed", "error", "cancelled-by-user", "cancelled":
			return "", fmt.Errorf("Coolify deployment %s: %s", state.Status, tail(state.Error+" "+state.Logs, 1200))
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline.C:
			return "", fmt.Errorf("timed out waiting for Coolify deployment %s", deploymentUUID)
		case <-ticker.C:
		}
	}
}

func (c coolifyConfig) application(ctx context.Context, applicationUUID string) (map[string]any, error) {
	var app map[string]any
	err := c.clientFor().request(ctx, http.MethodGet, "/applications/"+url.PathEscape(applicationUUID), nil, &app)
	return app, err
}
