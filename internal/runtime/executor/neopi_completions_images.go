package executor

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopitaxonomy"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/openaiwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

// neoPiImageReference is one input_references entry of an edit.
type neoPiImageReference struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

// neoPiImageBody is generateOpenAIImage's request body, in NeoPi's key order.
type neoPiImageBody struct {
	Model           string                `json:"model"`
	Prompt          string                `json:"prompt"`
	N               int64                 `json:"n"`
	ResponseFormat  string                `json:"response_format,omitempty"`
	Size            string                `json:"size,omitempty"`
	InputReferences []neoPiImageReference `json:"input_references,omitempty"`
}

// neoPiImageRequest is the part of an OpenAI images request NeoPi's
// ImageGenerationRequest carries: the prompt, the image count, the size, and
// the reference images of an edit as data URLs.
type neoPiImageRequest struct {
	prompt string
	count  int64
	size   string
	images []string
}

// executeImage is NeoPi's generateOpenAIImage for an openai-images row: one
// JSON generation, or for an edit with reference images a JSON edit that
// falls back to the generation endpoint when the host answers 404. NeoPi's
// image transport does not stream.
func (e *NeoPiCompletionsExecutor) executeImage(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, endpointPath string) (resp cliproxyexecutor.Response, err error) {
	row := e.runnerRow(auth, req, "openai-images")
	if row == nil {
		return resp, statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("%s: %s is not an image model", e.provider, req.Model)}
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, row.ID, auth)
	defer reporter.TrackFailure(ctx, &err)
	request, err := parseNeoPiImageRequest(req.Payload, opts.Headers.Get("Content-Type"))
	if err != nil {
		return resp, statusErr{code: http.StatusBadRequest, msg: err.Error()}
	}
	edit := endpointPath == openAICompatImagesEditsPath && len(request.images) > 0
	payload, err := neoPiImagePayload(row, request, edit)
	if err != nil {
		return resp, err
	}
	// User payload rules are the last change to the business payload.
	payload, _, err = helps.ApplyMediaPayloadConfig(e.cfg, e.Identifier(), row.ID, "openai", payload, "application/json", req, opts)
	if err != nil {
		return resp, err
	}
	client := reporter.TrackHTTPClient(e.httpClient(ctx, auth))
	path := openAICompatImagesGenerationsPath
	if edit {
		path = openAICompatImagesEditsPath
	}
	status, header, body, err := e.postRunner(ctx, auth, client, row, path, payload)
	if err == nil && edit && status == http.StatusNotFound {
		status, header, body, err = e.postRunner(ctx, auth, client, row, openAICompatImagesGenerationsPath, payload)
	}
	if err != nil {
		return resp, err
	}
	if status < 200 || status >= 300 {
		// The reply's headers ride along so its Retry-After reaches scheduling.
		message := fmt.Sprintf("%s/%s image request failed (%d): %s", row.Provider, row.ID, status, neoPiImageErrorMessage(body))
		return resp, &neoPiUpstreamError{code: status, body: message, headers: header}
	}
	if !json.Valid(body) {
		return resp, statusErr{code: http.StatusBadGateway, msg: "Image API returned malformed JSON"}
	}
	if !gjson.GetBytes(body, "data").IsArray() {
		return resp, statusErr{code: http.StatusBadGateway, msg: "Image API response is missing data"}
	}
	reporter.ObserveResponseModel(body)
	reporter.Publish(ctx, helps.ParseOpenAIUsage(body))
	reporter.EnsurePublished(ctx)
	return cliproxyexecutor.Response{Payload: body, Headers: header.Clone()}, nil
}

// runnerRow resolves the row of the runner transport api (openai-images,
// openai-speech) a request names: the row the account's discovery
// registered, else NeoPi's bundled row, on the account's configured endpoint.
func (e *NeoPiCompletionsExecutor) runnerRow(auth *cliproxyauth.Auth, req cliproxyexecutor.Request, api string) *registry.NeoPiModel {
	if info, ok := cliproxyauth.ResolvedModelInfo(req); ok && info != nil && info.NeoPi != nil && info.NeoPi.API == api {
		return e.accountRow(auth, info.NeoPi)
	}
	resolve := e.profile.Row
	if resolve == nil {
		resolve = registry.NeoPiCompletionsModel
	}
	if row := resolve(e.profile.Provider, req.Model); row != nil && row.API == api {
		return e.accountRow(auth, row)
	}
	return nil
}

