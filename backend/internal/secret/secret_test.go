package secret

import (
	"encoding/base64"
	"testing"
)

func TestEncryptDecrypt(t *testing.T) {
	t.Setenv("APP_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	ciphertext, err := Encrypt("internal-demo-value")
	if err != nil {
		t.Fatal(err)
	}
	if string(ciphertext) == "internal-demo-value" {
		t.Fatal("plaintext was stored")
	}
	plain, err := Decrypt(ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	if plain != "internal-demo-value" {
		t.Fatalf("unexpected plaintext %q", plain)
	}
}

func TestDecryptRejectsCorruptValue(t *testing.T) {
	t.Setenv("APP_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if _, err := Decrypt([]byte("bad")); err == nil {
		t.Fatal("expected corrupt ciphertext to be rejected")
	}
}
