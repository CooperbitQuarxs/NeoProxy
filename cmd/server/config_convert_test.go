package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestConfigConvertCommand(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(input, []byte("# Office proxy.\nport: 8317\napi-keys: [k]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runConfigCommand([]string{"convert", "--to", "toml", input}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	output := filepath.Join(dir, "config.toml")
	if !strings.Contains(stdout.String(), output) {
		t.Fatalf("stdout does not name the output: %q", stdout.String())
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.HasPrefix(text, "# Converted from config.yaml") || !strings.Contains(text, "# Office proxy.") || !strings.Contains(text, "port = 8317") {
		t.Fatalf("converted file:\n%s", text)
	}
	fromYAML, err := config.LoadConfig(input)
	if err != nil {
		t.Fatal(err)
	}
	fromTOML, err := config.LoadConfig(output)
	if err != nil {
		t.Fatal(err)
	}
	if fromYAML.Port != fromTOML.Port || len(fromTOML.APIKeys) != 1 || fromTOML.APIKeys[0] != "k" {
		t.Fatalf("converted config differs: yaml=%d %v toml=%d %v", fromYAML.Port, fromYAML.APIKeys, fromTOML.Port, fromTOML.APIKeys)
	}

	stderr.Reset()
	if code := runConfigCommand([]string{"convert", "--to", "toml", input}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "--force") {
		t.Fatalf("existing output must be refused: exit %d %s", code, stderr.String())
	}
	stderr.Reset()
	if code := runConfigCommand([]string{"convert", "--to", "toml", output, "-"}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "already toml") {
		t.Fatalf("same-format conversion must fail: exit %d %s", code, stderr.String())
	}
	if code := runConfigCommand([]string{"convert"}, &stdout, &stderr); code != 2 {
		t.Fatalf("missing --to: exit %d", code)
	}
}
