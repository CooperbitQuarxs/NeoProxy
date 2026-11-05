package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

// neoPiSpeechMimeTypes is SPEECH_FORMAT_MIME_TYPES; its keys are SPEECH_FORMATS.
var neoPiSpeechMimeTypes = map[string]string{
	"mp3": "audio/mpeg", "wav": "audio/wav", "pcm": "audio/pcm", "opus": "audio/opus", "aac": "audio/aac", "flac": "audio/flac",
}

// neoPiSpeechErrorDetail is how much of a failed reply NeoPi quotes:
// detail.slice(0, 300), in UTF-16 code units.
const neoPiSpeechErrorDetail = 300

// executeSpeech is NeoPi's synthesizeOpenAiSpeech for an openai-speech row:
// one JSON request to the row's /audio/speech, answered with the audio bytes
// labelled with the effective format's MIME type. The request is read with
// NeoPi's speech-server contract; a request without response_format asks for
// mp3, the endpoint's default.
func (e *NeoPiCompletionsExecutor) executeSpeech(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	row := e.runnerRow(auth, req, "openai-speech")
	if row == nil {
		return resp, statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("%s: %s is not a speech model", e.provider, req.Model)}
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, row.ID, auth)
	defer reporter.TrackFailure(ctx, &err)
	payload, err := neoPiSpeechPayload(row.ID, req.Payload)
	if err != nil {
		return resp, statusErr{code: http.StatusBadRequest, msg: err.Error()}
	}
	// User payload rules are the last change to the business payload.
	payload, _, err = helps.ApplyMediaPayloadConfig(e.cfg, e.Identifier(), row.ID, "openai", payload, "application/json", req, opts)
	if err != nil {
		return resp, err
	}
	// The audio comes back in the format the finished payload names.
	format := "mp3"
	if value := gjson.GetBytes(payload, "response_format"); value.Type == gjson.String && neoPiSpeechMimeTypes[value.Str] != "" {
		format = value.Str
	}
	client := reporter.TrackHTTPClient(e.httpClient(ctx, auth))
	status, header, body, err := e.postRunner(ctx, auth, client, row, "/audio/speech", payload)
	if err != nil {
		return resp, err
	}
	if status < 200 || status >= 300 {
		message := fmt.Sprintf("%s/%s speech API failed (%d): %s", row.Provider, row.ID, status, neoPiUTF16Prefix(string(body), neoPiSpeechErrorDetail))
		return resp, &neoPiUpstreamError{code: status, body: message, headers: header}
	}
	// Speech endpoints report no usage.
	reporter.EnsurePublished(ctx)
	headers := header.Clone()
	headers.Set("Content-Type", neoPiSpeechMimeTypes[format])
	return cliproxyexecutor.Response{Payload: body, Headers: headers}, nil
}

// neoPiSpeechPayload reads an OpenAI speech request with NeoPi's
// speech-server contract (a non-empty input, an optional non-empty voice,
// one of SPEECH_FORMATS, a numeric speed, string instructions) and builds
// synthesizeOpenAiSpeech's payload as JSON.stringify writes it.
func neoPiSpeechPayload(model string, request []byte) ([]byte, error) {
	if !gjson.ValidBytes(request) || !gjson.ParseBytes(request).IsObject() {
		return nil, fmt.Errorf("speech: request must be a JSON object")
	}
	root := gjson.ParseBytes(request)
	input := root.Get("input")
	if input.Type != gjson.String || input.Str == "" {
		return nil, fmt.Errorf("speech: input must be a non-empty string")
	}
	format := "mp3"
	if value := root.Get("response_format"); value.Exists() {
		if value.Type != gjson.String || neoPiSpeechMimeTypes[value.Str] == "" {
			formats := make([]string, 0, len(neoPiSpeechMimeTypes))
			for name := range neoPiSpeechMimeTypes {
				formats = append(formats, name)
			}
			slices.Sort(formats)
			return nil, fmt.Errorf("speech: response_format must be one of %s", strings.Join(formats, ", "))
		}
		format = value.Str
	}
	fields := []jsjson.Field{
		{Name: "model", Value: jsjson.Marshal(model)},
		{Name: "input", Value: jsjson.Marshal(input.Str)},
		{Name: "response_format", Value: jsjson.Marshal(format)},
	}
	if voice := root.Get("voice"); voice.Exists() {
		if voice.Type != gjson.String || voice.Str == "" {
			return nil, fmt.Errorf("speech: voice must be a non-empty string")
		}
		fields = append(fields, jsjson.Field{Name: "voice", Value: jsjson.Marshal(voice.Str)})
	}
	if speed := root.Get("speed"); speed.Exists() {
		if speed.Type != gjson.Number {
			return nil, fmt.Errorf("speech: speed must be a number")
		}
		fields = append(fields, jsjson.Field{Name: "speed", Value: json.RawMessage(jsjson.Number(speed.Float()))})
	}
	if instructions := root.Get("instructions"); instructions.Exists() {
		if instructions.Type != gjson.String {
			return nil, fmt.Errorf("speech: instructions must be a string")
		}
		fields = append(fields, jsjson.Field{Name: "instructions", Value: jsjson.Marshal(instructions.Str)})
	}
	return jsjson.Object(fields...), nil
}

// neoPiUTF16Prefix is String.prototype.slice(0, units): the leading units
// UTF-16 code units of text. A surrogate pair the cut splits leaves its high
// half, which UTF-8 cannot carry, so it becomes U+FFFD.
func neoPiUTF16Prefix(text string, units int) string {
	var out strings.Builder
	for _, r := range text {
		width := utf16.RuneLen(r)
		if width < 0 {
			width = 1
		}
		if units < width {
			if units > 0 {
				out.WriteRune(utf8.RuneError)
			}
			break
		}
		units -= width
		out.WriteRune(r)
	}
	return out.String()
}
