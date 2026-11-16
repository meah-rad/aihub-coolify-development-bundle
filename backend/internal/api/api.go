package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type API struct {
	db  *sql.DB
	mux *http.ServeMux
}
type user struct {
	ID    int64  `json:"id"`
	Email string `json:"email"`
	Role  string `json:"role"`
}
type contextKey string

const userKey contextKey = "user"

var loginLimiter = struct {
	sync.Mutex
	attempts map[string][]time.Time
}{attempts: map[string][]time.Time{}}

func New(db *sql.DB) http.Handler {
	a := &API{db: db, mux: http.NewServeMux()}
	a.routes()
	return a.requestID(a.mux)
}

func (a *API) routes() {
	a.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	a.mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := a.db.PingContext(r.Context()); err != nil {
			writeError(w, 503, "database_unavailable", "Database is unavailable")
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ready"})
	})
	a.mux.HandleFunc("POST /api/v1/auth/login", a.login)
	a.mux.HandleFunc("POST /api/v1/auth/logout", a.requireUser(a.logout))
	a.mux.HandleFunc("GET /api/v1/auth/me", a.requireUser(a.me))
	a.mux.HandleFunc("GET /api/v1/admin/overview", a.requireRole("admin", a.overview))
	a.mux.HandleFunc("GET /api/v1/admin/integrations/coolify", a.requireRole("admin", a.getCoolifySettings))
	a.mux.HandleFunc("PUT /api/v1/admin/integrations/coolify", a.requireRole("admin", a.updateCoolifySettings))
	a.mux.HandleFunc("POST /api/v1/admin/integrations/coolify/test", a.requireRole("admin", a.testCoolifySettings))
	a.mux.HandleFunc("GET /api/v1/users", a.requireRole("admin", a.users))
	a.mux.HandleFunc("POST /api/v1/users", a.requireRole("admin", a.createUser))
	a.mux.HandleFunc("PATCH /api/v1/users/{id}", a.requireRole("admin", a.updateUser))
	a.mux.HandleFunc("GET /api/v1/services", a.requireUser(a.services))
	a.mux.HandleFunc("GET /api/v1/services/{id}", a.requireUser(a.service))
	a.mux.HandleFunc("GET /api/v1/services/{id}/env", a.requireUser(a.getServiceEnv))
	a.mux.HandleFunc("PATCH /api/v1/services/{id}/env", a.requireUser(a.patchServiceEnv))
	a.mux.HandleFunc("POST /api/v1/services/{id}/actions", a.requireUser(a.action))
	a.mux.HandleFunc("GET /api/v1/deployments", a.requireUser(a.deployments))
	a.mux.HandleFunc("POST /api/v1/deployments", a.requireUser(a.deployUpload))
	a.mux.HandleFunc("GET /api/v1/deployments/{id}", a.requireUser(a.deployment))
	a.mux.HandleFunc("GET /api/v1/deployments/{id}/events", a.requireUser(a.events))
	a.mux.HandleFunc("GET /api/v1/tokens", a.requireUser(a.tokens))
	a.mux.HandleFunc("POST /api/v1/tokens", a.requireUser(a.createToken))
	a.mux.HandleFunc("DELETE /api/v1/tokens/{id}", a.requireUser(a.revokeToken))
	a.mux.HandleFunc("GET /", a.static)
}

func (a *API) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := randomHex(8)
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r)
	})
}
func (a *API) requireUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") && (r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch || r.Method == http.MethodDelete) {
			csrf, err := r.Cookie("aihub_csrf")
			if err != nil || csrf.Value == "" || csrf.Value != r.Header.Get("X-CSRF-Token") {
				writeError(w, 403, "csrf_failed", "Missing or invalid CSRF token")
				return
			}
		}
		u, ok := a.authenticate(r)
		if !ok {
			writeError(w, 401, "unauthorized", "Please sign in")
			return
		}
		next(w, r.WithContext(contextWithUser(r, u)))
	}
}
func (a *API) requireRole(role string, next http.HandlerFunc) http.HandlerFunc {
	return a.requireUser(func(w http.ResponseWriter, r *http.Request) {
		if currentUser(r).Role != role {
			writeError(w, 403, "forbidden", "Administrator role required")
			return
		}
		next(w, r)
	})
}
func contextWithUser(r *http.Request, u user) context.Context {
	return context.WithValue(r.Context(), userKey, u)
}
func currentUser(r *http.Request) user { u, _ := r.Context().Value(userKey).(user); return u }

