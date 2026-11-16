package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type deploymentResult struct {
	DeploymentID string `json:"id"`
	URL          string `json:"url"`
	Status       string `json:"status"`
	Error        string `json:"error"`
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "deploy":
		deploy()
	case "status", "logs":
		readDeployment(os.Args[1])
	default:
		usage()
	}
}

func deploy() {
	fs := flag.NewFlagSet("deploy", flag.ExitOnError)
	slug := fs.String("slug", "", "service slug")
	name := fs.String("name", "", "display name")
	dir := fs.String("dir", ".", "project directory")
	base := fs.String("url", env("AIHUB_URL", "http://localhost:12688"), "platform URL")
	wait := fs.Bool("wait", true, "wait for the deployment to finish")
	fs.Parse(os.Args[2:])
	token := os.Getenv("AIHUB_TOKEN")
	if token == "" {
		fatal("AIHUB_TOKEN is required")
	}
	if *slug == "" {
		*slug = filepath.Base(mustAbs(*dir))
	}
	if !validSlug(*slug) {
		fatal("invalid --slug")
	}
	if *name == "" {
		*name = *slug
	}
	archive, err := os.CreateTemp("", "aihub-*.tar.gz")
	if err != nil {
		fatal(err.Error())
	}
	defer os.Remove(archive.Name())
	gz := gzip.NewWriter(archive)
	tw := tar.NewWriter(gz)
	root := mustAbs(*dir)
	ignorePatterns := loadIgnorePatterns(root)
	err = filepath.Walk(root, func(path string, info os.FileInfo, e error) error {
		if e != nil {
			return e
		}
		rel, e := filepath.Rel(root, path)
		if e != nil {
			return e
		}
		if rel == "." {
			return nil
		}
		if shouldIgnore(rel, info, ignorePatterns) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlinks are not supported: %s", rel)
		}
		h, e := tar.FileInfoHeader(info, "")
		if e != nil {
			return e
		}
		h.Name = filepath.ToSlash(rel)
		if e = tw.WriteHeader(h); e != nil {
			return e
		}
		if info.Mode().IsRegular() {
			f, e := os.Open(path)
			if e != nil {
				return e
			}
			_, e = io.Copy(tw, f)
			f.Close()
			return e
		}
		return nil
	})
	if err == nil {
		err = tw.Close()
	}
	if err == nil {
		err = gz.Close()
	}
	if err == nil {
		err = archive.Close()
	}
	if err != nil {
		fatal(err.Error())
	}
	f, err := os.Open(archive.Name())
	if err != nil {
		fatal(err.Error())
	}
	defer f.Close()
	body, writer := io.Pipe()
	mw := multipart.NewWriter(writer)
	go func() {
		part, e := mw.CreateFormFile("source", filepath.Base(*dir)+".tar.gz")
		if e == nil {
			_, e = io.Copy(part, f)
		}
		if e == nil {
			e = mw.WriteField("slug", *slug)
		}
		if e == nil {
			e = mw.WriteField("name", *name)
		}
		closeErr := mw.Close()
		if e == nil {
			e = closeErr
		}
		_ = writer.CloseWithError(e)
	}()
	idempotencyKey := make([]byte, 16)
	if _, err = rand.Read(idempotencyKey); err != nil {
		fatal(err.Error())
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(*base, "/")+"/api/v1/deployments", body)
	if err != nil {
		fatal(err.Error())
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", hex.EncodeToString(idempotencyKey))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fatal(err.Error())
	}
	defer resp.Body.Close()
	result, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		fmt.Fprintln(os.Stderr, string(result))
		os.Exit(1)
	}
	var created struct {
		DeploymentID string `json:"deployment_id"`
		URL          string `json:"url"`
		Status       string `json:"status"`
		Error        string `json:"error"`
	}
	_ = json.Unmarshal(result, &created)
	completed := deploymentResult{DeploymentID: created.DeploymentID, URL: created.URL, Status: created.Status, Error: created.Error}
	if *wait && created.DeploymentID != "" && created.Status != "active" && created.Status != "failed" {
		completed = waitForDeployment(*base, token, created.DeploymentID)
	}
	fmt.Printf("Deployment %s (%s)\n", completed.DeploymentID, completed.Status)
	if completed.URL != "" {
		fmt.Println(completed.URL)
	}
	if completed.Status == "failed" {
		fatal("deployment failed: " + completed.Error)
	}
	fmt.Println("Use aihubctl status --id", completed.DeploymentID)
}

