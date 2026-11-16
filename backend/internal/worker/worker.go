package worker

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"aihub-cloud/backend/internal/secret"
	"gopkg.in/yaml.v3"
)

type job struct {
	QueueID                                                                                              int64
	ID, Kind, Action, ServiceID, Slug, Name, OldContainer, Network, CoolifyApp, CoolifyDeployment, Image string
	Sequence                                                                                             int
	OwnerID                                                                                              int64
}
type docker struct {
	base   string
	client *http.Client
}
type manifest struct {
	Web struct {
		Port       int    `yaml:"port"`
		Command    string `yaml:"command"`
		HealthPath string `yaml:"healthPath"`
	} `yaml:"web"`
}

func Run(ctx context.Context, db *sql.DB) error {
	logf("deployment worker started")
	if err := recoverStaleDeployments(ctx, db); err != nil {
		return fmt.Errorf("recover stale deployments: %w", err)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		j, err := claim(ctx, db)
		if err != nil {
			logf("claim job: %v", err)
			sleep(ctx, 3*time.Second)
			continue
		}
		if j == nil {
			sleep(ctx, 2*time.Second)
			continue
		}
		if j.Kind == "action" {
			if err := runAction(ctx, db, *j); err != nil {
				failAction(ctx, db, *j, err)
				logf("service action %s failed: %v", j.Action, err)
			}
			continue
		}
		if err := deploy(ctx, db, *j); err != nil {
			fail(ctx, db, *j, err)
			logf("deployment %s failed: %v", j.ID, err)
		}
	}
}

func recoverStaleDeployments(ctx context.Context, db *sql.DB) error {
	result, err := db.ExecContext(ctx, `UPDATE jobs SET status='queued',available_at=now(),locked_at=NULL WHERE kind='deploy' AND status='running' AND locked_at < now()-interval '45 minutes'`)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count > 0 {
		logf("requeued %d stale deployment job(s)", count)
	}
	return err
}

func claim(ctx context.Context, db *sql.DB) (*job, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var j job
	err = tx.QueryRowContext(ctx, `SELECT q.id,q.kind,COALESCE(d.id,''),COALESCE(q.action,''),COALESCE(s.id,''),COALESCE(s.slug,''),COALESCE(s.name,''),COALESCE(s.container_id,''),COALESCE(s.network_id,''),COALESCE(s.coolify_application_uuid,''),COALESCE(d.coolify_deployment_uuid,''),COALESCE(d.image,''),COALESCE(d.sequence,0),COALESCE(s.owner_id,0) FROM jobs q LEFT JOIN deployments d ON d.id=q.deployment_id LEFT JOIN services s ON s.id=COALESCE(d.service_id,q.service_id) WHERE q.status='queued' AND q.available_at<=now() ORDER BY q.created_at LIMIT 1 FOR UPDATE OF q SKIP LOCKED`).Scan(&j.QueueID, &j.Kind, &j.ID, &j.Action, &j.ServiceID, &j.Slug, &j.Name, &j.OldContainer, &j.Network, &j.CoolifyApp, &j.CoolifyDeployment, &j.Image, &j.Sequence, &j.OwnerID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE jobs SET status='running',attempts=attempts+1,locked_at=now() WHERE id=$1`, j.QueueID); err != nil {
		return nil, err
	}
	if j.Kind == "deploy" {
		if _, err = tx.ExecContext(ctx, `UPDATE deployments SET status=CASE WHEN coolify_deployment_uuid<>'' THEN 'deploying' ELSE 'building' END WHERE id=$1`, j.ID); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO deployment_events(deployment_id,message) VALUES($1,CASE WHEN $2<>'' THEN 'Resuming Coolify deployment by worker' ELSE 'Build queued by worker' END)`, j.ID, j.CoolifyDeployment); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &j, nil
}

