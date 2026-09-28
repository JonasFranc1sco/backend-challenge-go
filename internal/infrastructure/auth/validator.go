package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

var (
	ErrTokenMissing   = errors.New("authorization token is missing")
	ErrTokenInvalid   = errors.New("authorization token is invalid or signature verification failed")
	ErrTokenExpired   = errors.New("authorization token is expired")
	ErrInvalidIssuer  = errors.New("token issuer is invalid")
	ErrMissingKeyID   = errors.New("token header is missing 'kid' (Key ID)")
)

// JWTValidator defines the interface for validating OAuth 2.0 / OIDC JWT tokens.
type JWTValidator interface {
	ValidateToken(ctx context.Context, tokenString string) (*AuthIdentity, error)
}

type KeycloakJWTValidator struct {
	keyFetcher *JWKSKeyFetcher
	issuer     string
}

func NewKeycloakJWTValidator(keyFetcher *JWKSKeyFetcher, issuer string) *KeycloakJWTValidator {
	return &KeycloakJWTValidator{
		keyFetcher: keyFetcher,
		issuer:     strings.TrimSpace(issuer),
	}
}

type keycloakTokenClaims struct {
	jwt.RegisteredClaims
	ClientID    string                 `json:"client_id"`
	ProviderID  string                 `json:"provider_id"`
	Roles       []string               `json:"roles"`
	RealmAccess map[string]interface{} `json:"realm_access"`
}

// ValidateToken parses, verifies cryptographic signature via JWKS, and extracts identity claims.
func (v *KeycloakJWTValidator) ValidateToken(ctx context.Context, tokenString string) (*AuthIdentity, error) {
	tokenString = strings.TrimSpace(tokenString)
	if tokenString == "" {
		return nil, ErrTokenMissing
	}

	claims := &keycloakTokenClaims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (any, error) {
		// Enforce RSA signing algorithm
		if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}

		kidRaw, ok := token.Header["kid"]
		if !ok {
			return nil, ErrMissingKeyID
		}
		kid, ok := kidRaw.(string)
		if !ok || kid == "" {
			return nil, ErrMissingKeyID
		}

		return v.keyFetcher.GetKey(ctx, kid)
	})

	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrTokenExpired
		}
		return nil, fmt.Errorf("%w: %v", ErrTokenInvalid, err)
	}

	if !token.Valid {
		return nil, ErrTokenInvalid
	}

	// Validate issuer if configured
	if v.issuer != "" && claims.Issuer != v.issuer {
		return nil, fmt.Errorf("%w: expected '%s', got '%s'", ErrInvalidIssuer, v.issuer, claims.Issuer)
	}

	// Extract roles (supports direct 'roles' claim or Keycloak's 'realm_access.roles')
	rolesMap := make(map[string]bool)
	for _, r := range claims.Roles {
		rolesMap[strings.ToLower(strings.TrimSpace(r))] = true
	}
	if claims.RealmAccess != nil {
		if rawRoles, ok := claims.RealmAccess["roles"].([]interface{}); ok {
			for _, r := range rawRoles {
				if rStr, ok := r.(string); ok {
					rolesMap[strings.ToLower(strings.TrimSpace(rStr))] = true
				}
			}
		}
	}

	var roles []string
	for r := range rolesMap {
		roles = append(roles, r)
	}

	// If provider_id is empty, fallback to client_id if client starts with provider
	providerID := claims.ProviderID
	if providerID == "" && strings.HasPrefix(claims.ClientID, "provider-") {
		providerID = claims.ClientID
	}

	return &AuthIdentity{
		Subject:    claims.Subject,
		ClientID:   claims.ClientID,
		ProviderID: providerID,
		Roles:      roles,
	}, nil
}
