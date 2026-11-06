// Package aws holds the AWS settings NeoPi's Amazon Bedrock transports read
// from the environment: the region (pi-ai utils/aws-profile.ts) and the
// Bedrock API key bearer (pi-ai registry/aws.ts).
package aws

import (
	"os"
	"path/filepath"
	"strings"
)

// DefaultRegion is the region NeoPi falls back to when neither the caller,
// the environment nor the shared config names one.
const DefaultRegion = "us-east-1"

// ResolveRegion is resolveAwsRegion: an explicit region, else the
// environment's (AWS_REGION, then AWS_DEFAULT_REGION), else the selected
// shared-config profile's, else us-east-1.
func ResolveRegion(explicit, profile string) string {
	if explicit != "" {
		return explicit
	}
	if region := AmbientRegion(profile); region != "" {
		return region
	}
	return DefaultRegion
}

// AmbientRegion is resolveAwsAmbientRegion: the region the environment or
// the active shared-config profile selects, or "".
func AmbientRegion(profile string) string {
	if region := os.Getenv("AWS_REGION"); region != "" {
		return region
	}
	if region := os.Getenv("AWS_DEFAULT_REGION"); region != "" {
		return region
	}
	return ProfileRegion(profile)
}

// ProfileRegion is resolveAwsProfileRegion: the selected profile's region in
// the shared config file, read only when the file participates.
func ProfileRegion(profile string) string {
	if !shouldLoadSharedConfig(profile) {
		return ""
	}
	data, err := os.ReadFile(sharedConfigPath())
	if err != nil {
		return ""
	}
	return parseIni(string(data))[resolveProfile(profile)]["region"]
}

// BearerToken is resolveAwsBearerToken for a stored key: the key itself, else
// AWS_BEARER_TOKEN_BEDROCK. "" means the transport signs with SigV4 instead.
func BearerToken(apiKey string) string {
	if apiKey != "" {
		return apiKey
	}
	return os.Getenv("AWS_BEARER_TOKEN_BEDROCK")
}

// resolveProfile is resolveAwsProfile.
func resolveProfile(profile string) string {
	if profile != "" {
		return profile
	}
	if env := os.Getenv("AWS_PROFILE"); env != "" {
		return env
	}
	return "default"
}

// shouldLoadSharedConfig is shouldLoadAwsSharedConfig: an explicit profile
// selection enables the shared config file; the implicit default profile
// follows the AWS SDK's AWS_SDK_LOAD_CONFIG opt-in.
func shouldLoadSharedConfig(profile string) bool {
	if profile != "" || os.Getenv("AWS_PROFILE") != "" {
		return true
	}
	value := strings.ToLower(os.Getenv("AWS_SDK_LOAD_CONFIG"))
	return value == "1" || value == "true"
}

// sharedConfigPath is awsSharedFilePaths' config file, honoring AWS_CONFIG_FILE.
func sharedConfigPath() string {
	if path := os.Getenv("AWS_CONFIG_FILE"); path != "" {
		return path
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".aws", "config")
}

// parseIni is parseAwsIni: sections with their `profile ` and `sso-session `
// prefixes normalized; comment lines start with # or ;.
func parseIni(text string) map[string]map[string]string {
	out := map[string]map[string]string{}
	var current map[string]string
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			name := strings.TrimSpace(line[1 : len(line)-1])
			if rest, ok := strings.CutPrefix(name, "profile "); ok {
				name = strings.TrimSpace(rest)
			}
			if rest, ok := strings.CutPrefix(name, "sso-session "); ok {
				name = "sso-session:" + strings.TrimSpace(rest)
			}
			if out[name] == nil {
				out[name] = map[string]string{}
			}
			current = out[name]
			continue
		}
		if current == nil {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		current[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return out
}

// EncodeURIComponent is JavaScript's encodeURIComponent, which NeoPi applies
// to a region before it enters an endpoint host.
func EncodeURIComponent(value string) string {
	const unreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.!~*'()"
	const hex = "0123456789ABCDEF"
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		if strings.IndexByte(unreserved, c) >= 0 {
			out.WriteByte(c)
			continue
		}
		out.WriteByte('%')
		out.WriteByte(hex[c>>4])
		out.WriteByte(hex[c&15])
	}
	return out.String()
}
