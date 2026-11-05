package helps

import "runtime"

// StainlessArch is mapStainlessArch for the running process: the
// X-Stainless-Arch value the Anthropic and OpenAI JavaScript SDKs send.
func StainlessArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x64"
	case "386":
		return "x86"
	case "arm64":
		return "arm64"
	default:
		return "other::" + runtime.GOARCH
	}
}

// StainlessOS is mapStainlessOs for the running process: the X-Stainless-OS
// value the Anthropic and OpenAI JavaScript SDKs send.
func StainlessOS() string {
	switch runtime.GOOS {
	case "linux":
		return "Linux"
	case "darwin":
		return "MacOS"
	case "windows":
		return "Windows"
	case "freebsd":
		return "FreeBSD"
	default:
		return "Other::" + runtime.GOOS
	}
}
