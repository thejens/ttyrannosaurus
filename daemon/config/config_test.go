package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_MissingCreatesDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 7071 {
		t.Errorf("expected port 7071, got %d", cfg.Port)
	}
	if len(cfg.Schemes) == 0 {
		t.Error("expected default schemes to be populated")
	}

	// File should have been created
	if _, err := os.Stat(filepath.Join(dir, ".config", "ttyrannosaurus", "config.yaml")); err != nil {
		t.Errorf("config file not created: %v", err)
	}
}

func TestLoad_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	original := &Config{
		Port: 8080,
		Schemes: map[string]*SchemeConfig{
			"test": {Templates: []Template{{Pattern: "foo", Command: []string{"echo", "foo"}}}},
		},
	}
	if err := Save(original); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Port != 8080 {
		t.Errorf("port: want 8080 got %d", loaded.Port)
	}
	sc := loaded.Schemes["test"]
	if sc == nil || len(sc.Templates) == 0 {
		t.Fatalf("scheme 'test' not round-tripped")
	}
	tmpl := sc.Templates[0]
	if tmpl.Pattern != "foo" || tmpl.Command[0] != "echo" {
		t.Errorf("template not round-tripped: %+v", tmpl)
	}
}
