package executor

import (
	"bytes"
	"strings"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func claudeCreds(a *cliproxyauth.Auth) (apiKey, baseURL string) {
	if a == nil {
		return "", ""
	}
	if a.Attributes != nil {
		apiKey = a.Attributes["api_key"]
		baseURL = a.Attributes["base_url"]
	}
	if apiKey == "" {
		apiKey, _ = a.Metadata["access_token"].(string)
	}
	return
}

func observeClaudeStreamJSONPayload(payload []byte, hasDataPrefix bool, messageID *string, completed *bool) {
	root := gjson.ParseBytes(payload)
	switch root.Get("type").String() {
	case "message_start":
		if id := strings.TrimSpace(root.Get("message.id").String()); id != "" {
			*messageID = id
		}
	case "message_stop":
		*completed = true
	case "message":
		if !hasDataPrefix && strings.TrimSpace(root.Get("stop_reason").String()) != "" {
			if id := strings.TrimSpace(root.Get("id").String()); id != "" {
				*messageID = id
			}
			*completed = true
		}
	}
}

func observeClaudeSSEEvent(rawEvent []byte, messageID *string, completed *bool, streamErr *error) {
	var dataLines [][]byte
	for _, line := range bytes.Split(rawEvent, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if !bytes.HasPrefix(trimmed, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(trimmed[len("data:"):])
		if len(payload) > 0 {
			dataLines = append(dataLines, payload)
		}
	}
	if len(dataLines) == 0 {
		trimmed := bytes.TrimSpace(rawEvent)
		if bytes.HasPrefix(trimmed, []byte("{")) && gjson.ValidBytes(trimmed) {
			observeClaudeStreamJSONPayload(trimmed, false, messageID, completed)
		}
		return
	}
	joined := bytes.TrimSpace(bytes.Join(dataLines, []byte("\n")))
	if !gjson.ValidBytes(joined) {
		return
	}
	observeClaudeStreamJSONPayload(joined, true, messageID, completed)
	if streamErr != nil && *streamErr == nil && gjson.ParseBytes(joined).Get("type").String() == "error" {
		*streamErr = statusErr{code: 502, msg: string(joined)}
	}
}
