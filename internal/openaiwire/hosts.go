package openaiwire

import (
	"runtime"
	"strings"
)

// Host identity NeoPi's OpenAI-compatible clients send beside the request
// itself (pi-catalog wire/cline-pass.ts and wire/coreweave.ts), shared by the
// request path and model discovery.

// ClinePassClientHeaders is clinePassClientHeaders: the official Cline CLI's
// client identity, which Cline's gateway requires for some roster entries.
// The task id (NeoPi's prompt-cache session) is omitted when there is none,
// as on discovery and account calls.
func ClinePassClientHeaders(taskID string) map[string]string {
	headers := map[string]string{
		"HTTP-Referer":       "https://cline.bot",
		"X-Title":            "Cline",
		"X-IS-MULTIROOT":     "false",
		"X-CLIENT-TYPE":      "cline-sdk",
		"User-Agent":         "Cline/3.0.58",
		"X-CLIENT-VERSION":   "3.0.58",
		"X-PLATFORM":         nodePlatform(runtime.GOOS),
		"X-PLATFORM-VERSION": "3.0.54",
		"X-CORE-VERSION":     "0.0.79",
	}
	if taskID != "" {
		headers["X-Task-ID"] = taskID
	}
	return headers
}

// nodePlatform is Node's process.platform for a Go GOOS.
func nodePlatform(goos string) string {
	if goos == "windows" {
		return "win32"
	}
	return goos
}

// CoreWeaveProjectHeader is COREWEAVE_PROJECT_HEADER, the W&B project a
// CoreWeave (W&B Inference) request bills to.
const CoreWeaveProjectHeader = "OpenAI-Project"

// ResolveCoreWeaveProject is resolveCoreWeaveProject: COREWEAVE_PROJECT, then
// WANDB_INFERENCE_PROJECT, then WANDB_PROJECT when it is already
// entity-qualified or WANDB_ENTITY qualifies it.
func ResolveCoreWeaveProject(getenv func(string) string) string {
	if project := strings.TrimSpace(getenv("COREWEAVE_PROJECT")); project != "" {
		return project
	}
	if project := strings.TrimSpace(getenv("WANDB_INFERENCE_PROJECT")); project != "" {
		return project
	}
	project := strings.TrimSpace(getenv("WANDB_PROJECT"))
	if project == "" {
		return ""
	}
	if strings.Contains(project, "/") {
		return project
	}
	if entity := strings.TrimSpace(getenv("WANDB_ENTITY")); entity != "" {
		return entity + "/" + project
	}
	return ""
}