func deploy(ctx context.Context, db *sql.DB, j job) error {
	coolify, err := loadCoolifyConfig(ctx, db)
	if err != nil {
		return err
	}
	imageName, tag, err := coolify.imageRef(j.Slug, j.ID)
	if err != nil {
		return err
	}
	image := imageName + ":" + tag
	if j.Image != "" {
		image = j.Image
	}
	applicationUUID, deploymentUUID := j.CoolifyApp, j.CoolifyDeployment
	if deploymentUUID == "" {
		root := env("DATA_ROOT", "/var/lib/aihub/data")
		archive := filepath.Join(root, "artifacts", j.ID+".tar.gz")
		work, err := os.MkdirTemp("", "aihub-build-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(work)
		if err = extract(archive, work); err != nil {
			return err
		}
		cfg, err := readManifest(work)
		if err != nil {
			return err
		}
		if cfg.Web.Port == 0 {
			cfg.Web.Port = 3000
		}
		if cfg.Web.HealthPath == "" {
			cfg.Web.HealthPath = "/"
		}
		dockerfile, err := renderDockerfile(work, cfg)
		if err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(work, "Dockerfile"), []byte(dockerfile), 0600); err != nil {
			return err
		}
		image, err = coolify.publish(ctx, work, imageName, tag)
		if err != nil {
			logEvent(ctx, db, j.ID, err.Error())
			return err
		}
		logEvent(ctx, db, j.ID, "Built and pushed image "+image)
		if _, err = db.ExecContext(ctx, `UPDATE deployments SET image=$2 WHERE id=$1`, j.ID, image); err != nil {
			return err
		}
		applicationUUID, deploymentUUID, err = coolify.createOrUpdateAndDeploy(ctx, db, j, imageName, tag, cfg.Web.Port)
		if err != nil {
			return err
		}
		if _, err = db.ExecContext(ctx, `UPDATE deployments SET coolify_deployment_uuid=$2,status='deploying' WHERE id=$1`, j.ID, deploymentUUID); err != nil {
			return err
		}
		logEvent(ctx, db, j.ID, "Coolify deployment queued: "+deploymentUUID)
	}
	if applicationUUID == "" || deploymentUUID == "" {
		return fmt.Errorf("cannot resume deployment without Coolify application and deployment UUIDs")
	}
	_, err = coolify.waitDeployment(ctx, deploymentUUID)
	if err != nil {
		return err
	}
	return completeDeployment(ctx, db, j, coolify, applicationUUID, image)
}

func completeDeployment(ctx context.Context, db *sql.DB, j job, coolify coolifyConfig, applicationUUID, image string) error {
	app, err := coolify.application(ctx, applicationUUID)
	if err != nil {
		return err
	}
	serviceURL, _ := app["fqdn"].(string)
	if serviceURL == "" {
		if domains, ok := app["domains"].(string); ok {
			serviceURL = domains
		}
	}
	if serviceURL != "" && !strings.HasPrefix(serviceURL, "http://") && !strings.HasPrefix(serviceURL, "https://") {
		serviceURL = "https://" + strings.Split(serviceURL, ",")[0]
	}
	_, _ = db.ExecContext(ctx, `UPDATE deployments SET status='superseded' WHERE service_id=$1 AND id<>$2 AND status='active'`, j.ServiceID, j.ID)
	_, err = db.ExecContext(ctx, `UPDATE deployments SET status='active',image=$2,finished_at=now() WHERE id=$1`, j.ID, image)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `UPDATE services SET status='running',url=$2,updated_at=now() WHERE id=$1`, j.ServiceID, serviceURL)
	if err != nil {
		return err
	}
	_, _ = db.ExecContext(ctx, `UPDATE jobs SET status='done',locked_at=NULL WHERE deployment_id=$1`, j.ID)
	logEvent(ctx, db, j.ID, "Deployment is healthy at "+serviceURL)
	archive := filepath.Join(env("DATA_ROOT", "/var/lib/aihub/data"), "artifacts", j.ID+".tar.gz")
	_ = os.Remove(archive)
	return nil
}

func readManifest(root string) (manifest, error) {
	var m manifest
	path := filepath.Join(root, "platform.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return m, nil
		}
		return m, err
	}
	if err = yaml.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("invalid platform.yaml: %w", err)
	}
	return m, nil
}

