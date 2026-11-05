package executor

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/factorydroid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/googlewire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// factoryDroidGemini is the native Gemini wire bound to one attempt
// (streamFactoryDroidGemini): one POST to {base}/generate, generateContent
// SSE back, no empty-response retry.
type factoryDroidGemini struct {
	executor *FactoryDroidExecutor
	attempt  *factoryDroidAttempt
}

// factoryDroidGeminiPrepared is one shaped Factory Gemini request.
type factoryDroidGeminiPrepared struct {
	body    []byte
	headers http.Header
	url     string
	// byWire maps a returned call's wire name to the declared tool name.
	byWire map[string]string
}

// factoryDroidBlockReasons are the finish reasons the CLI reports as a
// content-filter block.
var factoryDroidBlockReasons = map[string]bool{"BLOCKLIST": true, "SAFETY": true, "RECITATION": true, "PROHIBITED_CONTENT": true,
	"SPII": true, "IMAGE_SAFETY": true, "IMAGE_PROHIBITED_CONTENT": true}

func (g *factoryDroidGemini) prepare(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*factoryDroidGeminiPrepared, error) {
	a, e := g.attempt, g.executor
	original := req.Payload
	if len(opts.OriginalRequest) > 0 {
		original = opts.OriginalRequest
	}
	// A native Gemini request is read as sent (thought parts and signatures
	// survive); every other dialect goes through the OpenAI Chat form.
	native := opts.SourceFormat == sdktranslator.FormatGemini || opts.SourceFormat == sdktranslator.FormatInteractions
	target := sdktranslator.FormatOpenAI
	if native {
		target = sdktranslator.FormatGemini
	}
	var originalTranslated, translated []byte
	var err error
	if opts.SourceFormat == sdktranslator.FormatGemini {
		translated = sdktranslator.NormalizeRequest(ctx, opts.SourceFormat, sdktranslator.FormatGemini, a.model.ID, req.Payload, true)
		originalTranslated = translated
		if !bytes.Equal(original, req.Payload) {
			originalTranslated = sdktranslator.NormalizeRequest(ctx, opts.SourceFormat, sdktranslator.FormatGemini, a.model.ID, original, true)
		}
	} else if originalTranslated, translated, err = helps.TranslateRequestPairReturningError(ctx, opts.Headers, e.cfg, opts.SourceFormat, target, a.model.ID, original, req.Payload, true, helps.APIKeyModelIsCompat(req)); err != nil {
		return nil, err
	}
	if translated, err = helps.ApplyRequestThinking(translated, req, opts, opts.SourceFormat.String(), target.String(), e.Identifier()); err != nil {
		return nil, err
	}
	nameMaxLength := 64
	if bound, ok := a.route.Catalog["toolCallNameMaxLength"].(float64); ok {
		nameMaxLength = int(bound)
	}
	// Turns a client replays are this model's own Factory Gemini output,
	// which NeoPi stamps google-generative-ai.
	source := &googlewire.Model{ID: a.model.ID, Provider: factorydroid.Provider, API: "google-generative-ai", Input: slices.Clone(a.model.Input),
		Reasoning: a.route.Reasoning, MaxTokens: a.maxTokens, NameMaxLength: nameMaxLength}
	var neoPiContext googlewire.Context
	var options googlewire.Options
	if native {
		neoPiContext, options, err = googlewire.FromGemini(translated, source)
	} else {
		neoPiContext, options, err = googlewire.FromOpenAIChat(translated, source)
	}
	if err != nil {
		return nil, statusErr{code: http.StatusBadRequest, msg: err.Error()}
	}
	neoPiContext.SystemPrompts = append([]string{helps.FactoryDroidSystemPrefix}, neoPiContext.SystemPrompts...)
	effort, off := a.effort(translated, target.String())
	var stops []string
	stopField := "stop"
	if native {
		stopField = "generationConfig.stopSequences"
	}
	if stop := gjson.GetBytes(translated, stopField); stop.Exists() {
		stops = []string{}
		if stop.Type == gjson.String {
			stops = append(stops, stop.String())
		}
		for _, entry := range stop.Array() {
			if entry.Type == gjson.String && stop.IsArray() {
				stops = append(stops, entry.String())
			}
		}
	}
	modelID := a.model.ID
	if a.row.RequestModelID != "" {
		modelID = a.row.RequestModelID
	}
	model := &googlewire.Model{ID: a.model.ID, Provider: factorydroid.Provider, API: "factory-droid-agent", Input: slices.Clone(a.model.Input),
		Reasoning: a.route.Reasoning, MaxTokens: a.maxTokens, NameMaxLength: nameMaxLength}
	body, byWire, err := helps.FactoryDroidGeminiBody(model, neoPiContext, helps.FactoryDroidGeminiOptions{ModelID: modelID,
		Temperature: options.Temperature, TopP: options.TopP, TopK: options.TopK, StopSequences: stops,
		Effort: effort, Disabled: off, LevelMedium: a.policyString("googleThinking") == "level-medium"})
	if err != nil {
		return nil, statusErr{code: http.StatusBadRequest, msg: err.Error()}
	}
	body = helps.NewPayloadFinalizer(e.cfg, e.Identifier(), a.model.ID, "gemini", "", originalTranslated, req, opts)(body)
	served := false
	for _, message := range neoPiContext.Messages {
		served = served || message.Role == googlewire.RoleAssistant
	}
	headers := make(http.Header)
	headers.Set("Accept", "*/*")
	headers.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	headers.Set("Authorization", "Bearer "+a.token)
	headers.Set("Connection", "keep-alive")
	headers.Set("Content-Type", "application/json")
	factoryDroidApplyIdentity(ctx, auth, opts.Headers, headers, a.identityHeaders(served))
	return &factoryDroidGeminiPrepared{body: body, headers: headers, url: a.baseURL + "/generate", byWire: byWire}, nil
}

