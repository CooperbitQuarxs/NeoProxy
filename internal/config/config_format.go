package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ConfigFormat names the on-disk syntax of a configuration file. TOML is the
// preferred format; YAML files keep loading unchanged.
//
// The loader, comment-preserving saves, v8 layout migration and management API
// all operate on a YAML node tree. A TOML file is converted to that tree when it
// is read and converted back when it is written, so every `yaml` struct tag and
// custom YAML decoder applies to both formats and a file keeps its format across
// every write.
type ConfigFormat string

const (
	ConfigFormatYAML ConfigFormat = "yaml"
	ConfigFormatTOML ConfigFormat = "toml"
)

// ParseConfigFormat accepts "toml", "yaml" or "yml", case-insensitively.
func ParseConfigFormat(name string) (ConfigFormat, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "toml":
		return ConfigFormatTOML, nil
	case "yaml", "yml":
		return ConfigFormatYAML, nil
	default:
		return "", fmt.Errorf("unknown config format %q (want toml or yaml)", name)
	}
}

// Extension returns the file extension conventionally used for f.
func (f ConfigFormat) Extension() string {
	if f == ConfigFormatTOML {
		return ".toml"
	}
	return ".yaml"
}

// ContentType returns the HTTP media type for a document in f.
func (f ConfigFormat) ContentType() string {
	if f == ConfigFormatTOML {
		return "application/toml; charset=utf-8"
	}
	return "application/yaml; charset=utf-8"
}

// DetectConfigFormat reports the format of a config document. The extension of
// path decides (.toml, or .yaml/.yml); other names are sniffed from data.
func DetectConfigFormat(path string, data []byte) ConfigFormat {
	if format, ok := configFormatFromExtension(path); ok {
		return format
	}
	return sniffConfigFormat(data)
}

func configFormatFromExtension(path string) (ConfigFormat, bool) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".toml":
		return ConfigFormatTOML, true
	case ".yaml", ".yml":
		return ConfigFormatYAML, true
	}
	return "", false
}

// sniffConfigFormat inspects the first significant line: a table header or a
// `key = value` line is TOML, anything else is YAML.
func sniffConfigFormat(data []byte) ConfigFormat {
	for _, line := range bytes.Split(data, []byte("\n")) {
		trimmed := strings.TrimSpace(string(line))
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			return ConfigFormatTOML
		}
		equals, colon := strings.Index(trimmed, "="), strings.Index(trimmed, ":")
		if equals > 0 && (colon < 0 || equals < colon) {
			return ConfigFormatTOML
		}
		return ConfigFormatYAML
	}
	return ConfigFormatYAML
}

// ConfigFileFormat reports the format of the config file at path, reading it
// only when the extension does not decide. A missing file of unknown extension
// is YAML.
func ConfigFileFormat(path string) ConfigFormat {
	if format, ok := configFormatFromExtension(path); ok {
		return format
	}
	data, _ := os.ReadFile(path)
	return sniffConfigFormat(data)
}

// ConfigToYAML returns the YAML document equivalent to data in format.
func ConfigToYAML(data []byte, format ConfigFormat) ([]byte, error) {
	if format != ConfigFormatTOML {
		return data, nil
	}
	return TOMLToYAML(data)
}

// ConfigFromYAML renders the YAML document yamlData in format.
func ConfigFromYAML(yamlData []byte, format ConfigFormat) ([]byte, error) {
	if format != ConfigFormatTOML {
		return yamlData, nil
	}
	return YAMLToTOML(yamlData)
}

// ReadConfigFile reads the config file at path and returns its YAML view, the
// bytes on disk (source hashes and ETags are computed from these) and its
// format. raw is nil only when the file could not be read.
func ReadConfigFile(path string) (yamlData, raw []byte, format ConfigFormat, err error) {
	raw, err = os.ReadFile(path)
	if err != nil {
		return nil, nil, "", err
	}
	format = DetectConfigFormat(path, raw)
	yamlData, err = ConfigToYAML(raw, format)
	if err != nil {
		return nil, raw, format, err
	}
	return yamlData, raw, format, nil
}

// EncodeConfigFile renders the YAML document yamlData in the format of the
// config file at path.
func EncodeConfigFile(path string, yamlData []byte) ([]byte, ConfigFormat, error) {
	format := ConfigFileFormat(path)
	data, err := ConfigFromYAML(yamlData, format)
	return data, format, err
}

// WriteConfigFile writes the YAML document yamlData to path in that file's format.
func WriteConfigFile(path string, yamlData []byte, perm os.FileMode) error {
	data, _, err := EncodeConfigFile(path, yamlData)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, perm)
}

// DefaultConfigFile returns the config file looked up in dir when none is named:
// config.toml when it exists, otherwise config.yaml.
func DefaultConfigFile(dir string) string {
	tomlPath := filepath.Join(dir, "config.toml")
	if _, err := os.Stat(tomlPath); err == nil || !errors.Is(err, os.ErrNotExist) {
		return tomlPath
	}
	return filepath.Join(dir, "config.yaml")
}

// ConvertConfigFile converts the config document data, read from a file in
// format from, to format to. Conflicting legacy aliases are dropped first, as a
// load would, so a null TOML cannot express never lets an overridden alias win.
// When source is not empty, a TOML result starts with a comment naming it.
func ConvertConfigFile(data []byte, from, to ConfigFormat, source string) ([]byte, error) {
	yamlData, err := ConfigToYAML(data, from)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(yamlData)) > 0 {
		if yamlData, _, err = NormalizeConfigLayout(yamlData, false); err != nil {
			return nil, err
		}
	}
	out, err := ConfigFromYAML(yamlData, to)
	if err != nil {
		return nil, err
	}
	if source != "" && to == ConfigFormatTOML {
		header := "# Converted from " + source + " by `neoproxy config convert --to toml`.\n" +
			"# TOML has no null: a setting left out takes its default. Commented-out\n" +
			"# examples keep their original YAML spelling; write them as TOML to enable them.\n\n"
		out = append([]byte(header), out...)
	}
	return out, nil
}
