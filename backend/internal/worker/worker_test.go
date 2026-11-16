package worker

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderNodeDockerfileUsesUnprivilegedRuntime(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"scripts":{"start":"node server.js"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var m manifest
	m.Web.Port = 3000
	dockerfile, err := renderDockerfile(root, m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dockerfile, "USER node") || !strings.Contains(dockerfile, "npm start") {
		t.Fatalf("unexpected Dockerfile: %s", dockerfile)
	}
}

func TestRenderPythonRequiresExplicitCommand(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "requirements.txt"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	var m manifest
	m.Web.Port = 8000
	if _, err := renderDockerfile(root, m); err == nil {
		t.Fatal("expected Python entrypoint requirement")
	}
	m.Web.Command = "python app.py"
	if _, err := renderDockerfile(root, m); err != nil {
		t.Fatal(err)
	}
}

func TestRenderRejectsUserDockerfile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("FROM scratch"), 0600); err != nil {
		t.Fatal(err)
	}
	var m manifest
	if _, err := renderDockerfile(root, m); err == nil {
		t.Fatal("expected custom Dockerfile rejection")
	}
}

func TestExtractMakesSourceReadableToRuntimeUser(t *testing.T) {
	root := t.TempDir()
	archive := filepath.Join(root, "src.tar.gz")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	content := []byte("console.log('ok')")
	if err = tw.WriteHeader(&tar.Header{Name: "src/server.js", Mode: 0600, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err = tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err = tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err = gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "out")
	if err = extract(archive, dest); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dest, "src", "server.js")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0444 != 0444 {
		t.Fatalf("expected runtime-readable source permissions, got %o", info.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != string(content) {
		t.Fatal("extracted content changed")
	}
}
