package auth

import (
	"encoding/json"
	"net/http"
	"strings"
)

type errorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func writeJSONError(w http.ResponseWriter, status int, errCode, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorResponse{
		Error:   errCode,
		Message: message,
	})
}

// Authenticate extracts and verifies the Bearer token from the Authorization header.
func Authenticate(validator JWTValidator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				writeJSONError(w, http.StatusUnauthorized, "unauthorized", "missing Authorization header")
				return
			}

			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
				writeJSONError(w, http.StatusUnauthorized, "unauthorized", "invalid Authorization header format: expected 'Bearer <token>'")
				return
			}

			tokenString := parts[1]
			identity, err := validator.ValidateToken(r.Context(), tokenString)
			if err != nil {
				writeJSONError(w, http.StatusUnauthorized, "unauthorized", err.Error())
				return
			}

			// Inject identity into context
			ctx := WithAuthIdentity(r.Context(), identity)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireInternal ensures that the authenticated caller has the 'internal' administrative role.
func RequireInternal() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity, ok := GetAuthIdentity(r.Context())
			if !ok || !identity.IsInternal() {
				writeJSONError(w, http.StatusForbidden, "forbidden", "internal service authorization required")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireProvider ensures that the authenticated caller has the 'provider' role.
func RequireProvider() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity, ok := GetAuthIdentity(r.Context())
			if !ok || !identity.HasRole("provider") {
				writeJSONError(w, http.StatusForbidden, "forbidden", "provider role required")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ValidateProviderTenancy verifies that the authenticated provider matches the requested providerID.
func ValidateProviderTenancy(identity *AuthIdentity, requestedProviderID string) bool {
	if identity == nil {
		return false
	}
	// Internal services can access any provider data
	if identity.IsInternal() {
		return true
	}
	return identity.ProviderID == strings.TrimSpace(requestedProviderID)
}
