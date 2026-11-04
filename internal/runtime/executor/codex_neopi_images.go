package executor

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// codexNeoPiModel is the account's discovered NeoPi record for model id.
func codexNeoPiModel(auth *cliproxyauth.Auth, id string) (*registry.CodexModel, error) {
	if auth != nil {
		models, _ := registry.GetGlobalRegistry().GetModelsAndEpochForClient(auth.ID)
		for _, model := range models {
			if model != nil && model.Codex != nil && strings.EqualFold(model.ID, id) {
				return model.Codex, nil
			}
		}
	}
	return nil, fmt.Errorf("codex: model %s has no discovered NeoPi metadata", id)
}

// neoPiImageTurn shapes an Images API request as a NeoPi Codex turn on the
// image base model with the image_generation tool. NeoPi has no forced tool
// choice, so the request's tool_choice is dropped as NeoPi drops it.
func (e *CodexExecutor) neoPiImageTurn(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (context.Context, *codexNeoPiTurn, codexOpenAIImagePreparedRequest, string, error) {
	prepared, err := codexPrepareOpenAIImageRequest(req, opts)
	if err != nil {
		return ctx, nil, prepared, "", err
	}
	mainModel := e.resolveGPTImage2BaseModel()
	model, err := codexNeoPiModel(auth, mainModel)
	if err != nil {
		return ctx, nil, prepared, mainModel, err
	}
	body, err := sjson.SetBytes(prepared.Body, "model", mainModel)
	if err != nil {
		return ctx, nil, prepared, mainModel, err
	}
	payloadOpts := opts
	payloadOpts.SourceFormat = sdktranslator.FromString(codexOpenAIImageSourceFormat)
	ctx = helps.WithPayloadFinalizer(ctx, helps.NewPayloadFinalizer(e.cfg, e.Identifier(), mainModel, "codex", "", prepared.Body, req, payloadOpts))
	turn, err := e.shapeNeoPiCodex(auth, model, codexNeoPiSessionID(req, opts), body)
	return ctx, turn, prepared, mainModel, err
}

// executeNeoPiImage serves a non-streaming Images API request.
func (e *CodexExecutor) executeNeoPiImage(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	ctx, turn, prepared, mainModel, err := e.neoPiImageTurn(ctx, auth, req, opts)
	reporter := helps.NewExecutorUsageReporter(ctx, e, mainModel, auth)
	defer reporter.TrackFailure(ctx, &err)
	if err != nil {
		return resp, err
	}
	reporter.SetTranslatedReasoningEffort(turn.shaped.Body, "codex")
	itemsByIndex := make(map[int64][]byte)
	var fallback [][]byte
	var completed []byte
	err = e.runNeoPiCodex(ctx, auth, turn, codexNeoPiSessionID(req, opts), nil, func(event []byte) error {
		reporter.ObserveResponseModel(event)
		helps.AppendAPIResponseChunk(ctx, e.cfg, event)
		switch gjson.GetBytes(event, "type").String() {
		case "response.output_item.done":
			collectCodexOutputItemDone(event, itemsByIndex, &fallback)
		case "response.completed":
			completed = event
		}
		return nil
	})
	if err != nil {
		err = e.neoPiError(err)
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	if completed == nil {
		return resp, statusErr{code: http.StatusGatewayTimeout, msg: "stream error: stream disconnected before completion"}
	}
	if detail, ok := helps.ParseCodexUsage(completed); ok {
		reporter.Publish(ctx, detail)
	}
	publishCodexImageToolUsage(ctx, reporter, turn.shaped.Body, completed)
	results, createdAt, usageRaw, firstMeta, err := codexExtractImageResults(completed, itemsByIndex, fallback)
	if err != nil {
		return resp, err
	}
	if len(results) == 0 {
		return resp, statusErr{code: http.StatusBadGateway, msg: "upstream did not return image output"}
	}
	out, err := codexBuildImagesAPIResponse(results, createdAt, usageRaw, firstMeta, prepared.ResponseFormat)
	if err != nil {
		return resp, err
	}
	return cliproxyexecutor.Response{Payload: out, Headers: http.Header{}}, nil
}

// executeNeoPiImageStream serves a streaming Images API request: partial
// images as they arrive, then each completed image.
func (e *CodexExecutor) executeNeoPiImageStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	ctx, turn, prepared, mainModel, err := e.neoPiImageTurn(ctx, auth, req, opts)
	reporter := helps.NewExecutorUsageReporter(ctx, e, mainModel, auth)
	defer reporter.TrackFailure(ctx, &err)
	if err != nil {
		return nil, err
	}
	reporter.SetTranslatedReasoningEffort(turn.shaped.Body, "codex")
	itemsByIndex := make(map[int64][]byte)
	var fallback [][]byte
	return e.streamNeoPiCodex(ctx, auth, turn, codexNeoPiSessionID(req, opts), reporter, func(event []byte) ([][]byte, error) {
		reporter.ObserveResponseModel(event)
		helps.AppendAPIResponseChunk(ctx, e.cfg, event)
		switch gjson.GetBytes(event, "type").String() {
		case "response.output_item.done":
			collectCodexOutputItemDone(event, itemsByIndex, &fallback)
		case "response.image_generation_call.partial_image":
			if frame := codexBuildImagePartialFrame(event, prepared.ResponseFormat, prepared.StreamPrefix); len(frame) > 0 {
				return [][]byte{frame}, nil
			}
		case "response.completed":
			if detail, ok := helps.ParseCodexUsage(event); ok {
				reporter.Publish(ctx, detail)
			}
			publishCodexImageToolUsage(ctx, reporter, turn.shaped.Body, event)
			results, _, usageRaw, _, errExtract := codexExtractImageResults(event, itemsByIndex, fallback)
			if errExtract != nil {
				return nil, errExtract
			}
			if len(results) == 0 {
				return nil, statusErr{code: http.StatusBadGateway, msg: "upstream did not return image output"}
			}
			var frames [][]byte
			for _, img := range results {
				if frame := codexBuildImageCompletedFrame(img, usageRaw, prepared.ResponseFormat, prepared.StreamPrefix); len(frame) > 0 {
					frames = append(frames, frame)
				}
			}
			return frames, nil
		}
		return nil, nil
	})
}
