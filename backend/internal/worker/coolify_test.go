package worker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCoolifyClientSendsBearerAndDecodesResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/projects" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatal("missing bearer token")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"uuid":"project-1"}`))
	}))
	defer server.Close()
	client := (&coolifyConfig{BaseURL: server.URL, Token: "test-token"}).clientFor()
	var result struct {
		UUID string `json:"uuid"`
	}
	if err := client.request(context.Background(), http.MethodGet, "/projects", nil, &result); err != nil {
		t.Fatal(err)
	}
	if result.UUID != "project-1" {
		t.Fatalf("unexpected project UUID: %s", result.UUID)
	}
}

func TestImageRefUsesDeploymentIDAsImmutableTag(t *testing.T) {
	config := coolifyConfig{RegistryPrefix: "registry.example.com/aihub"}
	name, tag, err := config.imageRef("demo-app", "deploy-123")
	if err != nil {
		t.Fatal(err)
	}
	if name != "registry.example.com/aihub/demo-app" || tag != "deploy-123" {
		t.Fatalf("unexpected image reference: %s:%s", name, tag)
	}
	config.RegistryPrefix = "https://registry.example.com/aihub"
	if _, _, err = config.imageRef("demo-app", "deploy-123"); err == nil {
		t.Fatal("expected scheme to be rejected")
	}
}

func TestWaitDeploymentReturnsWhenCoolifyDeploymentIsFinished(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/deployments/deployment-123" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"finished","status_message":"healthy"}`))
	}))
	defer server.Close()

	config := coolifyConfig{BaseURL: server.URL, Token: "test-token"}
	message, err := config.waitDeployment(context.Background(), "deployment-123")
	if err != nil {
		t.Fatal(err)
	}
	if message != "healthy" {
		t.Fatalf("unexpected deployment status message: %q", message)
	}
}