func waitForDeployment(base, token, id string) deploymentResult {
	deadline := time.NewTimer(15 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		req, err := http.NewRequest(http.MethodGet, strings.TrimRight(base, "/")+"/api/v1/deployments/"+id, nil)
		if err == nil {
			req.Header.Set("Authorization", "Bearer "+token)
			resp, requestErr := (&http.Client{Timeout: 10 * time.Second}).Do(req)
			if requestErr == nil {
				var current deploymentResult
				decodeErr := json.NewDecoder(resp.Body).Decode(&current)
				resp.Body.Close()
				if decodeErr == nil && (current.Status == "active" || current.Status == "failed") {
					return current
				}
			}
		}
		select {
		case <-deadline.C:
			fatal("deployment is still running; check status with aihubctl status --id " + id)
		case <-ticker.C:
		}
	}
}

func readDeployment(command string) {
	fs := flag.NewFlagSet(command, flag.ExitOnError)
	id := fs.String("id", "", "deployment ID")
	base := fs.String("url", env("AIHUB_URL", "http://localhost:12688"), "platform URL")
	_ = fs.Parse(os.Args[2:])
	token := os.Getenv("AIHUB_TOKEN")
	if token == "" {
		fatal("AIHUB_TOKEN is required")
	}
	if *id == "" {
		fatal("--id is required")
	}
	path := "/api/v1/deployments/" + *id
	if command == "logs" {
		path += "/events"
	}
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(*base, "/")+path, nil)
	if err != nil {
		fatal(err.Error())
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fatal(err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		result, _ := io.ReadAll(resp.Body)
		fmt.Fprintln(os.Stderr, string(result))
		os.Exit(1)
	}
	if _, err := io.Copy(os.Stdout, resp.Body); err != nil {
		fatal(err.Error())
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "Usage: aihubctl deploy --slug <name> [--name <display>] [--dir <path>] | status --id <deployment-id> | logs --id <deployment-id>")
	os.Exit(2)
}
func mustAbs(p string) string {
	a, e := filepath.Abs(p)
	if e != nil {
		fatal(e.Error())
	}
	return a
}
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

func loadIgnorePatterns(root string) []string {
	file, err := os.Open(filepath.Join(root, ".platformignore"))
	if err != nil {
		return nil
	}
	defer file.Close()
	var patterns []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			patterns = append(patterns, filepath.ToSlash(strings.TrimPrefix(line, "/")))
		}
	}
	return patterns
}

func shouldIgnore(rel string, info os.FileInfo, patterns []string) bool {
	name := strings.ToLower(info.Name())
	slash := filepath.ToSlash(rel)
	for _, part := range strings.Split(slash, "/") {
		if part == ".git" || part == "node_modules" || part == ".venv" || part == "__pycache__" || part == ".pytest_cache" || part == ".next" {
			return true
		}
	}
	if strings.HasPrefix(name, ".env") && name != ".env.example" && name != ".env.sample" {
		return true
	}
	for _, suffix := range []string{".pem", ".key", ".p12", ".pfx", ".crt", ".cer"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	if name == "id_rsa" || name == "id_ed25519" || strings.Contains(name, "credentials") || strings.Contains(name, "secret") {
		return true
	}
	for _, pattern := range patterns {
		p := strings.TrimSuffix(pattern, "/")
		if p == "" {
			continue
		}
		if match, _ := filepath.Match(p, slash); match {
			return true
		}
		if match, _ := filepath.Match(p, name); match {
			return true
		}
		if info.IsDir() && strings.HasPrefix(slash, p+"/") {
			return true
		}
	}
	return false
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func fatal(s string) { fmt.Fprintln(os.Stderr, s); os.Exit(1) }
