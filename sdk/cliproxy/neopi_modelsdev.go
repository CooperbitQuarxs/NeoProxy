package cliproxy

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sync"

	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/openaiwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// neoPiModelsDevURL is where the models.dev catalog is fetched; tests point it
// at a loopback server.
var neoPiModelsDevURL = registry.NeoPiModelsDevURL

// neoPiZstdMagic is the zstd frame magic NeoPi sniffs (little-endian).
const neoPiZstdMagic = 0xFD2FB528

// neoPiCatalogSession is NeoPi's catalog session for one network route: the
// last payload and its ETag, revalidated with a conditional GET, and the
// request in flight that concurrent callers share.
type neoPiCatalogSession struct {
	mu       sync.Mutex
	payload  []byte
	etag     string
	inflight *neoPiCatalogFetch
}

// neoPiCatalogFetch is one shared catalog request; done closes when it ends.
type neoPiCatalogFetch struct {
	done    chan struct{}
	payload []byte
	err     error
}

// neoPiModelsDevSessions holds a session per network route (see
// neoPiCatalogRoute), as NeoPi scopes sessions to the fetch implementation
// that owns the network.
var neoPiModelsDevSessions sync.Map

// neoPiCatalogRoute identifies the network a catalog request takes, as
// NewProxyAwareHTTPClient selects it: the configured proxy, else the
// context's RoundTripper, else the direct route.
func neoPiCatalogRoute(ctx context.Context, cfg *config.Config, auth *coreauth.Auth) any {
	if proxy := helps.EffectiveProxyURL(ctx, cfg, auth); proxy != "" {
		return "proxy " + proxy
	}
	rt := helps.ContextRoundTripper(ctx)
	if rt == nil {
		return "direct"
	}
	value := reflect.ValueOf(rt)
	switch value.Kind() {
	case reflect.Pointer, reflect.Func, reflect.Map, reflect.Chan, reflect.UnsafePointer:
		return fmt.Sprintf("transport %T %x", rt, value.Pointer())
	}
	if value.Comparable() {
		return rt
	}
	return fmt.Sprintf("transport %T", rt)
}

// neoPiFetchModelsDev is fetchWellKnownModels: the catalog, decompressed when
// the body is a zstd frame. Callers on one route share a single request and
// each stops waiting when its own context ends. A failed or unusable fetch
// falls back to the last payload (nil when there is none), as NeoPi's stale
// fallback does. NeoPi bounds this optional fetch with a deadline; NeoProxy
// sets none after the connection is established.
func neoPiFetchModelsDev(ctx context.Context, cfg *config.Config, auth *coreauth.Auth) []byte {
	value, _ := neoPiModelsDevSessions.LoadOrStore(neoPiCatalogRoute(ctx, cfg, auth), &neoPiCatalogSession{})
	session := value.(*neoPiCatalogSession)
	session.mu.Lock()
	fetch := session.inflight
	if fetch == nil {
		fetch = &neoPiCatalogFetch{done: make(chan struct{})}
		session.inflight = fetch
		cached, etag := session.payload, session.etag
		// The shared request outlives the caller that started it.
		go func(ctx context.Context) {
			payload, nextETag, err := neoPiFetchModelsDevPayload(ctx, cfg, auth, cached, etag)
			session.mu.Lock()
			if err == nil {
				session.payload, session.etag = payload, nextETag
			}
			session.inflight = nil
			fetch.payload, fetch.err = payload, err
			session.mu.Unlock()
			close(fetch.done)
		}(context.WithoutCancel(ctx))
	}
	session.mu.Unlock()
	select {
	case <-fetch.done:
		if fetch.err == nil {
			return fetch.payload
		}
		log.WithError(fetch.err).Debug("models.dev catalog unavailable")
	case <-ctx.Done():
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.payload
}

// neoPiFetchModelsDevPayload is fetchCatalogPayload: a conditional GET that
// answers the cached payload on 304. It returns the payload and its ETag.
func neoPiFetchModelsDevPayload(ctx context.Context, cfg *config.Config, auth *coreauth.Auth, cached []byte, etag string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, neoPiModelsDevURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "application/zstd, application/json")
	req.Header.Set("User-Agent", openaiwire.NeoPiUserAgent)
	if cached != nil && etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := helps.NewProxyAwareHTTPClient(ctx, cfg, auth, 0).Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Debug("models.dev catalog: close body")
		}
	}()
	if resp.StatusCode == http.StatusNotModified && cached != nil {
		return cached, etag, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, "", fmt.Errorf("models catalog fetch failed: %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, neoPiDiscoveryBodyLimit))
	if err != nil {
		return nil, "", err
	}
	if len(body) >= 4 && binary.LittleEndian.Uint32(body) == neoPiZstdMagic {
		decoder, errDecoder := zstd.NewReader(bytes.NewReader(body), zstd.WithDecoderMaxMemory(neoPiDiscoveryBodyLimit))
		if errDecoder != nil {
			return nil, "", errDecoder
		}
		body, err = io.ReadAll(io.LimitReader(decoder, neoPiDiscoveryBodyLimit))
		decoder.Close()
		if err != nil {
			return nil, "", err
		}
	}
	// JSON.parse: an unparseable catalog is a failed fetch.
	if !gjson.ValidBytes(body) {
		return nil, "", fmt.Errorf("models catalog is not JSON")
	}
	return body, resp.Header.Get("ETag"), nil
}

// fetchNeoPiSiliconFlowModels is SiliconFlow's fetchDynamicModels: the
// models.dev catalog first (optional enrichment), then the account's /models
// listing, joined by NeoPiSiliconFlowListing.
func fetchNeoPiSiliconFlowModels(ctx context.Context, cfg *config.Config, auth *coreauth.Auth, provider, key, base string) ([]*ModelInfo, error) {
	modelsDev := neoPiFetchModelsDev(ctx, cfg, auth)
	endpoint := base + "/models"
	body, err := neoPiDiscoveryGet(ctx, cfg, auth, provider, endpoint, key)
	if err != nil {
		return nil, err
	}
	models, err := registry.NeoPiSiliconFlowListing(provider, base, body, modelsDev)
	if err != nil {
		return nil, fmt.Errorf("%s models: %s: %w", provider, endpoint, err)
	}
	return models, nil
}
