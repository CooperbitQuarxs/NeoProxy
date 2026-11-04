package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func readExample(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func decodeForComparison(t *testing.T, yamlData []byte) Config {
	t.Helper()
	var cfg Config
	applyLoadDefaults(&cfg)
	if err := yaml.Unmarshal(yamlData, &cfg); err != nil {
		t.Fatalf("decode: %v\n%s", err, yamlData)
	}
	return cfg
}

func sameConfig(a, b Config) bool {
	return equivalentConfigValues(reflect.ValueOf(&a).Elem(), reflect.ValueOf(&b).Elem())
}

// The shipped TOML example is the converter's output for the YAML example.
func TestConfigExampleTOMLIsGeneratedFromYAML(t *testing.T) {
	want, err := ConvertConfigFile(readExample(t, "config.example.yaml"), ConfigFormatYAML, ConfigFormatTOML, "config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if got := readExample(t, "config.example.toml"); string(got) != string(want) {
		t.Fatal("config.example.toml is stale; regenerate it with: neoproxy config convert --force --to toml config.example.yaml config.example.toml")
	}
}

// Loading the TOML example yields the same runtime config as the YAML example.
func TestLoadConfigTOMLExampleEquivalentToYAML(t *testing.T) {
	dir := t.TempDir()
	load := func(name string) *Config {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, readExample(t, "config.example."+strings.TrimPrefix(filepath.Ext(name), ".")), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadConfig(path)
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		cfg.SourceHash = ""
		return cfg
	}
	fromYAML, fromTOML := load("config.yaml"), load("config.toml")
	if !sameConfig(*fromYAML, *fromTOML) {
		t.Fatal("config.toml and config.yaml examples load differently")
	}
}

// YAML -> TOML -> YAML decodes identically, keeps comments, and is stable.
func TestTOMLRoundTripExample(t *testing.T) {
	source := readExample(t, "config.example.yaml")
	tomlData, err := YAMLToTOML(source)
	if err != nil {
		t.Fatal(err)
	}
	back, err := TOMLToYAML(tomlData)
	if err != nil {
		t.Fatal(err)
	}
	if !sameConfig(decodeForComparison(t, source), decodeForComparison(t, back)) {
		t.Fatal("YAML -> TOML -> YAML changed the decoded config")
	}
	for _, comment := range []string{"# Configuration template using the v8 layout.", "# round-robin (default), weighted-round-robin, fill-first"} {
		if !strings.Contains(string(tomlData), comment) || !strings.Contains(string(back), comment) {
			t.Fatalf("comment %q was lost", comment)
		}
	}
	again, err := YAMLToTOML(back)
	if err != nil {
		t.Fatal(err)
	}
	backAgain, err := TOMLToYAML(again)
	if err != nil {
		t.Fatal(err)
	}
	third, err := YAMLToTOML(backAgain)
	if err != nil {
		t.Fatal(err)
	}
	if string(third) != string(again) {
		t.Fatal("repeated TOML writes are not stable")
	}
}

func TestTOMLToYAMLValues(t *testing.T) {
	doc := `
title = "quote \" and \\ backslash"
"key.with.dots" = 1
unicode = "héllo"
multi = '''
line one
line two
'''
float = 1.0
big = 1e300
inf = inf
when = 1979-05-27T07:32:00Z
day = 1979-05-27
empty-array = []
empty-table = {}
dotted.inner = true

[[models]]
name = "a"
alias = ["x", "y"]

[[models.variants]]
id = 1

[[models]]
name = "b"
tags = [{ k = "v" }, { k = "w" }]
`
	yamlData, err := TOMLToYAML([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err = yaml.Unmarshal(yamlData, &got); err != nil {
		t.Fatalf("%v\n%s", err, yamlData)
	}
	if got["title"] != `quote " and \ backslash` || got["key.with.dots"] != 1 || got["unicode"] != "héllo" {
		t.Fatalf("strings and keys: %#v", got)
	}
	if got["multi"] != "line one\nline two\n" || got["float"] != 1.0 || got["big"] != 1e300 {
		t.Fatalf("multi/float: %#v %#v %#v", got["multi"], got["float"], got["big"])
	}
	if got["when"] != "1979-05-27T07:32:00Z" || got["day"] != "1979-05-27" {
		t.Fatalf("datetimes must become strings: %#v %#v", got["when"], got["day"])
	}
	if !reflect.DeepEqual(got["empty-array"], []any{}) || !reflect.DeepEqual(got["empty-table"], map[string]any{}) {
		t.Fatalf("empty collections: %#v %#v", got["empty-array"], got["empty-table"])
	}
	if !reflect.DeepEqual(got["dotted"], map[string]any{"inner": true}) {
		t.Fatalf("dotted key: %#v", got["dotted"])
	}
	models := got["models"].([]any)
	first, second := models[0].(map[string]any), models[1].(map[string]any)
	if first["name"] != "a" || !reflect.DeepEqual(first["variants"], []any{map[string]any{"id": 1}}) {
		t.Fatalf("nested array of tables: %#v", first)
	}
	if !reflect.DeepEqual(second["tags"], []any{map[string]any{"k": "v"}, map[string]any{"k": "w"}}) {
		t.Fatalf("inline tables in arrays: %#v", second)
	}
	// Key order follows the TOML document.
	if idx := strings.Index(string(yamlData), "title:"); idx < 0 || idx > strings.Index(string(yamlData), "unicode:") {
		t.Fatalf("key order lost:\n%s", yamlData)
	}

	// And back: the TOML rendering decodes to the same values.
	tomlAgain, err := YAMLToTOML(yamlData)
	if err != nil {
		t.Fatal(err)
	}
	yamlAgain, err := TOMLToYAML(tomlAgain)
	if err != nil {
		t.Fatalf("%v\n%s", err, tomlAgain)
	}
	var gotAgain map[string]any
	if err = yaml.Unmarshal(yamlAgain, &gotAgain); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, gotAgain) {
		t.Fatalf("TOML rendering changed values:\n%s", tomlAgain)
	}
}
func TestYAMLToTOMLNulls(t *testing.T) {
	// A null whose absence decodes the same is left out.
	out, err := YAMLToTOML([]byte("port: 8317\nproxy-url: null\n"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "proxy-url") || strings.Contains(string(out), "null") {
		t.Fatalf("null kept:\n%s", out)
	}
	// Opaque plugin settings receive null as a value, so it cannot be dropped.
	if _, err = YAMLToTOML([]byte("plugins:\n  configs:\n    demo:\n      value: null\n")); err == nil || !strings.Contains(err.Error(), "plugins.configs.demo.value") {
		t.Fatalf("plugin null must be rejected naming the key, got %v", err)
	}
	// A null scalar leaves the default in place either way, so it is dropped.
	if _, err = YAMLToTOML([]byte("ws-auth: null\n")); err != nil {
		t.Fatalf("ws-auth: null decodes like absence, got %v", err)
	}
	// A null that clears a list with a default cannot be expressed.
	if _, err = YAMLToTOML([]byte("discovery:\n  subtypes: null\n")); err == nil || !strings.Contains(err.Error(), "discovery.subtypes") {
		t.Fatalf("discovery.subtypes: null must be rejected naming the key, got %v", err)
	}
	// A null array element would shift its siblings.
	if _, err = YAMLToTOML([]byte("api-keys: [a, null]\n")); err == nil || !strings.Contains(err.Error(), "null array element") {
		t.Fatalf("null array element must be rejected, got %v", err)
	}
}

func TestYAMLToTOMLAnchorsAndQuoting(t *testing.T) {
	out, err := YAMLToTOML([]byte("base: &b {x: 1}\nderived:\n  <<: *b\n  y: \"true\"\nweird key: 2\n"))
	if err != nil {
		t.Fatal(err)
	}
	back, err := TOMLToYAML(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var got map[string]any
	if err = yaml.Unmarshal(back, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"base": map[string]any{"x": 1}, "derived": map[string]any{"x": 1, "y": "true"}, "weird key": 2}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v\n%s", got, out)
	}
}

func TestDetectConfigFormat(t *testing.T) {
	cases := []struct {
		path string
		data string
		want ConfigFormat
	}{
		{"config.toml", "port: 1", ConfigFormatTOML},
		{"config.yml", "port = 1", ConfigFormatYAML},
		{"config", "# comment\nport = 1\n", ConfigFormatTOML},
		{"config", "[server]\nport = 1\n", ConfigFormatTOML},
		{"config", "url: http://a=b\n", ConfigFormatYAML},
		{"config", "", ConfigFormatYAML},
	}
	for _, c := range cases {
		if got := DetectConfigFormat(c.path, []byte(c.data)); got != c.want {
			t.Errorf("DetectConfigFormat(%q, %q) = %s, want %s", c.path, c.data, got, c.want)
		}
	}
}

func TestDefaultConfigFilePrefersTOML(t *testing.T) {
	dir := t.TempDir()
	if got := DefaultConfigFile(dir); got != filepath.Join(dir, "config.yaml") {
		t.Fatalf("no files: %s", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if got := DefaultConfigFile(dir); got != filepath.Join(dir, "config.toml") {
		t.Fatalf("both files: %s", got)
	}
}

// Saves of a TOML-loaded config write TOML and keep its comments.
func TestSaveConfigPreserveCommentsKeepsTOML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	raw := "# Top comment.\nport = 8317 # the port\n\n# Debug logging.\ndebug = false\n"
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Debug = true
	if err = SaveConfigPreserveComments(path, cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"# Top comment.", "port = 8317 # the port", "# Debug logging.", "debug = true"} {
		if !strings.Contains(text, want) {
			t.Fatalf("saved TOML lacks %q:\n%s", want, text)
		}
	}
	reloaded, err := LoadConfig(path)
	if err != nil || !reloaded.Debug || reloaded.Port != 8317 {
		t.Fatalf("reload: %+v %v", reloaded, err)
	}
}

func TestLoadConfigTOMLSyntaxError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("port = \n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("want a positioned TOML error, got %v", err)
	}
	if cfg, err := LoadConfigOptional(path, true); err != nil || cfg == nil {
		t.Fatalf("optional load of invalid TOML: %v", err)
	}
}
