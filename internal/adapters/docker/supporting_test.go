package docker

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
)

func TestSupportingComposeKeepsSecretValuesOutOfFile(t *testing.T) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	apiKey := base64.RawURLEncoding.EncodeToString(raw[:])
	service := SupportingService{
		Name: "pocketid", Image: "ghcr.io/pocket-id/pocket-id:v2.18.0", HostPort: 7081, ContainerPort: 1411,
		Environment: map[string]string{"STATIC_API_KEY": apiKey, "APP_URL": "http://localhost:7081"},
	}
	compose, variables, err := supportingComposeYAML(service, "/Users/example/.homelab/services/pocketid/data")
	if err != nil {
		t.Fatal(err)
	}
	text := string(compose)
	if strings.Contains(text, apiKey) || !strings.Contains(text, "${HOMELAB_SERVICE_STATIC_API_KEY:?required}") || !strings.Contains(text, "127.0.0.1:7081:1411") {
		t.Fatalf("supporting service compose has incorrect exposure or secret handling: %s", text)
	}
	if variables["HOMELAB_SERVICE_STATIC_API_KEY"] != apiKey {
		t.Fatal("runtime API key was not passed to Compose")
	}
}

func TestSupportingComposeRequiresPinnedImage(t *testing.T) {
	service := SupportingService{Name: "pocketid", Image: "ghcr.io/pocket-id/pocket-id", HostPort: 7081, ContainerPort: 1411, DataDir: "/tmp/pocketid"}
	deployer := &Deployer{root: "/tmp/homelab-test-apps"}
	if err := deployer.EnsureSupportingService(context.Background(), service); err == nil {
		t.Fatal("unpinned supporting image was accepted")
	}
}
