package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neoroute"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/tidwall/sjson"
)

type neoSelectionContextKey struct{}

func neoSelectionFromContext(ctx context.Context) (neoroute.Selection, bool) {
	selection, ok := ctx.Value(neoSelectionContextKey{}).(neoroute.Selection)
	return selection, ok
}
func neoAsked(ctx context.Context, fallback string) string {
	if selection, ok := neoSelectionFromContext(ctx); ok {
		return selection.Asked
	}
	return fallback
}
func (h *BaseAPIHandler) shouldNeoRoute(ctx context.Context) bool {
	_, selected := neoSelectionFromContext(ctx)
	_, principal := neo.PrincipalFromContext(ctx)
	return principal && !selected
}
func (h *BaseAPIHandler) SetNeoResolver(resolver neoroute.Resolver) {
	h.runtimeMu.Lock()
	defer h.runtimeMu.Unlock()
	h.NeoResolver = resolver
}
func (h *BaseAPIHandler) neoResolver() neoroute.Resolver {
	h.runtimeMu.RLock()
	defer h.runtimeMu.RUnlock()
	return h.NeoResolver
}

// enforceNeoPin runs after pre-auth interceptors and before the engine's shared
// retry machinery. Interceptors cannot widen the resolver's account authority.
func enforceNeoPin(ctx context.Context, opts *coreexecutor.Options) {
	if selection, ok := neoSelectionFromContext(ctx); ok {
		if opts.Metadata == nil {
			opts.Metadata = make(map[string]any)
		}
		opts.Metadata[coreexecutor.PinnedAuthMetadataKey] = selection.AuthID
		opts.Metadata[coreexecutor.RequestedModelMetadataKey] = selection.Asked
	}
}

type neoEngineError struct{ message *interfaces.ErrorMessage }