func (a *API) authenticate(r *http.Request) (user, bool) {
	var u user
	if raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "); raw != "" {
		h := digest(raw)
		err := a.db.QueryRowContext(r.Context(), `SELECT u.id,u.email,u.role FROM access_tokens t JOIN users u ON u.id=t.user_id WHERE t.token_hash=$1 AND t.revoked_at IS NULL AND u.disabled=false`, h).Scan(&u.ID, &u.Email, &u.Role)
		return u, err == nil
	}
	cookie, err := r.Cookie("aihub_session")
	if err != nil {
		return u, false
	}
	err = a.db.QueryRowContext(r.Context(), `SELECT u.id,u.email,u.role FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=$1 AND s.expires_at>now() AND u.disabled=false`, digest(cookie.Value)).Scan(&u.ID, &u.Email, &u.Role)
	return u, err == nil
}

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	var in struct{ Email, Password string }
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in) != nil {
		writeError(w, 400, "invalid_request", "Invalid JSON body")
		return
	}
	ip, _, splitErr := net.SplitHostPort(r.RemoteAddr)
	if splitErr != nil {
		ip = r.RemoteAddr
	}
	loginKey := ip + "|" + strings.ToLower(strings.TrimSpace(in.Email))
	if !allowLogin(loginKey) {
		writeError(w, 429, "login_rate_limited", "Too many login attempts; try again later")
		return
	}
	var u user
	var hash string
	err := a.db.QueryRowContext(r.Context(), `SELECT id,email,role,password_hash FROM users WHERE lower(email)=lower($1) AND disabled=false`, in.Email).Scan(&u.ID, &u.Email, &u.Role, &hash)
	if err != nil || !checkPassword(hash, in.Password) {
		writeError(w, 401, "invalid_credentials", "Email or password is incorrect")
		return
	}
	clearLoginFailures(loginKey)
	token := randomToken(32)
	_, err = a.db.ExecContext(r.Context(), `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,$3)`, digest(token), u.ID, time.Now().Add(12*time.Hour))
	if err != nil {
		writeError(w, 500, "session_error", "Unable to create session")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "aihub_session", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: strings.EqualFold(env("COOKIE_SECURE", "false"), "true"), Expires: time.Now().Add(12 * time.Hour)})
	http.SetCookie(w, &http.Cookie{Name: "aihub_csrf", Value: randomToken(24), Path: "/", SameSite: http.SameSiteLaxMode, Secure: strings.EqualFold(env("COOKIE_SECURE", "false"), "true"), Expires: time.Now().Add(12 * time.Hour)})
	writeJSON(w, 200, map[string]any{"user": u})
}

func allowLogin(ip string) bool {
	now := time.Now()
	window := now.Add(-15 * time.Minute)
	loginLimiter.Lock()
	defer loginLimiter.Unlock()
	kept := make([]time.Time, 0, 10)
	for _, at := range loginLimiter.attempts[ip] {
		if at.After(window) {
			kept = append(kept, at)
		}
	}
	if len(kept) >= 10 {
		loginLimiter.attempts[ip] = kept
		return false
	}
	loginLimiter.attempts[ip] = append(kept, now)
	return true
}
func clearLoginFailures(ip string) {
	loginLimiter.Lock()
	delete(loginLimiter.attempts, ip)
	loginLimiter.Unlock()
}
func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	if c, e := r.Cookie("aihub_session"); e == nil {
		_, _ = a.db.ExecContext(r.Context(), `DELETE FROM sessions WHERE token_hash=$1`, digest(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: "aihub_session", Value: "", Path: "/", HttpOnly: true, MaxAge: -1, SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, &http.Cookie{Name: "aihub_csrf", Value: "", Path: "/", MaxAge: -1, SameSite: http.SameSiteLaxMode})
	writeJSON(w, 200, map[string]string{"status": "ok"})
}
func (a *API) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"user": currentUser(r)})
}

