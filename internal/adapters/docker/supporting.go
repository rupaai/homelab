package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/rupaai/homelab/internal/sharedir"
)

// SupportingService describes a hidden Homelab-managed container such as Pocket ID.
type SupportingService struct {
	Name          string
	Image         string
	HostPort      int
	ContainerPort int
	DataDir       string
	Environment   map[string]string
}

var supportName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,40}$`)

// EnsureSupportingService runs an internal service through the same Compose runner used
// for apps. The service is bound to loopback and has its own Compose project and data.
func (d *Deployer) EnsureSupportingService(ctx context.Context, s SupportingService) error {
	if !supportName.MatchString(s.Name) {
		return fmt.Errorf("invalid supporting service name")
	}
	if !strings.Contains(s.Image, ":") {
		return fmt.Errorf("supporting service image must be pinned to a version")
	}
	if s.HostPort < 1 || s.HostPort > 65535 || s.ContainerPort < 1 || s.ContainerPort > 65535 {
		return fmt.Errorf("supporting service ports are invalid")
	}
	if s.DataDir == "" {
		return fmt.Errorf("supporting service data folder is required")
	}
	root, err := filepath.Abs(s.DataDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	if err := sharedir.Open(root); err != nil {
		return err
	}
	compose, variables, err := supportingComposeYAML(s, root)
	if err != nil {
		return err
	}
	dir := filepath.Join(d.root, "..", "services", s.Name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(dir, "compose.yaml"), compose); err != nil {
		return err
	}
	_, err = d.composeProjectWithEnv(ctx, dir, "homelab-"+s.Name, s.Name, variables, "up", "-d", "--remove-orphans")
	return err
}

func supportingComposeYAML(s SupportingService, dataDir string) ([]byte, map[string]string, error) {
	if strings.ContainsAny(dataDir, "\n\r\x00") {
		return nil, nil, fmt.Errorf("supporting service data folder is invalid")
	}
	env, variables := map[string]string{}, map[string]string{}
	for k, v := range s.Environment {
		if !varRe.MatchString(k) || strings.HasPrefix(k, "COMPOSE_") || strings.ContainsAny(v, "\n\r\x00") {
			return nil, nil, fmt.Errorf("supporting service environment is invalid")
		}
		key := "HOMELAB_SERVICE_" + k
		env[k] = "${" + key + ":?required}"
		variables[key] = v
	}
	ports := []string{"127.0.0.1:" + strconv.Itoa(s.HostPort) + ":" + strconv.Itoa(s.ContainerPort)}
	service := map[string]any{"image": s.Image, "restart": "unless-stopped", "environment": env, "ports": ports, "volumes": []string{dollars(dataDir) + ":/app/data"}, "healthcheck": map[string]any{"test": []string{"CMD", "/app/pocket-id", "healthcheck"}, "interval": "30s", "timeout": "5s", "retries": 3, "start_period": "10s"}, "logging": map[string]any{"driver": "json-file", "options": map[string]string{"max-size": "10m", "max-file": "3"}}}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(map[string]any{"services": map[string]any{s.Name: service}}); err != nil {
		return nil, nil, err
	}
	return b.Bytes(), variables, nil
}
