package api

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func (a *API) deployUpload(w http.ResponseWriter, r *http.Request) {
	if currentUser(r).Role == "viewer" {
		writeError(w, 403, "forbidden", "Read-only role")
		return
	}
	coolify, settingsErr := a.readCoolifySettings()
	if settingsErr != nil || coolify.BaseURL == "" || coolify.APIToken == "" || coolify.ProjectUUID == "" || coolify.EnvironmentUUID == "" || coolify.ServerUUID == "" || coolify.RegistryPrefix == "" || coolify.RegistryUsername == "" || coolify.RegistryPassword == "" {
		writeError(w, 503, "coolify_not_configured", "An administrator must configure and test the Coolify integration before deployments can be submitted")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
		writeError(w, 413, "upload_too_large", "Source archive must be under 256 MiB")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	file, _, err := r.FormFile("source")
	if err != nil {
		writeError(w, 400, "source_required", "Upload a .tar.gz archive in the source field")
		return
	}
	defer file.Close()
	slug := strings.ToLower(strings.TrimSpace(r.FormValue("slug")))
	if !validSlug(slug) {
		writeError(w, 400, "invalid_slug", "Slug must start with a letter and contain 2-31 lowercase letters, digits, or hyphens")
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = slug
	}
	owner := currentUser(r).ID
	rawKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	key := ""
	if rawKey != "" {
		key = digest(fmt.Sprintf("%d:%s", owner, rawKey))
	}
	if key != "" {
		var existing string
		if err := a.db.QueryRowContext(r.Context(), `SELECT id FROM deployments WHERE idempotency_key=$1`, key).Scan(&existing); err == nil {
			writeJSON(w, 202, map[string]string{"deployment_id": existing, "status": "queued"})
			return
		}
	}
	id := randomHex(16)
	dataRoot := env("DATA_ROOT", "/var/lib/aihub/data")
	artifactDir := filepath.Join(dataRoot, "artifacts")
	if err := os.MkdirAll(artifactDir, 0700); err != nil {
		writeError(w, 500, "storage_error", "Unable to prepare artifact storage")
		return
	}
	tmp, err := os.CreateTemp(artifactDir, "upload-*.tar.gz")
	if err != nil {
		writeError(w, 500, "storage_error", "Unable to receive source archive")
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	size, err := io.Copy(tmp, io.LimitReader(file, (256<<20)+1))
	closeErr := tmp.Close()
	if err != nil || closeErr != nil || size > 256<<20 {
		writeError(w, 413, "upload_too_large", "Source archive must be under 256 MiB")
		return
	}
	if err := validateTarGzip(tmpName); err != nil {
		writeError(w, 400, "unsafe_archive", err.Error())
		return
	}
	if err := os.Rename(tmpName, filepath.Join(artifactDir, id+".tar.gz")); err != nil {
		writeError(w, 500, "storage_error", "Unable to store source archive")
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, 500, "database_error", "Unable to start deployment")
		return
	}
	defer tx.Rollback()
	serviceID := randomHex(12)
	err = tx.QueryRowContext(r.Context(), `INSERT INTO services(id,slug,name,owner_id,status) VALUES($1,$2,$3,$4,'building') ON CONFLICT(slug) DO UPDATE SET name=EXCLUDED.name,updated_at=now() WHERE services.owner_id=EXCLUDED.owner_id RETURNING id`, serviceID, slug, name, owner).Scan(&serviceID)
	if err != nil {
		writeError(w, 409, "slug_conflict", "This service slug is already in use or unavailable")
		return
	}
	var sequence int
	if err = tx.QueryRowContext(r.Context(), `SELECT COALESCE(MAX(sequence),0)+1 FROM deployments WHERE service_id=$1`, serviceID).Scan(&sequence); err != nil {
		writeError(w, 500, "database_error", "Unable to allocate deployment version")
		return
	}
	if _, err = tx.ExecContext(r.Context(), `INSERT INTO deployments(id,service_id,sequence,status,idempotency_key) VALUES($1,$2,$3,'queued',NULLIF($4,''))`, id, serviceID, sequence, key); err != nil {
		writeError(w, 409, "duplicate_deployment", "Deployment request was already accepted")
		return
	}
	if _, err = tx.ExecContext(r.Context(), `INSERT INTO jobs(deployment_id) VALUES($1)`, id); err != nil {
		writeError(w, 500, "database_error", "Unable to queue deployment")
		return
	}
	if err = tx.Commit(); err != nil {
		writeError(w, 500, "database_error", "Unable to queue deployment")
		return
	}
	audit(r.Context(), a.db, owner, "deployment.create", id)
	writeJSON(w, 202, map[string]any{"deployment_id": id, "service_id": serviceID, "slug": slug, "sequence": sequence, "status": "queued"})
}

func validateTarGzip(file string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("source must be a valid gzip-compressed tar archive")
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	total := int64(0)
	count := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("invalid tar archive")
		}
		count++
		if count > 20000 {
			return fmt.Errorf("archive contains more than 20,000 entries")
		}
		name := filepath.ToSlash(h.Name)
		if filepath.IsAbs(h.Name) || name == ".." || strings.HasPrefix(name, "../") || strings.Contains(name, "/../") {
			return fmt.Errorf("archive contains an unsafe path")
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeDir {
			return fmt.Errorf("archive contains unsupported links or special files")
		}
		if h.Size < 0 || h.Size > 256<<20 {
			return fmt.Errorf("archive contains an oversized file")
		}
		total += h.Size
		if total > 1<<30 {
			return fmt.Errorf("uncompressed archive exceeds 1 GiB")
		}
	}
	if count == 0 {
		return fmt.Errorf("archive is empty")
	}
	return nil
}