func (a *API) overview(w http.ResponseWriter, r *http.Request) {
	var services, running, deployments, queued int
	_ = a.db.QueryRowContext(r.Context(), `SELECT count(*) FROM services`).Scan(&services)
	_ = a.db.QueryRowContext(r.Context(), `SELECT count(*) FROM services WHERE status='running'`).Scan(&running)
	_ = a.db.QueryRowContext(r.Context(), `SELECT count(*) FROM deployments`).Scan(&deployments)
	_ = a.db.QueryRowContext(r.Context(), `SELECT count(*) FROM jobs WHERE status='queued'`).Scan(&queued)
	writeJSON(w, 200, map[string]any{"services": services, "running": running, "deployments": deployments, "queued": queued, "host": a.hostMetrics(r.Context())})
}
func (a *API) services(w http.ResponseWriter, r *http.Request) {
	q := `SELECT id,slug,name,status,url,created_at,container_id,coolify_application_uuid FROM services`
	args := []any{}
	if currentUser(r).Role != "admin" {
		q += ` WHERE owner_id=$1`
		args = append(args, currentUser(r).ID)
	}
	q += ` ORDER BY updated_at DESC`
	rows, err := a.db.QueryContext(r.Context(), q, args...)
	if err != nil {
		writeError(w, 500, "database_error", "Unable to list services")
		return
	}
	defer rows.Close()
	type serviceRow struct {
		id, slug, name, status, url, container, coolifyApp string
		created                                            time.Time
	}
	rowsData := []serviceRow{}
	for rows.Next() {
		var id, slug, name, status, url string
		var container, coolifyApp string
		var created time.Time
		if rows.Scan(&id, &slug, &name, &status, &url, &created, &container, &coolifyApp) == nil {
			rowsData = append(rowsData, serviceRow{id: id, slug: slug, name: name, status: status, url: url, container: container, coolifyApp: coolifyApp, created: created})
		}
	}
	_ = rows.Close()
	coolifyBase, projectUUID, environmentUUID := a.coolifyLinkSettings(r.Context())
	out := make([]map[string]any, 0, len(rowsData))
	for _, s := range rowsData {
		stats := a.containerStats(r.Context(), s.container)
		out = append(out, map[string]any{"id": s.id, "slug": s.slug, "name": s.name, "status": s.status, "url": s.url, "coolify_application_uuid": s.coolifyApp, "coolify_url": coolifyAppURL(coolifyBase, projectUUID, environmentUUID, s.coolifyApp), "created_at": s.created, "cpu_percent": stats.CPUPct, "memory_bytes": stats.Memory, "pids": stats.PIDs, "resource_available": stats.Available})
	}
	writeJSON(w, 200, map[string]any{"items": out})
}
func (a *API) service(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var owner int64
	var item map[string]any
	var slug, name, status, url string
	var created time.Time
	var container string
	var coolifyApp string
	err := a.db.QueryRowContext(r.Context(), `SELECT owner_id,slug,name,status,url,created_at,container_id,coolify_application_uuid FROM services WHERE id=$1`, id).Scan(&owner, &slug, &name, &status, &url, &created, &container, &coolifyApp)
	if err != nil {
		writeError(w, 404, "not_found", "Service not found")
		return
	}
	if currentUser(r).Role != "admin" && owner != currentUser(r).ID {
		writeError(w, 404, "not_found", "Service not found")
		return
	}
	stats := a.containerStats(r.Context(), container)
	coolifyBase, projectUUID, environmentUUID := a.coolifyLinkSettings(r.Context())
	item = map[string]any{"id": id, "slug": slug, "name": name, "status": status, "url": url, "coolify_application_uuid": coolifyApp, "coolify_url": coolifyAppURL(coolifyBase, projectUUID, environmentUUID, coolifyApp), "created_at": created, "cpu_percent": stats.CPUPct, "memory_bytes": stats.Memory, "pids": stats.PIDs, "resource_available": stats.Available}
	writeJSON(w, 200, item)
}