// postRunner sends one JSON runner request (image or speech) with NeoPi's
// headers and returns the reply.
func (e *NeoPiCompletionsExecutor) postRunner(ctx context.Context, auth *cliproxyauth.Auth, client *http.Client, row *registry.NeoPiModel, path string, payload []byte) (int, http.Header, []byte, error) {
	url := strings.TrimRight(row.BaseURL, "/") + path
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+e.apiKey(auth))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", openaiwire.NeoPiUserAgent)
	if auth != nil {
		util.ApplyCustomHeadersFromAttrs(httpReq, auth.Attributes)
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{URL: url, Method: http.MethodPost, Headers: httpReq.Header.Clone(), Body: payload, Provider: e.provider})
	response, err := client.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return 0, nil, nil, err
	}
	defer func() { _ = response.Body.Close() }()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, response.StatusCode, response.Header.Clone())
	body, err := io.ReadAll(response.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return 0, nil, nil, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, body)
	return response.StatusCode, response.Header, body, nil
}

// neoPiImagePayload builds generateOpenAIImage's body: gpt-image models get
// no response_format (they answer base64 only), every other model is asked
// for b64_json, and an edit carries its references as data URLs.
func neoPiImagePayload(row *registry.NeoPiModel, request neoPiImageRequest, edit bool) ([]byte, error) {
	model := row.ID
	if row.RequestModelID != "" {
		model = row.RequestModelID
	}
	body := neoPiImageBody{Model: model, Prompt: request.prompt, N: request.count, Size: request.size}
	if neopitaxonomy.Classify(row.Provider, row.ID).Family != "gpt-image" {
		body.ResponseFormat = "b64_json"
	}
	if edit {
		for _, image := range request.images {
			body.InputReferences = append(body.InputReferences, neoPiImageReference{Type: "image_url", URL: image})
		}
	}
	return jsjson.Marshal(body), nil
}

// parseNeoPiImageRequest reads an OpenAI images request, JSON or multipart.
// The count defaults to one image, as NeoPi's does.
func parseNeoPiImageRequest(payload []byte, contentType string) (neoPiImageRequest, error) {
	request := neoPiImageRequest{count: 1}
	if json.Valid(payload) {
		root := gjson.ParseBytes(payload)
		request.prompt = root.Get("prompt").String()
		if n := root.Get("n"); n.Type == gjson.Number {
			request.count = n.Int()
		}
		request.size = strings.TrimSpace(root.Get("size").String())
		request.images = neoPiImageURLs(root)
		return request, nil
	}
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") || params["boundary"] == "" {
		return request, fmt.Errorf("image request must be JSON or multipart form data")
	}
	form, err := multipart.NewReader(bytes.NewReader(payload), params["boundary"]).ReadForm(openAICompatMultipartMemory)
	if err != nil {
		return request, fmt.Errorf("read image form: %w", err)
	}
	defer func() { _ = form.RemoveAll() }()
	if values := form.Value["prompt"]; len(values) > 0 {
		request.prompt = values[0]
	}
	if values := form.Value["n"]; len(values) > 0 {
		var n int64
		if _, errScan := fmt.Sscan(strings.TrimSpace(values[0]), &n); errScan == nil {
			request.count = n
		}
	}
	if values := form.Value["size"]; len(values) > 0 {
		request.size = strings.TrimSpace(values[0])
	}
	for _, key := range []string{"image", "image[]"} {
		for _, header := range form.File[key] {
			file, errOpen := header.Open()
			if errOpen != nil {
				return request, fmt.Errorf("open image upload: %w", errOpen)
			}
			data, errRead := io.ReadAll(file)
			_ = file.Close()
			if errRead != nil {
				return request, fmt.Errorf("read image upload: %w", errRead)
			}
			mimeType := header.Header.Get("Content-Type")
			if mimeType == "" || mimeType == "application/octet-stream" {
				mimeType = http.DetectContentType(data)
			}
			request.images = append(request.images, "data:"+mimeType+";base64,"+base64.StdEncoding.EncodeToString(data))
		}
	}
	return request, nil
}

// neoPiImageURLs collects a JSON edit's reference images: `image` and
// `images`, each a URL string or an {image_url} / {url} object.
func neoPiImageURLs(root gjson.Result) []string {
	var urls []string
	add := func(value gjson.Result) {
		url := value.String()
		if value.IsObject() {
			url = value.Get("image_url.url").String()
			if imageURL := value.Get("image_url"); imageURL.Type == gjson.String {
				url = imageURL.String()
			}
			if url == "" {
				url = value.Get("url").String()
			}
		}
		if url = strings.TrimSpace(url); url != "" {
			urls = append(urls, url)
		}
	}
	if image := root.Get("image"); image.Exists() {
		add(image)
	}
	for _, image := range root.Get("images").Array() {
		add(image)
	}
	return urls
}

// neoPiImageErrorMessage is errorMessage: the reply's detail or
// error.message, else its text.
func neoPiImageErrorMessage(body []byte) string {
	if json.Valid(body) {
		root := gjson.ParseBytes(body)
		if detail := root.Get("detail"); detail.Type == gjson.String {
			return detail.String()
		}
		if message := root.Get("error.message"); message.Type == gjson.String {
			return message.String()
		}
	}
	return string(body)
}
