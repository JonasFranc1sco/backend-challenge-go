package auth

import (
	"context"
	"strings"
)

type contextKey string

const authIdentityKey contextKey = "auth_identity"

// AuthIdentity holds the authenticated claims extracted from the verified Keycloak JWT.
type AuthIdentity struct {
	Subject    string   `json:"sub"`
	ClientID   string   `json:"client_id"`
	ProviderID string   `json:"provider_id"`
	Roles      []string `json:"roles"`
}

// HasRole checks if the identity possesses the given role.
func (id *AuthIdentity) HasRole(targetRole string) bool {
	if id == nil {
		return false
	}
	targetRole = strings.ToLower(strings.TrimSpace(targetRole))
	for _, r := range id.Roles {
		if strings.ToLower(strings.TrimSpace(r)) == targetRole {
			return true
		}
	}
	return false
}

// IsInternal returns true if the authenticated identity has the internal role.
func (id *AuthIdentity) IsInternal() bool {
	return id.HasRole("internal")
}

// IsProvider returns true if the identity has provider role and matches the given provider ID.
func (id *AuthIdentity) IsProvider(expectedProviderID string) bool {
	if !id.HasRole("provider") {
		return false
	}
	return id.ProviderID == strings.TrimSpace(expectedProviderID)
}

// WithAuthIdentity stores the AuthIdentity in the request context.
func WithAuthIdentity(ctx context.Context, identity *AuthIdentity) context.Context {
	return context.WithValue(ctx, authIdentityKey, identity)
}

// GetAuthIdentity retrieves the AuthIdentity from the request context.
func GetAuthIdentity(ctx context.Context) (*AuthIdentity, bool) {
	id, ok := ctx.Value(authIdentityKey).(*AuthIdentity)
	return id, ok && id != nil
}
