package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/factorydroid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/googlewire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

// factoryDroidNow stamps a Gemini image response; tests pin it.
var factoryDroidNow = time.Now

// executeImage is generateFactoryDroidImage: GPT Image models call the
// proxy's OpenAI Images routes, a prompt alone on /images/generations and
// reference images inline on /images/edits; Gemini image models call the
// native Gemini route asking for TEXT and IMAGE output. Both carry chat's
// client identity. A reply without an image is an error. NeoPi's image
// transport does not stream.
func (e *FactoryDroidExecutor) executeImage(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	a, err := e.attempt(auth, req, opts)
	if err != nil {
		return resp, err
	}
	if !a.model.Policy.Image || (a.wire != factorydroid.WireResponses && a.wire != factorydroid.WireGoogle) {
		return resp, statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("Factory Droid %s is not an image generation model", a.model.ID)}
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, a.row.ID, auth)
	defer reporter.TrackFailure(ctx, &err)
	request, err := parseNeoPiImageRequest(req.Payload, opts.Headers.Get("Content-Type"))
	if err != nil {
		return resp, statusErr{code: http.StatusBadRequest, msg: err.Error()}
	}
	inputs, err := helps.FactoryDroidImageInputs(request.images)
	if err != nil {
		return resp, statusErr{code: http.StatusBadRequest, msg: err.Error()}
	}
	gemini := a.wire == factorydroid.WireGoogle
	if err = helps.FactoryDroidValidateReferences(factorydroid.Provider, inputs, !gemini && len(inputs) > 0); err != nil {
		return resp, statusErr{code: http.StatusBadRequest, msg: err.Error()}
	}
	model := a.row.ID
	if a.row.RequestModelID != "" {
		model = a.row.RequestModelID
	}
	var payload []byte
	url := strings.TrimRight(a.baseURL, "/")
	protocol := "openai"
	if gemini {
		imageConfig := gjson.GetBytes(req.Payload, "image_config")
		payload = helps.FactoryDroidGeminiImageBody(model, request.prompt, inputs, imageConfig.Get("aspect_ratio").String(), imageConfig.Get("image_size").String())
		url += "/generate"
		protocol = "gemini"
	} else {
		payload = helps.FactoryDroidImagesBody(model, request.prompt, request.count, inputs)
		if len(inputs) > 0 {
			url += "/images/edits"
		} else {
			url += "/images/generations"
		}
	}
	// User payload rules are the last change to the business payload.
	payload, _, err = helps.ApplyMediaPayloadConfig(e.cfg, e.Identifier(), a.row.ID, protocol, payload, "application/json", req, opts)
	if err != nil {
		return resp, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return resp, err
	}
	// Chat's request headers for the model's upstream, as the CLI's image
	// calls reuse them; the Gemini route accepts any type.
	httpReq.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	httpReq.Header.Set("Authorization", "Bearer "+a.token)
	httpReq.Header.Set("Connection", "keep-alive")
	httpReq.Header.Set("Content-Type", "application/json")
	factoryDroidApplyIdentity(ctx, auth, opts.Headers, httpReq.Header, a.identityHeaders(false))
	if gemini {
		httpReq.Header.Set("Accept", "*/*")
	}
	info := helps.UpstreamRequestLog{URL: url, Method: http.MethodPost, Headers: httpReq.Header.Clone(), Body: payload, Provider: e.Identifier()}
	if auth != nil {
		info.AuthID, info.AuthLabel = auth.ID, auth.Label
		info.AuthType, info.AuthValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, info)
	client := reporter.TrackHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0))
	response, err := client.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() { _ = response.Body.Close() }()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, response.StatusCode, response.Header.Clone())
	decoded, err := util.DecodeResponseBody(response.Body, response.Header.Get("Content-Encoding"))
	if err != nil {
		return resp, err
	}
	defer func() { _ = decoded.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(decoded)
		helps.AppendAPIResponseChunk(ctx, e.cfg, body)
		// The reply's headers ride along so its Retry-After reaches scheduling.
		message := fmt.Sprintf("%s/%s image request failed (%d): %s", factorydroid.Provider, a.model.ID, response.StatusCode, neoPiImageErrorMessage(body))
		return resp, factoryDroidRegionError(a.model.ID, &neoPiUpstreamError{code: response.StatusCode, body: message, headers: response.Header.Clone()})
	}
	var body []byte
	if gemini {
		body, err = e.geminiImage(ctx, a.model.ID, decoded)
	} else {
		body, err = e.openAIImage(ctx, a.model.ID, decoded)
	}
	if err != nil {
		return resp, err
	}
	reporter.ObserveResponseModel(body)
	reporter.Publish(ctx, helps.ParseOpenAIUsage(body))
	reporter.EnsurePublished(ctx)
	return cliproxyexecutor.Response{Payload: body, Headers: response.Header.Clone()}, nil
}

// openAIImage checks an Images API reply (parseImageApiResponse and
// decodeImageResponse): JSON with a data array holding an image.
func (e *FactoryDroidExecutor) openAIImage(ctx context.Context, modelID string, decoded io.Reader) ([]byte, error) {
	body, err := io.ReadAll(decoded)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, body)
	if !json.Valid(body) {
		return nil, statusErr{code: http.StatusBadGateway, msg: "Image API returned malformed JSON"}
	}
	if !gjson.GetBytes(body, "data").IsArray() {
		return nil, statusErr{code: http.StatusBadGateway, msg: "Image API response is missing data"}
	}
	if helps.FactoryDroidImagesCount(body) == 0 {
		return nil, statusErr{code: http.StatusBadGateway, msg: fmt.Sprintf("%s/%s returned no image: the response carried no image", factorydroid.Provider, modelID)}
	}
	return body, nil
}

// geminiImage folds the Gemini image stream into an Images API response: a
// server-declared error ends it, a prompt block ends without candidates,
// anything else must reach a terminal STOP and carry an image.
func (e *FactoryDroidExecutor) geminiImage(ctx context.Context, modelID string, decoded io.Reader) ([]byte, error) {
	scanner := bufio.NewScanner(decoded)
	scanner.Buffer(nil, streamScannerBuffer)
	result := &helps.FactoryDroidGeminiImageResult{}
	for scanner.Scan() {
		line := scanner.Bytes()
		helps.AppendAPIResponseChunk(ctx, e.cfg, line)
		payload := helps.JSONPayload(line)
		if len(payload) == 0 {
			continue
		}
		if gjson.GetBytes(payload, "error").Exists() {
			return nil, googlewire.StreamChunkError(payload)
		}
		result.Observe(payload)
	}
	if err := scanner.Err(); err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	if err := result.Terminal(); err != nil {
		return nil, statusErr{code: http.StatusBadGateway, msg: err.Error()}
	}
	if len(result.Images) == 0 {
		return nil, statusErr{code: http.StatusBadGateway, msg: fmt.Sprintf("%s/%s returned no image: %s", factorydroid.Provider, modelID, result.NoImageDetail())}
	}
	return result.ImagesResponse(factoryDroidNow().Unix()), nil
}
