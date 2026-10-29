package config

import (
	"os"
	"path/filepath"
	"testing"
)

// Upgrading from an older schema must keep the saved API key.
func TestLoadKeepsAPIKeyAcrossSchemaUpgrade(t *testing.T) {
	root := t.TempDir()
	t.Setenv("APICHAT_ROOT", root)
	os.MkdirAll(filepath.Join(root, "data"), 0o755)
	old := `{"api_key":"sk-ant-test-123","port":47821,"profile":"guest","budget":150}`
	if err := os.WriteFile(filepath.Join(root, "data", "config.json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	c := Load()
	if c.APIKey != "sk-ant-test-123" || c.Profile != "guest" || c.Budget != 150 {
		t.Fatalf("settings lost: %+v", c)
	}
	if c.Schema != currentSchema {
		t.Fatalf("schema not migrated: %d", c.Schema)
	}
	again := Load()
	if again.APIKey != "sk-ant-test-123" {
		t.Fatal("key lost after migration save")
	}
	if _, err := os.Stat(filepath.Join(root, "data", "config.json.bak")); err != nil {
		t.Fatal("no backup written before migration")
	}
	if k, _ := c.Public()["api_key"].(string); k != "" {
		t.Fatal("Public() must not expose the key")
	}
}
