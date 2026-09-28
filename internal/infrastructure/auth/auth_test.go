package auth_test

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/JonasFranc1sco/backend-challenge-go/internal/infrastructure/auth"
	"github.com/golang-jwt/jwt/v5"
)

func TestAuth_ValidationAndMiddleware(t *testing.T) {
	// 1. Generate test RSA key pair
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate RSA key: %v", err)
	}
	publicKey := &privateKey.PublicKey

	// 2. Setup mock JWKS HTTP server
	jwksHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nStr := base64.RawURLEncoding.EncodeToString(publicKey.N.Bytes())
		eStr := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(publicKey.E)).Bytes())

		resp := map[string]interface{}{
			"keys": []map[string]interface{}{
				{
					"kid": "test-key-1",
					"kty": "RSA",
					"alg": "RS256",
					"use": "sig",
					"n":   nStr,
					"e":   eStr,
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	jwksServer := httptest.NewServer(jwksHandler)
	defer jwksServer.Close()

	// 3. Initialize JWKSKeyFetcher and Validator
	keyFetcher := auth.NewJWKSKeyFetcher(jwksServer.URL, 1*time.Minute)
	validator := auth.NewKeycloakJWTValidator(keyFetcher, "test-issuer")

	// 4. Helper to mint signed tokens
	mintToken := func(claims jwt.MapClaims) string {
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		token.Header["kid"] = "test-key-1"
		tokenStr, err := token.SignedString(privateKey)
		if err != nil {
			t.Fatalf("failed to sign token: %v", err)
		}
		return tokenStr
	}

	// Test 4.1: Valid Provider Token
	providerClaims := jwt.MapClaims{
		"sub":         "user-provider-a",
		"iss":         "test-issuer",
		"exp":         time.Now().Add(10 * time.Minute).Unix(),
		"client_id":   "provider-a",
		"provider_id": "provider-a",
		"roles":       []string{"provider"},
	}
	providerToken := mintToken(providerClaims)

	identity, err := validator.ValidateToken(t.Context(), providerToken)
	if err != nil {
		t.Fatalf("expected valid token, got: %v", err)
	}
	if identity.ProviderID != "provider-a" {
		t.Errorf("expected provider-a, got %s", identity.ProviderID)
	}
	if !identity.HasRole("provider") {
		t.Errorf("expected provider role")
	}
	if identity.IsInternal() {
		t.Errorf("provider should not have internal role")
	}

	// Test 4.2: Valid Internal Token
	internalClaims := jwt.MapClaims{
		"sub":       "user-internal",
		"iss":       "test-issuer",
		"exp":       time.Now().Add(10 * time.Minute).Unix(),
		"client_id": "internal-service",
		"roles":     []string{"internal"},
	}
	internalToken := mintToken(internalClaims)

	internalIdentity, err := validator.ValidateToken(t.Context(), internalToken)
	if err != nil {
		t.Fatalf("expected valid internal token: %v", err)
	}
	if !internalIdentity.IsInternal() {
		t.Errorf("expected internal role")
	}

	// Test 4.3: Expired Token
	expiredClaims := jwt.MapClaims{
		"sub": "user-provider-a",
		"iss": "test-issuer",
		"exp": time.Now().Add(-10 * time.Minute).Unix(),
	}
	expiredToken := mintToken(expiredClaims)
	_, err = validator.ValidateToken(t.Context(), expiredToken)
	if err == nil {
		t.Fatal("expected expired token error, got nil")
	}

	// Test 4.4: Tenancy Validation
	if !auth.ValidateProviderTenancy(identity, "provider-a") {
		t.Error("provider-a identity should match provider-a")
	}
	if auth.ValidateProviderTenancy(identity, "provider-b") {
		t.Error("provider-a identity should NOT match provider-b")
	}
	if !auth.ValidateProviderTenancy(internalIdentity, "any-provider") {
		t.Error("internal identity should be allowed to access any provider")
	}

	// Test 4.5: HTTP Middleware
	authMiddleware := auth.Authenticate(validator)
	protectedHandler := authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	// Missing header -> 401
	reqMissing := httptest.NewRequest(http.MethodGet, "/test", nil)
	rrMissing := httptest.NewRecorder()
	protectedHandler.ServeHTTP(rrMissing, reqMissing)
	if rrMissing.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 on missing auth header, got %d", rrMissing.Code)
	}

	// Valid header -> 200
	reqValid := httptest.NewRequest(http.MethodGet, "/test", nil)
	reqValid.Header.Set("Authorization", "Bearer "+providerToken)
	rrValid := httptest.NewRecorder()
	protectedHandler.ServeHTTP(rrValid, reqValid)
	if rrValid.Code != http.StatusOK {
		t.Errorf("expected 200 on valid token, got %d", rrValid.Code)
	}

	// Internal guard with provider token -> 403 Forbidden
	internalOnlyHandler := authMiddleware(auth.RequireInternal()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))

	rrForbidden := httptest.NewRecorder()
	internalOnlyHandler.ServeHTTP(rrForbidden, reqValid)
	if rrForbidden.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for provider accessing internal route, got %d", rrForbidden.Code)
	}
}
