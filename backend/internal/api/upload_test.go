package api

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestValidSlug(t *testing.T) {
	for _, slug := range []string{"web", "demo-2", "a1"} {
		if !validSlug(slug) {
			t.Errorf("expected %q to be valid", slug)
		}
	}
	for _, slug := range []string{"A-web", "-bad", "a", "bad/name", "bad_name", "this-name-is-way-too-long-to-be-valid"} {
		if validSlug(slug) {
			t.Errorf("expected %q to be invalid", slug)
		}
	}
}

func writeArchive(t *testing.T, name string, kind byte, link string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "src.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	if err = tw.WriteHeader(&tar.Header{Name: name, Typeflag: kind, Linkname: link, Mode: 0600, Size: 0}); err != nil {
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
	return path
}

func TestValidateTarGzipRejectsUnsafeEntries(t *testing.T) {
	for _, entry := range []struct {
		name string
		kind byte
		link string
	}{{"../secret", tar.TypeReg, ""}, {"link", tar.TypeSymlink, "../../secret"}, {"device", tar.TypeChar, ""}} {
		if err := validateTarGzip(writeArchive(t, entry.name, entry.kind, entry.link)); err == nil {
			t.Errorf("expected rejection for %q", entry.name)
		}
	}
}

func TestValidateTarGzipAcceptsRegularFile(t *testing.T) {
	if err := validateTarGzip(writeArchive(t, "src/index.js", tar.TypeReg, "")); err != nil {
		t.Fatal(err)
	}
}