func (a *API) submitDeployment(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 256<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeError(w, 413, "upload_too_large", "Upload must be under 256 MiB")
		return
	}
	source, header, err := r.FormFile("source")
	if err != nil {
		writeError(w, 400, "source_required", "Upload a source archive in the source field")
		return
	}
	defer source.Close()
	if header.Size > 256<<20 {
		writeError(w, 413, "upload_too_large", "Upload must be under 256 MiB")
		return
	}
	slug := strings.ToLower(r.FormValue("slug"))
	if !validSlug(slug) {
		writeError(w, 400, "invalid_slug", "Slug must be 2-31 lowercase letters, digits, or hyphens and start with a letter")
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = slug
	}
	key := r.Header.Get("Idempotency-Key")
	if key != "" {
		var id string
		err := a.db.QueryRowContext(r.Context(), `SELECT id FROM deployments WHERE id=$1`, key).Scan(&id)
		if err == nil {
			writeJSON(w, 202, map[string]string{"deployment_id": id, "status": "queued"})
			return
		}
	}
	_ = source
	writeError(w, 501, "build_unavailable", "Build worker is not enabled yet")
	log.Printf("deployment upload accepted for validation: %s (%s)", slug, header.Filename)
}
func (a *API) deployments(w http.ResponseWriter, r *http.Request) {
	q := `SELECT d.id,d.service_id,s.slug,d.sequence,d.status,d.error,d.created_at FROM deployments d JOIN services s ON s.id=d.service_id`
	args := []any{}
	if currentUser(r).Role != "admin" {
		q += ` WHERE s.owner_id=$1`
		args = append(args, currentUser(r).ID)
	}
	q += ` ORDER BY d.created_at DESC LIMIT 100`
	rows, err := a.db.QueryContext(r.Context(), q, args...)
	if err != nil {
		writeError(w, 500, "database_error", "Unable to list deployments")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, sid, slug, status, msg string
		var seq int
		var created time.Time
		if rows.Scan(&id, &sid, &slug, &seq, &status, &msg, &created) == nil {
			items = append(items, map[string]any{"id": id, "service_id": sid, "slug": slug, "sequence": seq, "status": status, "error": msg, "created_at": created})
		}
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (a *API) deployment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var owner int64
	var out map[string]any
	var sid, slug, serviceURL, status, msg string
	var seq int
	var created time.Time
	err := a.db.QueryRowContext(r.Context(), `SELECT s.owner_id,s.id,s.slug,s.url,d.status,d.error,d.sequence,d.created_at FROM deployments d JOIN services s ON s.id=d.service_id WHERE d.id=$1`, id).Scan(&owner, &sid, &slug, &serviceURL, &status, &msg, &seq, &created)
	if err != nil || (currentUser(r).Role != "admin" && owner != currentUser(r).ID) {
		writeError(w, 404, "not_found", "Deployment not found")
		return
	}
	out = map[string]any{"id": id, "service_id": sid, "slug": slug, "status": status, "error": msg, "sequence": seq, "url": serviceURL, "created_at": created}
	writeJSON(w, 200, out)
}
func (a *API) events(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var owner int64
	err := a.db.QueryRowContext(r.Context(), `SELECT owner_id FROM services WHERE id=(SELECT service_id FROM deployments WHERE id=$1)`, id).Scan(&owner)
	if err != nil || (currentUser(r).Role != "admin" && owner != currentUser(r).ID) {
		writeError(w, 404, "not_found", "Deployment not found")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	rows, err := a.db.QueryContext(r.Context(), `SELECT id,level,message,created_at FROM deployment_events WHERE deployment_id=$1 ORDER BY id`, id)
	if err != nil {
		return
	}
	defer rows.Close()
	f, ok := w.(http.Flusher)
	if !ok {
		return
	}
	for rows.Next() {
		var seq int64
		var level, msg string
		var created time.Time
		if rows.Scan(&seq, &level, &msg, &created) == nil {
			b, _ := json.Marshal(map[string]any{"id": seq, "level": level, "message": msg, "created_at": created})
			fmt.Fprintf(w, "id: %d\nevent: log\ndata: %s\n\n", seq, b)
			f.Flush()
		}
	}
	fmt.Fprint(w, "event: complete\ndata: {}\n\n")
}
func (a *API) action(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Action string `json:"action"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		writeError(w, 400, "invalid_request", "Invalid JSON body")
		return
	}
	if in.Action != "start" && in.Action != "stop" && in.Action != "restart" && in.Action != "delete" {
		writeError(w, 400, "invalid_action", "Unsupported action")
		return
	}
	if currentUser(r).Role == "viewer" {
		writeError(w, 403, "forbidden", "Read-only role")
		return
	}
	id := r.PathValue("id")
	var owner int64
	err := a.db.QueryRowContext(r.Context(), `SELECT owner_id FROM services WHERE id=$1`, id).Scan(&owner)
	if err != nil || (currentUser(r).Role != "admin" && owner != currentUser(r).ID) {
		writeError(w, 404, "not_found", "Service not found")
		return
	}
	var jobID int64
	err = a.db.QueryRowContext(r.Context(), `INSERT INTO jobs(kind,service_id,action) VALUES('action',$1,$2) RETURNING id`, id, in.Action).Scan(&jobID)
	if err != nil {
		writeError(w, 500, "queue_error", "Unable to queue service action")
		return
	}
	status := map[string]string{"start": "starting", "stop": "stopping", "restart": "restarting", "delete": "deleting"}[in.Action]
	_, _ = a.db.ExecContext(r.Context(), `UPDATE services SET status=$2,updated_at=now() WHERE id=$1`, id, status)
	audit(r.Context(), a.db, currentUser(r).ID, "service."+in.Action, id)
	writeJSON(w, 202, map[string]any{"job_id": jobID, "service_id": id, "action": in.Action, "status": "queued"})
}

func (a *API) tokens(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.QueryContext(r.Context(), `SELECT id,name,created_at,revoked_at FROM access_tokens WHERE user_id=$1 ORDER BY created_at DESC`, currentUser(r).ID)
	if err != nil {
		writeError(w, 500, "database_error", "Unable to list tokens")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var name string
		var created time.Time
		var revoked sql.NullTime
		if rows.Scan(&id, &name, &created, &revoked) == nil {
			items = append(items, map[string]any{"id": id, "name": name, "created_at": created, "revoked": revoked.Valid})
		}
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (a *API) createToken(w http.ResponseWriter, r *http.Request) {
	if currentUser(r).Role == "viewer" {
		writeError(w, 403, "forbidden", "Read-only role")
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		writeError(w, 400, "invalid_request", "Invalid JSON body")
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		writeError(w, 400, "name_required", "Token name is required")
		return
	}
	raw := randomToken(32)
	var id int64
	err := a.db.QueryRowContext(r.Context(), `INSERT INTO access_tokens(user_id,name,token_hash) VALUES($1,$2,$3) RETURNING id`, currentUser(r).ID, in.Name, digest(raw)).Scan(&id)
	if err != nil {
		writeError(w, 500, "database_error", "Unable to create token")
		return
	}
	audit(r.Context(), a.db, currentUser(r).ID, "token.create", fmt.Sprint(id))
	writeJSON(w, 201, map[string]any{"id": id, "name": in.Name, "token": raw})
}
func (a *API) revokeToken(w http.ResponseWriter, r *http.Request) {
	if currentUser(r).Role == "viewer" {
		writeError(w, 403, "forbidden", "Read-only role")
		return
	}
	_, err := a.db.ExecContext(r.Context(), `UPDATE access_tokens SET revoked_at=now() WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL`, r.PathValue("id"), currentUser(r).ID)
	if err != nil {
		writeError(w, 500, "database_error", "Unable to revoke token")
		return
	}
	writeJSON(w, 200, map[string]string{"status": "revoked"})
}

func (a *API) static(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		http.NotFound(w, r)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	http.ServeFile(w, r, "/app/static/"+path)
}
func audit(ctx context.Context, db *sql.DB, actor int64, action, resource string) {
	_, _ = db.ExecContext(ctx, `INSERT INTO audit_events(actor_id,action,resource) VALUES($1,$2,$3)`, actor, action, resource)
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"code": code, "message": message})
}
func digest(v string) string { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }
func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func randomHex(n int) string { return randomToken(n) }
func validSlug(s string) bool {
	if len(s) < 2 || len(s) > 31 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}
func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
func checkPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
