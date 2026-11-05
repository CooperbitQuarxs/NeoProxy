// Package kilo is NeoPi's Kilo Gateway sign-in (auth rule kilo, custom hook
// "kilo", registry/oauth/kilo.ts loginKilo): a device authorization whose
// approval yields a bearer token the gateway accepts for a year. The token is
// the account's API key; there is nothing to refresh.
package kilo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

const (
	// Provider is NeoPi's provider id.
	Provider = "kilo"
	// BaseURL is the gateway kiloModelManagerOptions and inference default to.
	BaseURL = "https://api.kilo.ai/api/gateway"
	// DeviceAuthBaseURL is KILO_DEVICE_AUTH_BASE_URL.
	DeviceAuthBaseURL = "https://api.kilo.ai/api/device-auth"
	// PollInterval is POLL_INTERVAL_MS.
	PollInterval = 5 * time.Second
	// TokenLifetime is how long NeoPi records the token as valid.
	TokenLifetime = 365 * 24 * time.Hour
)

// DeviceCode is a started device authorization.
type DeviceCode struct {
	// Code is the code the person enters at VerificationURL.
	Code            string
	VerificationURL string
	// ExpiresIn is how long the code can be approved, in seconds.
	ExpiresIn int
}

// Error is loginKilo's OAuthError: Kind names its category (device-auth,
// polling, validation, timeout) and Status the HTTP status that caused it,
// when one did.
type Error struct {
	Kind    string
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

// StartDeviceFlow posts a new device authorization, as loginKilo does before
// it shows the code.
func StartDeviceFlow(ctx context.Context, client *http.Client, base string) (*DeviceCode, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/codes", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusTooManyRequests {
			return nil, &Error{Kind: "polling", Status: resp.StatusCode, Message: "Too many pending authorization requests. Please try again later."}
		}
		return nil, &Error{Kind: "device-auth", Status: resp.StatusCode, Message: fmt.Sprintf("Failed to initiate device authorization: %d", resp.StatusCode)}
	}
	var body struct {
		Code            *string  `json:"code"`
		VerificationURL *string  `json:"verificationUrl"`
		ExpiresIn       *float64 `json:"expiresIn"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	if body.Code == nil || *body.Code == "" || body.VerificationURL == nil || *body.VerificationURL == "" || body.ExpiresIn == nil || *body.ExpiresIn <= 0 {
		return nil, &Error{Kind: "validation", Message: "Kilo device authorization response missing required fields"}
	}
	return &DeviceCode{Code: *body.Code, VerificationURL: *body.VerificationURL, ExpiresIn: int(*body.ExpiresIn)}, nil
}

// PollForToken polls the authorization every PollInterval until it is
// approved, denied or expires, as loginKilo does, and returns the token. now
// and wait are the clock and pause the poll uses.
func PollForToken(ctx context.Context, client *http.Client, base string, code *DeviceCode, now func() time.Time, wait func(context.Context, time.Duration) error) (string, error) {
	deadline := now().Add(time.Duration(code.ExpiresIn) * time.Second)
	for now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		token, done, err := pollOnce(ctx, client, base, code.Code)
		if err != nil || done {
			return token, err
		}
		if err = wait(ctx, PollInterval); err != nil {
			return "", err
		}
	}
	return "", &Error{Kind: "timeout", Message: "Authentication timed out. Please try again."}
}

// pollOnce reads the authorization's state: done with a token when approved,
// not done while it is pending.
func pollOnce(ctx context.Context, client *http.Client, base, code string) (string, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/codes/"+url.PathEscape(code), nil)
	if err != nil {
		return "", false, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusAccepted:
		return "", false, nil
	case resp.StatusCode == http.StatusForbidden:
		return "", true, &Error{Kind: "device-auth", Message: "Authorization was denied"}
	case resp.StatusCode == http.StatusGone:
		return "", true, &Error{Kind: "device-auth", Message: "Authorization code expired. Please try again."}
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return "", true, &Error{Kind: "polling", Status: resp.StatusCode, Message: fmt.Sprintf("Failed to poll device authorization: %d", resp.StatusCode)}
	}
	var body struct {
		Status string `json:"status"`
		Token  string `json:"token"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", true, err
	}
	switch {
	case body.Status == "approved" && body.Token != "":
		return body.Token, true, nil
	case body.Status == "denied":
		return "", true, &Error{Kind: "device-auth", Message: "Authorization was denied"}
	case body.Status == "expired":
		return "", true, &Error{Kind: "device-auth", Message: "Authorization code expired. Please try again."}
	}
	return "", false, nil
}

// Wait pauses for d unless ctx ends first.
func Wait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