func (e *neoEngineError) Error() string {
	if e.message != nil && e.message.Error != nil {
		return e.message.Error.Error()
	}
	return "Upstream execution failed"
}
func (e *neoEngineError) StatusCode() int {
	if e.message == nil {
		return 502
	}
	return e.message.StatusCode
}
func (e *neoEngineError) Unwrap() error {
	if e.message == nil {
		return nil
	}
	return e.message.Error
}
func neoErrorMessage(err error) *interfaces.ErrorMessage {
	var engine *neoEngineError
	if errors.As(err, &engine) {
		return engine.message
	}
	var policy *neoroute.Error
	if errors.As(err, &policy) {
		body, _ := json.Marshal(map[string]any{"error": map[string]any{"message": policy.Error(), "code": policy.Code, "virtualModel": policy.Decision.VirtualModel, "resetsAt": policy.Decision.ResetsAt}})
		return &interfaces.ErrorMessage{StatusCode: policy.Status, Error: errors.New(string(body))}
	}
	return executionErrorMessage(err)
}
func neoProtocolAllowed(protocol string) bool {
	return protocol == "openai" || protocol == "openai-response" || protocol == "claude"
}
func neoPrepared(ctx context.Context, selection neoroute.Selection, raw []byte) (context.Context, string, []byte, error) {
	model := selection.Model
	if selection.Effort != nil {
		model += "(" + *selection.Effort + ")"
	}
	body, err := sjson.SetBytes(raw, "model", model)
	if err != nil {
		return ctx, "", nil, err
	}
	ctx = context.WithValue(ctx, neoSelectionContextKey{}, selection)
	ctx = WithPinnedAuthID(ctx, selection.AuthID)
	ctx = coreusage.WithRequestedModelAlias(ctx, selection.Asked)
	return ctx, model, body, nil
}
func (h *BaseAPIHandler) executeNeo(ctx context.Context, entryProtocol, exitProtocol, asked string, raw []byte, alt string, execOptions modelExecutionOptions) ([]byte, http.Header, *interfaces.ErrorMessage) {
	resolver := h.neoResolver()
	if resolver == nil {
		return nil, nil, neoErrorMessage(fmt.Errorf("NeoProxy routing is unavailable"))
	}
	if !neoProtocolAllowed(entryProtocol) {
		return nil, nil, &interfaces.ErrorMessage{StatusCode: 400, Error: fmt.Errorf("inference keys support Chat Completions, Responses and Messages")}
	}
	req := neoroute.Request{Asked: asked, Protocol: entryProtocol, Body: raw, Client: neoroute.ClientName(headersFromContext(ctx))}
	execution, err := resolver.Run(ctx, req, func(attemptCtx context.Context, selection neoroute.Selection) (*neoroute.Execution, error) {
		attemptCtx, model, body, errPrepare := neoPrepared(attemptCtx, selection, raw)
		if errPrepare != nil {
			return nil, errPrepare
		}
		options := execOptions
		options.ForcedProvider = selection.Provider
		options.AuthSelectionModel = ""
		response, headers, errMessage := h.executeWithAuthManagerFormats(attemptCtx, entryProtocol, exitProtocol, model, body, alt, false, options)
		if errMessage != nil {
			return nil, &neoEngineError{message: errMessage}
		}
		return &neoroute.Execution{Body: response, Headers: headers}, nil
	})
	if err != nil {
		return nil, nil, neoErrorMessage(err)
	}
	return execution.Body, execution.Headers, nil
}
func (h *BaseAPIHandler) executeNeoStream(ctx context.Context, entryProtocol, exitProtocol, asked string, raw []byte, alt string, execOptions modelExecutionOptions) (<-chan []byte, http.Header, <-chan *interfaces.ErrorMessage) {
	fail := func(err error) (<-chan []byte, http.Header, <-chan *interfaces.ErrorMessage) {
		errs := make(chan *interfaces.ErrorMessage, 1)
		errs <- neoErrorMessage(err)
		close(errs)
		return nil, nil, errs
	}
	resolver := h.neoResolver()
	if resolver == nil {
		return fail(fmt.Errorf("NeoProxy routing is unavailable"))
	}
	if !neoProtocolAllowed(entryProtocol) {
		return fail(&neoroute.Error{Status: 400, Code: "neoproxy_protocol", Decision: neoroute.Decision{Outcome: "error", Reason: "unsupported-protocol"}})
	}
	req := neoroute.Request{Asked: asked, Protocol: entryProtocol, Body: raw, Stream: true, Client: neoroute.ClientName(headersFromContext(ctx))}
	execution, err := resolver.Run(ctx, req, func(attemptCtx context.Context, selection neoroute.Selection) (*neoroute.Execution, error) {
		attemptCtx, model, body, errPrepare := neoPrepared(attemptCtx, selection, raw)
		if errPrepare != nil {
			return nil, errPrepare
		}
		options := execOptions
		options.ForcedProvider = selection.Provider
		options.AuthSelectionModel = ""
		data, headers, errs := h.executeStreamWithAuthManagerFormats(attemptCtx, entryProtocol, exitProtocol, model, body, alt, false, options)
		chunks := make(chan neoroute.Chunk)
		go func() {
			defer close(chunks)
			for data != nil || errs != nil {
				select {
				case <-attemptCtx.Done():
					return
				case payload, open := <-data:
					if !open {
						data = nil
						continue
					}
					select {
					case chunks <- neoroute.Chunk{Data: payload}:
					case <-attemptCtx.Done():
						return
					}
				case message, open := <-errs:
					if !open {
						errs = nil
						continue
					}
					if message != nil {
						select {
						case chunks <- neoroute.Chunk{Err: &neoEngineError{message: message}}:
						case <-attemptCtx.Done():
						}
						return
					}
				}
			}
		}()
		return &neoroute.Execution{Headers: headers, Chunks: chunks}, nil
	})
	if err != nil {
		return fail(err)
	}
	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage, 1)
	go func() {
		defer close(data)
		defer close(errs)
		for {
			select {
			case <-ctx.Done():
				return
			case chunk, open := <-execution.Chunks:
				if !open {
					return
				}
				if chunk.Err != nil {
					select {
					case errs <- neoErrorMessage(chunk.Err):
					case <-ctx.Done():
					}
					return
				}
				select {
				case data <- chunk.Data:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return data, execution.Headers, errs
}
