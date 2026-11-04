package anthropicnpi

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

var wireVersionState struct {
	sync.Mutex
	adopted string
}
var requiredVersion = regexp.MustCompile(`(?i)version (\d+\.\d+\.\d+) or newer is required`)

// WireVersion follows NeoPi's explicit override, server adoption, pinned default
// precedence. It never changes the existing claude provider's fingerprint.
func WireVersion() string {
	if value := os.Getenv("PI_AI_CLAUDE_CODE_VERSION"); value != "" {
		return value
	}
	wireVersionState.Lock()
	defer wireVersionState.Unlock()
	if wireVersionState.adopted != "" {
		return wireVersionState.adopted
	}
	return CodeVersion
}

func AdoptRequiredVersion(message string) bool {
	if os.Getenv("PI_AI_CLAUDE_CODE_VERSION") != "" || !strings.Contains(message, "claude_code_version_too_old") {
		return false
	}
	match := requiredVersion.FindStringSubmatch(message)
	if len(match) != 2 {
		return false
	}
	wireVersionState.Lock()
	defer wireVersionState.Unlock()
	current := wireVersionState.adopted
	if current == "" {
		current = CodeVersion
	}
	oldParts, nextParts := strings.Split(current, "."), strings.Split(match[1], ".")
	if len(oldParts) != 3 {
		return false
	}
	for index := range oldParts {
		old, oldErr := strconv.Atoi(oldParts[index])
		next, nextErr := strconv.Atoi(nextParts[index])
		if oldErr != nil || nextErr != nil || next < old {
			return false
		}
		if next > old {
			wireVersionState.adopted = match[1]
			return true
		}
	}
	return false
}
