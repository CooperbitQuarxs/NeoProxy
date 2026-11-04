package kimi

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopiversion"
)

// deviceIDFileName is where NeoPi keeps the install's Kimi device id.
const deviceIDFileName = "kimi-device-id"

var deviceIDs sync.Map // auth dir -> device id

// deviceID is getDeviceId: the install's persisted id, created on first use. A
// missing or unwritable directory never fails a request; the id then lives
// for this process only.
func deviceID(authDir string) string {
	if cached, ok := deviceIDs.Load(authDir); ok {
		return cached.(string)
	}
	path := filepath.Join(authDir, deviceIDFileName)
	if data, err := os.ReadFile(path); err == nil {
		if existing := strings.TrimSpace(string(data)); existing != "" {
			actual, _ := deviceIDs.LoadOrStore(authDir, existing)
			return actual.(string)
		}
	}
	id := strings.ReplaceAll(uuid.NewString(), "-", "")
	if authDir != "" {
		if err := os.MkdirAll(authDir, 0o700); err == nil {
			_ = os.WriteFile(path, []byte(id+"\n"), 0o600)
		}
	}
	actual, _ := deviceIDs.LoadOrStore(authDir, id)
	return actual.(string)
}

// nodeArch is os.arch() for the running binary.
func nodeArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x64"
	case "386":
		return "ia32"
	default:
		return runtime.GOARCH
	}
}

// deviceModel is getDeviceModel: platform label, kernel release and arch.
func deviceModel() string {
	release, _ := unameReleaseVersion()
	label := runtime.GOOS
	switch runtime.GOOS {
	case "darwin":
		label = "macOS"
	case "windows":
		label = "Windows"
	case "linux":
		label = "Linux"
	}
	parts := []string{}
	for _, part := range []string{label, release, nodeArch()} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// sanitizeHeaderValue keeps printable ASCII, as NeoPi does for header values.
func sanitizeHeaderValue(value, fallback string) string {
	var out strings.Builder
	for _, r := range value {
		if r >= 0x20 && r <= 0x7e {
			out.WriteRune(r)
		}
	}
	if sanitized := strings.TrimSpace(out.String()); sanitized != "" {
		return sanitized
	}
	return fallback
}

// CommonHeaders is getKimiCommonHeaders: the KimiCLI identity and this
// device's fingerprint, sent on every Kimi auth, discovery and model request.
func CommonHeaders(authDir string) http.Header {
	hostname, _ := os.Hostname()
	_, version := unameReleaseVersion()
	h := make(http.Header)
	h.Set("User-Agent", "KimiCLI/"+neopiversion.Version)
	h.Set("X-Msh-Platform", "kimi_cli")
	h.Set("X-Msh-Version", neopiversion.Version)
	h.Set("X-Msh-Device-Name", sanitizeHeaderValue(hostname, "unknown"))
	h.Set("X-Msh-Device-Model", sanitizeHeaderValue(deviceModel(), "unknown"))
	h.Set("X-Msh-Os-Version", sanitizeHeaderValue(version, "unknown"))
	h.Set("X-Msh-Device-Id", sanitizeHeaderValue(deviceID(authDir), "unknown"))
	return h
}