func loadServiceEnv(ctx context.Context, db *sql.DB, serviceID string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT name,ciphertext FROM service_env WHERE service_id=$1 ORDER BY name`, serviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	vars := []string{}
	for rows.Next() {
		var name string
		var ciphertext []byte
		if err = rows.Scan(&name, &ciphertext); err != nil {
			return nil, err
		}
		value, e := secret.Decrypt(ciphertext)
		if e != nil {
			return nil, e
		}
		vars = append(vars, name+"="+value)
	}
	return vars, rows.Err()
}

func writeRoute(serviceID, slug, domain, container string, port int) error {
	root := env("ROUTES_ROOT", "/var/lib/aihub/routes")
	if err := os.MkdirAll(root, 0755); err != nil {
		return err
	}
	name := "svc-" + serviceID
	config := map[string]any{"http": map[string]any{
		"routers":  map[string]any{name: map[string]any{"rule": fmt.Sprintf("Host(`%s.%s`)", slug, domain), "entryPoints": []string{"web"}, "service": name}},
		"services": map[string]any{name: map[string]any{"loadBalancer": map[string]any{"servers": []map[string]string{{"url": fmt.Sprintf("http://%s:%d", container, port)}}}}},
	}}
	data, err := yaml.Marshal(config)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(root, ".route-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Chmod(0644); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, filepath.Join(root, serviceID+".yaml"))
}

func removeRoute(serviceID string) error {
	err := os.Remove(filepath.Join(env("ROUTES_ROOT", "/var/lib/aihub/routes"), serviceID+".yaml"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
func renderDockerfile(root string, m manifest) (string, error) {
	if _, err := os.Stat(filepath.Join(root, "Dockerfile")); err == nil {
		return "", fmt.Errorf("custom Dockerfile is not supported in v1; remove it and use platform.yaml")
	}
	if _, err := os.Stat(filepath.Join(root, "package.json")); err == nil {
		var pkg struct {
			Scripts map[string]string `json:"scripts"`
		}
		data, e := os.ReadFile(filepath.Join(root, "package.json"))
		if e != nil {
			return "", e
		}
		if json.Unmarshal(data, &pkg) != nil {
			return "", fmt.Errorf("package.json is invalid")
		}
		command := strings.TrimSpace(m.Web.Command)
		if command == "" {
			if pkg.Scripts["start"] == "" {
				return "", fmt.Errorf("Node project must define scripts.start or web.command in platform.yaml")
			}
			command = "npm start"
		}
		install := "npm install"
		if _, e = os.Stat(filepath.Join(root, "package-lock.json")); e == nil {
			install = "npm ci"
		}
		return fmt.Sprintf("FROM swr.cn-north-4.myhuaweicloud.com/ddn-k8s/docker.io/library/node:24-alpine\nWORKDIR /app\nCOPY package*.json ./\nRUN %s\nCOPY . .\nRUN mkdir -p /data && chown node:node /data\nUSER node\nENV PORT=%d HOST=0.0.0.0\nEXPOSE %d\nCMD [\"/bin/sh\",\"-lc\",%q]\n", install, m.Web.Port, m.Web.Port, command), nil
	}
	if _, err := os.Stat(filepath.Join(root, "requirements.txt")); err == nil {
		command := strings.TrimSpace(m.Web.Command)
		if command == "" {
			return "", fmt.Errorf("Python project must define web.command in platform.yaml")
		}
		return fmt.Sprintf("FROM swr.cn-north-4.myhuaweicloud.com/ddn-k8s/docker.io/library/python:3.13-alpine\nWORKDIR /app\nCOPY requirements.txt ./\nRUN pip install --no-cache-dir -r requirements.txt && adduser -D -u 10001 appuser && mkdir -p /data && chown appuser:appuser /data\nCOPY . .\nUSER appuser\nENV PORT=%d HOST=0.0.0.0\nEXPOSE %d\nCMD [\"/bin/sh\",\"-lc\",%q]\n", m.Web.Port, m.Web.Port, command), nil
	}
	return "", fmt.Errorf("could not identify a supported Node.js or Python project")
}

func extract(archive, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	base, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		name := filepath.Clean(filepath.FromSlash(h.Name))
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return fmt.Errorf("unsafe archive path")
		}
		target := filepath.Join(base, name)
		if !strings.HasPrefix(target, base+string(filepath.Separator)) && target != base {
			return fmt.Errorf("archive path escapes build context")
		}
		if h.Typeflag == tar.TypeDir {
			if err = os.MkdirAll(target, 0755); err != nil {
				return err
			}
			continue
		}
		if h.Typeflag != tar.TypeReg {
			return fmt.Errorf("unsupported archive entry")
		}
		if err = os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		mode := os.FileMode(h.Mode)&0755 | 0444
		out, e := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if e != nil {
			return e
		}
		_, e = io.CopyN(out, tr, h.Size)
		closeErr := out.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func (d docker) waitHealthy(ctx context.Context, id string) error {
	deadline := time.NewTimer(90 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("health check timed out after 90 seconds")
		case <-ticker.C:
			resp, err := d.request("GET", "/containers/"+id+"/json", nil, "")
			if err != nil {
				continue
			}
			var state struct {
				State struct {
					Status string `json:"Status"`
					Health struct {
						Status string `json:"Status"`
					} `json:"Health"`
				} `json:"State"`
			}
			err = json.NewDecoder(resp.Body).Decode(&state)
			resp.Body.Close()
			if err != nil {
				continue
			}
			if state.State.Health.Status == "healthy" {
				return nil
			}
			if state.State.Health.Status == "unhealthy" || state.State.Status == "exited" {
				return fmt.Errorf("container failed its health check")
			}
		}
	}
}
func fail(ctx context.Context, db *sql.DB, j job, cause error) {
	_, _ = db.ExecContext(ctx, `UPDATE deployments SET status='failed',error=$2,finished_at=now() WHERE id=$1`, j.ID, tail(cause.Error(), 1000))
	_, _ = db.ExecContext(ctx, `UPDATE jobs SET status='failed',locked_at=NULL WHERE deployment_id=$1`, j.ID)
	_, _ = db.ExecContext(ctx, `UPDATE services SET status=CASE WHEN EXISTS(SELECT 1 FROM deployments WHERE service_id=$1 AND id<>$2 AND status='active') THEN 'running' ELSE 'failed' END,updated_at=now() WHERE id=$1`, j.ServiceID, j.ID)
	logEvent(ctx, db, j.ID, cause.Error())
}

func runAction(ctx context.Context, db *sql.DB, j job) error {
	if j.CoolifyApp != "" {
		settings, err := loadCoolifyConfig(ctx, db)
		if err != nil {
			return err
		}
		path := "/applications/" + url.PathEscape(j.CoolifyApp) + "/" + j.Action
		if j.Action == "delete" {
			path = "/applications/" + url.PathEscape(j.CoolifyApp)
		}
		method := http.MethodPost
		var queued struct {
			DeploymentUUID string `json:"deployment_uuid"`
			Message        string `json:"message"`
		}
		if j.Action == "delete" {
			method = http.MethodDelete
		}
		var result any
		if j.Action == "start" || j.Action == "restart" {
			result = &queued
		}
		if err = settings.clientFor().request(ctx, method, path, map[string]any{"instant_deploy": true}, result); err != nil {
			return err
		}
		if queued.DeploymentUUID != "" {
			if _, err = settings.waitDeployment(ctx, queued.DeploymentUUID); err != nil {
				return err
			}
		}
		if j.Action == "delete" {
			if _, err = db.ExecContext(ctx, `UPDATE jobs SET status='done',locked_at=NULL WHERE id=$1`, j.QueueID); err != nil {
				return err
			}
			_, err = db.ExecContext(ctx, `DELETE FROM services WHERE id=$1`, j.ServiceID)
			return err
		}
		status := map[string]string{"start": "running", "restart": "running", "stop": "stopped"}[j.Action]
		_, err = db.ExecContext(ctx, `UPDATE services SET status=$2,updated_at=now() WHERE id=$1`, j.ServiceID, status)
		if err == nil {
			_, err = db.ExecContext(ctx, `UPDATE jobs SET status='done',locked_at=NULL WHERE id=$1`, j.QueueID)
		}
		return err
	}
	d := docker{base: strings.TrimRight(env("DOCKER_API", "http://docker-proxy-rw:2375"), "/"), client: &http.Client{Timeout: 20 * time.Second}}
	if j.OldContainer == "" && j.Action != "delete" {
		return fmt.Errorf("service has no active container")
	}
	switch j.Action {
	case "start":
		if err := d.start(j.OldContainer); err != nil {
			return err
		}
		if err := d.waitHealthy(ctx, j.OldContainer); err != nil {
			return err
		}
		_, err := db.ExecContext(ctx, `UPDATE services SET status='running',updated_at=now() WHERE id=$1`, j.ServiceID)
		if err != nil {
			return err
		}
	case "stop":
		if err := d.stop(j.OldContainer); err != nil {
			return err
		}
		_, err := db.ExecContext(ctx, `UPDATE services SET status='stopped',updated_at=now() WHERE id=$1`, j.ServiceID)
		if err != nil {
			return err
		}
	case "restart":
		if err := d.restart(j.OldContainer); err != nil {
			return err
		}
		if err := d.waitHealthy(ctx, j.OldContainer); err != nil {
			return err
		}
		_, err := db.ExecContext(ctx, `UPDATE services SET status='running',updated_at=now() WHERE id=$1`, j.ServiceID)
		if err != nil {
			return err
		}
	case "delete":
		if err := removeRoute(j.ServiceID); err != nil {
			return err
		}
		sleep(ctx, time.Second)
		if j.OldContainer != "" {
			_ = d.stop(j.OldContainer)
			if err := d.removeContainer(j.OldContainer); err != nil {
				return err
			}
		}
		if j.Network != "" {
			_ = d.disconnect("aihub-edge", j.Network)
			if err := d.removeNetwork(j.Network); err != nil {
				return err
			}
		}
		if err := d.removeVolume("aihub-data-" + j.ServiceID); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, `UPDATE jobs SET status='done',locked_at=NULL WHERE id=$1`, j.QueueID); err != nil {
			return err
		}
		_, err := db.ExecContext(ctx, `DELETE FROM services WHERE id=$1`, j.ServiceID)
		if err != nil {
			return err
		}
		return nil
	default:
		return fmt.Errorf("unsupported service action")
	}
	_, err := db.ExecContext(ctx, `UPDATE jobs SET status='done',locked_at=NULL WHERE id=$1`, j.QueueID)
	return err
}

func failAction(ctx context.Context, db *sql.DB, j job, cause error) {
	_, _ = db.ExecContext(ctx, `UPDATE jobs SET status='failed',locked_at=NULL WHERE id=$1`, j.QueueID)
	_, _ = db.ExecContext(ctx, `UPDATE services SET status=CASE WHEN container_id<>'' OR coolify_application_uuid<>'' THEN 'running' ELSE 'failed' END,updated_at=now() WHERE id=$1`, j.ServiceID)
	logf("action %s on service %s: %v", j.Action, j.ServiceID, cause)
}
func logEvent(ctx context.Context, db *sql.DB, messageID, message string) {
	_, _ = db.ExecContext(ctx, `INSERT INTO deployment_events(deployment_id,message) VALUES($1,$2)`, messageID, message)
}
func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
func env(k, f string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return f
}
func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}
func logf(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }

func (d docker) request(method, path string, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequest(method, d.base+"/v1.44"+path, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return d.client.Do(req)
}
func (d docker) load(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	resp, err := d.request("POST", "/images/load?quiet=1", f, "application/x-tar")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("docker image load: %s", string(b))
	}
	return nil
}
func (d docker) createNetwork(name string) error {
	body := strings.NewReader(fmt.Sprintf(`{"Name":%q,"CheckDuplicate":true,"Driver":"bridge","Internal":false,"Labels":{"aihub.managed":"true"}}`, name))
	resp, err := d.request("POST", "/networks/create", body, "application/json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 409 {
		return nil
	}
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("network create: %s", b)
	}
	return nil
}
func (d docker) createVolume(name string) error {
	body := strings.NewReader(fmt.Sprintf(`{"Name":%q,"Labels":{"aihub.managed":"true"}}`, name))
	resp, err := d.request("POST", "/volumes/create", body, "application/json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("volume create: %s", b)
	}
	return nil
}
func (d docker) connect(container, network string) error {
	b, _ := json.Marshal(map[string]string{"Container": container})
	resp, err := d.request("POST", "/networks/"+network+"/connect", strings.NewReader(string(b)), "application/json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		data, _ := io.ReadAll(resp.Body)
		if strings.Contains(strings.ToLower(string(data)), "already exists") {
			return nil
		}
		return fmt.Errorf("network connect: %s", data)
	}
	return nil
}
func (d docker) disconnect(container, network string) error {
	b, _ := json.Marshal(map[string]any{"Container": container, "Force": true})
	resp, err := d.request("POST", "/networks/"+network+"/disconnect", strings.NewReader(string(b)), "application/json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}
func (d docker) createContainer(name, image, network, volume string, labels map[string]string, port int, healthPath string, extraEnv []string) (string, error) {
	envVars := append([]string{fmt.Sprintf("PORT=%d", port), "HOST=0.0.0.0", "HOME=/tmp"}, extraEnv...)
	cfg := map[string]any{"Image": image, "Env": envVars, "Labels": labels, "ExposedPorts": map[string]any{fmt.Sprintf("%d/tcp", port): map[string]any{}}}
	cfg["Healthcheck"] = map[string]any{"Test": []string{"CMD", "wget", "-q", "-O", "/dev/null", fmt.Sprintf("http://127.0.0.1:%d%s", port, healthPath)}, "Interval": int64(2 * time.Second), "Timeout": int64(2 * time.Second), "Retries": 3, "StartPeriod": int64(10 * time.Second)}
	host := map[string]any{"NetworkMode": network, "Memory": 536870912, "MemorySwap": 536870912, "NanoCpus": 500000000, "PidsLimit": 128, "ReadonlyRootfs": true, "CapDrop": []string{"ALL"}, "SecurityOpt": []string{"no-new-privileges:true"}, "RestartPolicy": map[string]string{"Name": "unless-stopped"}, "Tmpfs": map[string]string{"/tmp": "rw,noexec,nosuid,size=64m"}, "Mounts": []map[string]string{{"Type": "volume", "Source": volume, "Target": "/data"}}, "LogConfig": map[string]any{"Type": "json-file", "Config": map[string]string{"max-size": "10m", "max-file": "3"}}}
	config := map[string]any{"Image": cfg["Image"], "Env": cfg["Env"], "Labels": cfg["Labels"], "ExposedPorts": cfg["ExposedPorts"], "Healthcheck": cfg["Healthcheck"], "HostConfig": host, "NetworkingConfig": map[string]any{"EndpointsConfig": map[string]any{network: map[string]any{}}}}
	b, _ := json.Marshal(config)
	resp, err := d.request("POST", "/containers/create?name="+name, strings.NewReader(string(b)), "application/json")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("container create: %s", data)
	}
	var result struct {
		ID string `json:"Id"`
	}
	if err = json.Unmarshal(data, &result); err != nil {
		return "", err
	}
	return result.ID, nil
}
func (d docker) start(id string) error {
	resp, err := d.request("POST", "/containers/"+id+"/start", nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode != 304 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("container start: %s", b)
	}
	return nil
}
func (d docker) stop(id string) error {
	resp, err := d.request("POST", "/containers/"+id+"/stop?t=15", nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode != 304 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("container stop: %s", b)
	}
	return nil
}
func (d docker) restart(id string) error {
	resp, err := d.request("POST", "/containers/"+id+"/restart?t=15", nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("container restart: %s", b)
	}
	return nil
}
func (d docker) removeContainer(id string) error {
	resp, err := d.request("DELETE", "/containers/"+id+"?force=1&v=0", nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}
func (d docker) removeNetwork(id string) error {
	resp, err := d.request("DELETE", "/networks/"+id, nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotFound {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("network remove: %s", b)
	}
	return nil
}
func (d docker) removeVolume(name string) error {
	resp, err := d.request("DELETE", "/volumes/"+name+"?force=1", nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotFound {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("volume remove: %s", b)
	}
	return nil
}
