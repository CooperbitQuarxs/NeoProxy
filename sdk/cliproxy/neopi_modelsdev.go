package cliproxy

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
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

// neoPiModelsDevSession is NeoPi's catalog session: the last payload and its
// ETag, revalidated with a conditional GET.
var neoPiModelsDevSession struct {
	sync.Mutex
	payload []byte
	etag    string
}

// neoPiFetchModelsDev is fetchWellKnownModels: the catalog, decompressed when
// the body is a zstd frame. A failed or unusable fetch falls back to the
// last payload (nil when there is none), as NeoPi's stale fallback does.
// NeoPi bounds this optional fetch with a deadline; NeoProxy sets none after
// the connection is established.
func neoPiFetchModelsDev(ctx context.Context, cfg *config.Config, auth *coreauth.Auth) []byte {
	neoPiModelsDevSession.Lock()
	defer neoPiModelsDevSession.Unlock()
	payload, err := neoPiFetchModelsDevPayload(ctx, cfg, auth)
	if err != nil {
		log.WithError(err).Debug("models.dev catalog unavailable")
		return neoPiModelsDevSession.payload
	}
	return payload
}

func neoPiFetchModelsDevPayload(ctx context.Context, cfg *config.Config, auth *coreauth.Auth) ([]byte, error) {
	session := &neoPiModelsDevSession
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, neoPiModelsDevURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/zstd, application/json")
	req.Header.Set("User-Agent", openaiwire.NeoPiUserAgent)
	if session.payload != nil && session.etag != "" {
		req.Header.Set("If-None-Match", session.etag)
	}
	resp, err := helps.NewProxyAwareHTTPClient(ctx, cfg, auth, 0).Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Debug("models.dev catalog: close body")
		}
	}()
	if resp.StatusCode == http.StatusNotModified && session.payload != nil {
		return session.payload, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("models catalog fetch failed: %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, neoPiDiscoveryBodyLimit))
	if err != nil {
		return nil, err
	}
	if len(body) >= 4 && binary.LittleEndian.Uint32(body) == neoPiZstdMagic {
		decoder, errDecoder := zstd.NewReader(bytes.NewReader(body), zstd.WithDecoderMaxMemory(neoPiDiscoveryBodyLimit))
		if errDecoder != nil {
			return nil, errDecoder
		}
		body, err = io.ReadAll(io.LimitReader(decoder, neoPiDiscoveryBodyLimit))
		decoder.Close()
		if err != nil {
			return nil, err
		}
	}
	// JSON.parse: an unparseable catalog is a failed fetch.
	if !gjson.ValidBytes(body) {
		return nil, fmt.Errorf("models catalog is not JSON")
	}
	session.payload, session.etag = body, resp.Header.Get("ETag")
	return body, nil
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
