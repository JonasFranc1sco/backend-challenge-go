package auth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"
)

type jwksResponse struct {
	Keys []jwkKey `json:"keys"`
}

type jwkKey struct {
	KID string `json:"kid"`
	KTY string `json:"kty"`
	Alg string `json:"alg"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// JWKSKeyFetcher manages fetching, parsing, and caching of RSA public keys from Keycloak's JWKS endpoint.
type JWKSKeyFetcher struct {
	jwksURL    string
	httpClient *http.Client
	cacheTTL   time.Duration

	mu       sync.RWMutex
	keys     map[string]*rsa.PublicKey
	cachedAt time.Time
}

func NewJWKSKeyFetcher(jwksURL string, cacheTTL time.Duration) *JWKSKeyFetcher {
	if cacheTTL <= 0 {
		cacheTTL = 1 * time.Hour
	}
	return &JWKSKeyFetcher{
		jwksURL: jwksURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		cacheTTL: cacheTTL,
		keys:     make(map[string]*rsa.PublicKey),
	}
}

// GetKey retrieves the RSA public key for a given Key ID (KID).
// If the key is not in cache or cache is expired, it refreshes from the Keycloak JWKS endpoint.
func (f *JWKSKeyFetcher) GetKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	f.mu.RLock()
	key, exists := f.keys[kid]
	fresh := time.Since(f.cachedAt) < f.cacheTTL
	f.mu.RUnlock()

	if exists && fresh {
		return key, nil
	}

	// Refresh cache under write lock
	f.mu.Lock()
	defer f.mu.Unlock()

	// Double-check after acquiring write lock
	if key, exists := f.keys[kid]; exists && time.Since(f.cachedAt) < f.cacheTTL {
		return key, nil
	}

	if err := f.refresh(ctx); err != nil {
		// If refresh failed but we still have an expired key, return it as fallback
		if fallbackKey, ok := f.keys[kid]; ok {
			return fallbackKey, nil
		}
		return nil, fmt.Errorf("failed to refresh JWKS keys: %w", err)
	}

	key, exists = f.keys[kid]
	if !exists {
		return nil, fmt.Errorf("key with kid '%s' not found in JWKS", kid)
	}

	return key, nil
}

func (f *JWKSKeyFetcher) refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.jwksURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create JWKS request: %w", err)
	}

	resp, err := f.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to fetch JWKS: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("JWKS request returned status %d: %s", resp.StatusCode, string(body))
	}

	var jwks jwksResponse
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		return fmt.Errorf("failed to decode JWKS response: %w", err)
	}

	newKeys := make(map[string]*rsa.PublicKey)
	for _, k := range jwks.Keys {
		if k.KTY != "RSA" {
			continue
		}
		pubKey, err := parseRSAPublicKey(k.N, k.E)
		if err != nil {
			continue
		}
		newKeys[k.KID] = pubKey
	}

	f.keys = newKeys
	f.cachedAt = time.Now()
	return nil
}

func parseRSAPublicKey(nStr, eStr string) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(nStr)
	if err != nil {
		return nil, fmt.Errorf("failed to decode modulus: %w", err)
	}

	eBytes, err := base64.RawURLEncoding.DecodeString(eStr)
	if err != nil {
		return nil, fmt.Errorf("failed to decode exponent: %w", err)
	}

	n := new(big.Int).SetBytes(nBytes)
	e := new(big.Int).SetBytes(eBytes)

	return &rsa.PublicKey{
		N: n,
		E: int(e.Int64()),
	}, nil
}