// open posts the request; a non-2xx answer is the Factory Gemini error.
func (g *factoryDroidGemini) open(ctx context.Context, auth *cliproxyauth.Auth, prepared *factoryDroidGeminiPrepared, client *http.Client) (*http.Response, error) {
	e := g.executor
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, prepared.url, bytes.NewReader(prepared.body))
	if err != nil {
		return nil, err
	}
	request.Header = prepared.headers.Clone()
	info := helps.UpstreamRequestLog{URL: request.URL.String(), Method: request.Method, Headers: request.Header, Body: prepared.body, Provider: e.Identifier()}
	if auth != nil {
		info.AuthID, info.AuthLabel = auth.ID, auth.Label
		info.AuthType, info.AuthValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, info)
	response, err := client.Do(request)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, response.StatusCode, response.Header)
	decoded, errDecode := util.DecodeResponseBody(response.Body, response.Header.Get("Content-Encoding"))
	if errDecode != nil {
		_ = response.Body.Close()
		return nil, errDecode
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		response.Body = decoded
		return response, nil
	}
	data, _ := io.ReadAll(decoded)
	_ = decoded.Close()
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)
	return nil, statusErr{code: response.StatusCode, msg: fmt.Sprintf("Factory Gemini generate failed (%d): %s", response.StatusCode, googlewire.ExtractErrorMessage(string(data), response.StatusCode))}
}

// factoryDroidGeminiTerminal is the native terminal mapping: any tool call
// wins over every finish reason, a promptFeedback block reason comes next,
// then the last chunk's finish reason (STOP and MAX_TOKENS end cleanly). A
// stream with neither reason was truncated.
type factoryDroidGeminiTerminal struct {
	finish, block string
	toolCalls     bool
}

func (t *factoryDroidGeminiTerminal) observe(payload []byte) {
	root := gjson.ParseBytes(payload)
	if finish := root.Get("candidates.0.finishReason"); finish.Type == gjson.String {
		t.finish = finish.String()
	}
	if block := root.Get("promptFeedback.blockReason"); block.Type == gjson.String {
		t.block = block.String()
	}
	for _, part := range root.Get("candidates.0.content.parts").Array() {
		t.toolCalls = t.toolCalls || part.Get("functionCall").Exists()
	}
}

func (t *factoryDroidGeminiTerminal) err() error {
	switch {
	case t.toolCalls:
		return nil
	case t.block != "":
		return statusErr{code: http.StatusBadGateway, msg: "Generation was blocked by content filters (" + t.block + ")"}
	case t.finish == "":
		return statusErr{code: http.StatusBadGateway, msg: "Factory Droid Gemini stream closed before a finish_reason was received"}
	case t.finish == "STOP" || t.finish == "MAX_TOKENS":
		return nil
	case factoryDroidBlockReasons[t.finish]:
		return statusErr{code: http.StatusBadGateway, msg: "Generation was blocked by content filters (" + t.finish + ")"}
	case t.finish == "MALFORMED_FUNCTION_CALL":
		return statusErr{code: http.StatusBadGateway, msg: "Generation failed with finish reason: " + t.finish}
	}
	return statusErr{code: http.StatusBadGateway, msg: "Unknown finish reason: " + t.finish}
}

// scan relays every SSE data frame, a server-declared error ending it, with
// returned call names under the declared tool's name.
func (g *factoryDroidGemini) scan(ctx context.Context, body io.Reader, byWire map[string]string, emit func([]byte) error) (*factoryDroidGeminiTerminal, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(nil, streamScannerBuffer)
	terminal := &factoryDroidGeminiTerminal{}
	for scanner.Scan() {
		line := scanner.Bytes()
		helps.AppendAPIResponseChunk(ctx, g.executor.cfg, line)
		payload := helps.JSONPayload(line)
		if len(payload) == 0 {
			continue
		}
		if gjson.GetBytes(payload, "error").Exists() {
			return terminal, googlewire.StreamChunkError(payload)
		}
		for index, part := range gjson.GetBytes(payload, "candidates.0.content.parts").Array() {
			if name := part.Get("functionCall.name").String(); byWire[name] != "" && byWire[name] != name {
				payload = neoPiSet(payload, fmt.Sprintf("candidates.0.content.parts.%d.functionCall.name", index), byWire[name])
			}
		}
		terminal.observe(payload)
		if err := emit(bytes.Clone(payload)); err != nil {
			return terminal, err
		}
	}
	return terminal, scanner.Err()
}

