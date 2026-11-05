package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func TestManagerMarkResult_NeoPiResponses404DoesNotCool(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	providers := []struct {
		name     string
		provider string
		baseURL  string
		legacy   bool
	}{
		{name: "meta account", provider: "meta"},
		{name: "normalized meta", provider: " META "},
		{name: "custom Sakana entry", provider: "my-fugu-key", baseURL: registry.ResponsesProviderBaseURL(registry.ResponsesSakana)},
		{name: "custom Abliteration entry", provider: "my-abliteration-key", baseURL: registry.ResponsesProviderBaseURL(registry.ResponsesAbliterate)},
		{name: "custom Model API entry", provider: "my-model-api-key", baseURL: registry.ResponsesProviderBaseURL(registry.ResponsesMeta) + "/"},
		{name: "legacy codex", provider: "codex", legacy: true},
		{name: "unrelated gateway named sakana", provider: "sakana", baseURL: "https://example.com/v1", legacy: true},
	}
	errors := []struct {
		name string
		err  *Error
	}{
		{name: "generic", err: &Error{HTTPStatus: http.StatusNotFound, Message: `{"error":{"message":"Not Found"}}`}},
		{name: "model not found", err: &Error{HTTPStatus: http.StatusNotFound, Code: "model_not_found", Message: `{"error":{"code":"model_not_found","message":"The model does not exist"}}`}},
	}
	for _, provider := range providers {
		for _, failure := range errors {
			for _, model := range []string{"", "test-model"} {
				t.Run(provider.name+"/"+failure.name+"/"+model, func(t *testing.T) {
					m := NewManager(nil, nil, nil)
					auth := &Auth{ID: "responses-auth", Provider: provider.provider, Attributes: map[string]string{"base_url": provider.baseURL}}
					if _, err := m.Register(context.Background(), auth); err != nil {
						t.Fatalf("register auth: %v", err)
					}
					// A result's provider label is not authoritative: custom
					// compatibility entries are identified from the stored auth.
					m.MarkResult(context.Background(), Result{AuthID: auth.ID, Provider: "unrelated-result-label", Model: model, Error: failure.err})
					updated, ok := m.GetByID(auth.ID)
					if !ok || updated == nil {
						t.Fatal("registered auth missing")
					}
					if updated.Failed != 1 {
						t.Fatalf("failed count = %d, want 1", updated.Failed)
					}
					next := updated.NextRetryAfter
					if model != "" {
						if state := updated.ModelStates[model]; state != nil {
							next = state.NextRetryAfter
							if !provider.legacy && state.Unavailable {
								t.Fatal("404 made model unavailable")
							}
						}
					}
					if provider.legacy {
						if remaining := time.Until(next); remaining < 11*time.Hour || remaining > 13*time.Hour {
							t.Fatalf("legacy cooldown = %v, want approximately 12h", remaining)
						}
					} else if !next.IsZero() || updated.Unavailable || updated.Quota.Exceeded {
						t.Fatalf("Responses 404 changed availability: retry=%v unavailable=%v quota=%+v", next, updated.Unavailable, updated.Quota)
					}
				})
			}
		}
	}
}

func TestManagerMarkResult_NeoPiResponses404PreservesExistingCooldown(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	for _, model := range []string{"", "test-model"} {
		t.Run(model, func(t *testing.T) {
			m := NewManager(nil, nil, nil)
			auth := &Auth{ID: "meta-auth", Provider: "meta"}
			if _, err := m.Register(context.Background(), auth); err != nil {
				t.Fatalf("register auth: %v", err)
			}
			retryAfter := time.Minute
			m.MarkResult(context.Background(), Result{AuthID: auth.ID, Provider: auth.Provider, Model: model, Error: &Error{HTTPStatus: http.StatusTooManyRequests}, RetryAfter: &retryAfter})
			before, _ := m.GetByID(auth.ID)
			if before == nil {
				t.Fatal("registered auth missing")
			}
			next := before.NextRetryAfter
			if model != "" {
				next = before.ModelStates[model].NextRetryAfter
			}
			if !next.After(time.Now()) {
				t.Fatal("expected 429 cooldown before 404")
			}
			m.MarkResult(context.Background(), Result{AuthID: auth.ID, Provider: auth.Provider, Model: model, Error: &Error{HTTPStatus: http.StatusNotFound}, RetryAfter: &retryAfter})
			after, _ := m.GetByID(auth.ID)
			if after == nil {
				t.Fatal("registered auth missing after 404")
			}
			got := after.NextRetryAfter
			if model != "" {
				got = after.ModelStates[model].NextRetryAfter
			}
			if !got.Equal(next) {
				t.Fatalf("404 changed existing cooldown from %v to %v", next, got)
			}
		})
	}
}

type responsesNotFoundTestError struct{ error }

func (e responsesNotFoundTestError) Unwrap() error                { return e.error }
func (e responsesNotFoundTestError) NeoPiResponsesNotFound() bool { return true }

func TestManagerMarkResult_XAIResponses404TransportMarker(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	for _, marked := range []bool{false, true} {
		for _, model := range []string{"", "grok-test"} {
			name := "legacy/"
			if marked {
				name = "responses/"
			}
			t.Run(name+model, func(t *testing.T) {
				m := NewManager(nil, nil, nil)
				auth := &Auth{ID: "xai-auth", Provider: "xai"}
				if _, err := m.Register(context.Background(), auth); err != nil {
					t.Fatalf("register auth: %v", err)
				}
				var upstream error = &statusBearingError{
					status: http.StatusNotFound,
					msg:    `{"error":{"code":"model_not_found","message":"The model does not exist"}}`,
				}
				if marked {
					upstream = responsesNotFoundTestError{error: upstream}
				}
				resultErr := resultErrorFromError(upstream)
				if marked && resultErr.Code != ErrorCodeNeoPiResponsesNotFound {
					t.Fatalf("transport marker lost during conversion: %+v", resultErr)
				}
				m.MarkResult(context.Background(), Result{AuthID: auth.ID, Provider: auth.Provider, Model: model, Error: resultErr})
				updated, ok := m.GetByID(auth.ID)
				if !ok || updated == nil {
					t.Fatal("registered auth missing")
				}
				next := updated.NextRetryAfter
				if model != "" && updated.ModelStates[model] != nil {
					next = updated.ModelStates[model].NextRetryAfter
				}
				if marked {
					if !next.IsZero() || updated.Unavailable {
						t.Fatalf("Responses 404 cooled xAI auth: retry=%v unavailable=%v", next, updated.Unavailable)
					}
				} else if remaining := time.Until(next); remaining < 11*time.Hour || remaining > 13*time.Hour {
					t.Fatalf("legacy xAI cooldown = %v, want approximately 12h", remaining)
				}
			})
		}
	}
}
