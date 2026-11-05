package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

// neoPiSpeechMimeTypes is SPEECH_FORMAT_MIME_TYPES; its keys are SPEECH_FORMATS.
var neoPiSpeechMimeTypes = map[string]string{
	"mp3": "audio/mpeg", "wav": "audio/wav", "pcm": "audio/pcm", "opus": "audio/opus", "aac": "audio/aac", "flac": "audio/flac",
}

// neoPiSpeechBody is synthesizeOpenAiSpeech's payload, in NeoPi's key order.
type neoPiSpeechBody struct {
	Model          string   `json:"model"`
	Input          string   `json:"input"`
	ResponseFormat string   `json:"response_format"`
	Voice          *string  `json:"voice,omitempty"`
	Speed          *float64 `json:"speed,omitempty"`
	Instructions   *string  `json:"instructions,omitempty"`
}

// neoPiSpeechErrorDetail is the leading 300 characters of a failed reply,
// as NeoPi quotes it.
const neoPiSpeechErrorDetail = 300

// executeSpeech is NeoPi's synthesizeOpenAiSpeech for an openai-speech row:
// one JSON request to the row's /audio/speech, answered with the audio bytes
// labelled with the requested format's MIME type. An OpenAI speech request
// without response_format asks for mp3, the endpoint's default.
func (e *NeoPiCompletionsExecutor) executeSpeech(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	row := e.runnerRow(auth, req, "openai-speech")
	if row == nil {
		return resp, statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("%s: %s is not a speech model", e.provider, req.Model)}
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, row.ID, auth)
	defer reporter.TrackFailure(ctx, &err)
	payload, format, err := neoPiSpeechPayload(row.ID, req.Payload)
	if err != nil {
		return resp, statusErr{code: http.StatusBadRequest, msg: err.Error()}
	}
	// User payload rules are the last change to the business payload.
	payload, _, err = helps.ApplyMediaPayloadConfig(e.cfg, e.Identifier(), row.ID, "openai", payload, "application/json", req, opts)
	if err != nil {
		return resp, err
	}
	client := reporter.TrackHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0))
	status, header, body, err := e.postRunner(ctx, auth, client, row, "/audio/speech", payload)
	if err != nil {
		return resp, err
	}
	if status < 200 || status >= 300 {
		detail := []rune(string(body))
		detail = detail[:min(len(detail), neoPiSpeechErrorDetail)]
		return resp, statusErr{code: status, msg: fmt.Sprintf("%s/%s speech API failed (%d): %s", row.Provider, row.ID, status, string(detail))}
	}
	// Speech endpoints report no usage.
	reporter.EnsurePublished(ctx)
	headers := header.Clone()
	headers.Set("Content-Type", neoPiSpeechMimeTypes[format])
	return cliproxyexecutor.Response{Payload: body, Headers: headers}, nil
}

// neoPiSpeechPayload builds synthesizeOpenAiSpeech's payload from an OpenAI
// speech request and returns it with the requested format.
func neoPiSpeechPayload(model string, request []byte) ([]byte, string, error) {
	if !gjson.ValidBytes(request) {
		return nil, "", fmt.Errorf("speech request must be JSON")
	}
	root := gjson.ParseBytes(request)
	format := "mp3"
	if value := root.Get("response_format"); value.Exists() {
		format = value.String()
	}
	if _, ok := neoPiSpeechMimeTypes[format]; !ok {
		formats := make([]string, 0, len(neoPiSpeechMimeTypes))
		for name := range neoPiSpeechMimeTypes {
			formats = append(formats, name)
		}
		slices.Sort(formats)
		return nil, "", fmt.Errorf("response_format %q is not one of %v", format, formats)
	}
	body := neoPiSpeechBody{Model: model, Input: root.Get("input").String(), ResponseFormat: format}
	if voice := root.Get("voice"); voice.Type == gjson.String {
		value := voice.String()
		body.Voice = &value
	}
	if speed := root.Get("speed"); speed.Type == gjson.Number {
		value := speed.Float()
		body.Speed = &value
	}
	if instructions := root.Get("instructions"); instructions.Type == gjson.String {
		value := instructions.String()
		body.Instructions = &value
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(body); err != nil {
		return nil, "", err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), format, nil
}