// Execute aggregates the stream into one generateContent response.
func (g *factoryDroidGemini) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (result cliproxyexecutor.Response, err error) {
	e := g.executor
	prepared, err := g.prepare(ctx, auth, req, opts)
	if err != nil {
		return result, err
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, g.attempt.model.ID, auth)
	defer reporter.TrackFailure(ctx, &err)
	response, err := g.open(ctx, auth, prepared, reporter.TrackHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)))
	if err != nil {
		return result, err
	}
	defer func() { _ = response.Body.Close() }()
	var chunks [][]byte
	var usage helps.StreamUsageBuffer
	terminal, err := g.scan(ctx, response.Body, prepared.byWire, func(payload []byte) error {
		if detail, ok := helps.ParseGeminiStreamUsage(geminiNormalizeUsage(payload)); ok {
			usage.Observe(detail, true)
		}
		chunks = append(chunks, payload)
		return nil
	})
	if err == nil {
		err = terminal.err()
	}
	if err != nil {
		usage.PublishFailure(ctx, reporter, err)
		return result, err
	}
	data := geminiAggregateChunks(chunks)
	reporter.ObserveResponseModel(data)
	format := cliproxyexecutor.ResponseFormatOrSource(opts)
	var param any
	out := sdktranslator.TranslateNonStream(ctx, sdktranslator.FormatGemini, format, req.Model, helps.ApplyPatchOriginalRequest(req, opts), prepared.body, data, &param)
	if helps.ApplyPatchTranslationError(param) != nil || len(out) == 0 {
		return result, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
	}
	reporter.Publish(ctx, helps.ParseGeminiUsage(geminiNormalizeUsage(data)))
	if format == sdktranslator.FormatOpenAIResponse {
		out = helps.EnsureResponsesUsageDetails(out)
	}
	log.WithFields(log.Fields{"provider": e.Identifier(), "model": g.attempt.model.ID}).Info("provider request completed")
	return cliproxyexecutor.Response{Payload: out, Headers: response.Header.Clone()}, nil
}

// ExecuteStream relays the stream in the client's format.
func (g *factoryDroidGemini) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	e := g.executor
	prepared, err := g.prepare(ctx, auth, req, opts)
	if err != nil {
		return nil, err
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, g.attempt.model.ID, auth)
	defer reporter.TrackFailure(ctx, &err)
	response, err := g.open(ctx, auth, prepared, reporter.TrackHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)))
	if err != nil {
		return nil, err
	}
	out := make(chan cliproxyexecutor.StreamChunk, 1)
	go func() {
		defer close(out)
		defer func() { _ = response.Body.Close() }()
		format := cliproxyexecutor.ResponseFormatOrSource(opts)
		original := helps.ApplyPatchOriginalRequest(req, opts)
		gatewayErr := statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
		var usage helps.StreamUsageBuffer
		var param any
		helps.InitializeApplyPatchStream(ctx, sdktranslator.FormatGemini, format, req.Model, original, prepared.body, &param)
		emit := func(payload []byte) error {
			for _, line := range sdktranslator.TranslateStream(ctx, sdktranslator.FormatGemini, format, req.Model, original, prepared.body, payload, &param) {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: bytes.Clone(line)}:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			if helps.ApplyPatchTranslationError(param) != nil {
				return errApplyPatchStop
			}
			return nil
		}
		terminal, streamErr := g.scan(ctx, response.Body, prepared.byWire, func(payload []byte) error {
			if detail, ok := helps.ParseGeminiStreamUsage(geminiNormalizeUsage(payload)); ok {
				usage.Observe(detail, true)
			}
			reporter.ObserveResponseModel(payload)
			return emit(payload)
		})
		if errors.Is(streamErr, errApplyPatchStop) {
			helps.StopApplyPatchStream(ctx, param, reporter, out, gatewayErr)
			return
		}
		if streamErr == nil {
			streamErr = terminal.err()
		}
		if streamErr == nil {
			streamErr = emit([]byte("[DONE]"))
			if errors.Is(streamErr, errApplyPatchStop) {
				helps.StopApplyPatchStream(ctx, param, reporter, out, gatewayErr)
				return
			}
		}
		if streamErr != nil {
			if ctx.Err() != nil {
				return
			}
			helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
			usage.PublishFailure(ctx, reporter, streamErr)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
			case <-ctx.Done():
			}
			return
		}
		if helps.EndApplyPatchStream(ctx, param, reporter, out, gatewayErr) {
			return
		}
		usage.Publish(ctx, reporter)
		log.WithFields(log.Fields{"provider": e.Identifier(), "model": g.attempt.model.ID}).Info("provider request completed")
	}()
	return &cliproxyexecutor.StreamResult{Headers: response.Header.Clone(), Chunks: out}, nil
}
