package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestShouldIgnoreSecretAndGeneratedFiles(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{".env.local", "deploy.key", "id_rsa", "node_modules", ".env.example", "server.js"} {
		if name == "node_modules" {
			if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(filepath.Join(root, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{".env.local", "deploy.key", "id_rsa", "node_modules"} {
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if !shouldIgnore(name, info, nil) {
			t.Errorf("expected %s to be excluded", name)
		}
	}
	for _, name := range []string{".env.example", "server.js"} {
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if shouldIgnore(name, info, nil) {
			t.Errorf("expected %s to be included", name)
		}
	}
}

func TestPlatformIgnorePatterns(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".platformignore"), []byte("dist/\n*.local\n# comment\n"), 0600); err != nil {
		t.Fatal(err)
	}
	patterns := loadIgnorePatterns(root)
	file := filepath.Join(root, "x.local")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(file)
	if !shouldIgnore("x.local", info, patterns) {
		t.Fatal("expected custom pattern to match")
	}
}
