package usecase

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/JonasFranc1sco/backend-challenge-go/internal/domain"
)

// CanonicalPayload represents the normalized business fields used to compute the deterministic SHA-256 hash.
// Note: Transport metadata (e.g. HTTP headers, SQS messageId) and the Idempotency-Key itself are strictly excluded.
type CanonicalPayload struct {
	ExternalTransactionID          string
	GameID                         string
	Kind                           string
	Money                          domain.Money
	PlayerID                       string
	ProviderID                     string
	ReferenceExternalTransactionID string
	RoundID                        string
	WalletID                       string
}

// canonicalJSON mirrors CanonicalPayload with explicit alphabetical key ordering for canonical JSON representation.
type canonicalJSON struct {
	ExternalTransactionID          string        `json:"externalTransactionId"`
	GameID                         string        `json:"gameId"`
	Kind                           string        `json:"kind"`
	Money                          canonicalMoney `json:"money"`
	PlayerID                       string        `json:"playerId"`
	ProviderID                     string        `json:"providerId"`
	ReferenceExternalTransactionID string        `json:"referenceExternalTransactionId,omitempty"`
	RoundID                        string        `json:"roundId"`
	WalletID                       string        `json:"walletId"`
}

type canonicalMoney struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// ComputeCanonicalPayloadHash serializes the business payload into canonical JSON with sorted keys
// and returns the 64-character hexadecimal SHA-256 digest.
func ComputeCanonicalPayloadHash(p CanonicalPayload) (string, error) {
	cj := canonicalJSON{
		ExternalTransactionID:          strings.TrimSpace(p.ExternalTransactionID),
		GameID:                         strings.TrimSpace(p.GameID),
		Kind:                           strings.TrimSpace(p.Kind),
		Money: canonicalMoney{
			Amount:   p.Money.String(),
			Currency: strings.ToUpper(strings.TrimSpace(p.Money.Currency())),
		},
		PlayerID:                       strings.TrimSpace(p.PlayerID),
		ProviderID:                     strings.TrimSpace(p.ProviderID),
		ReferenceExternalTransactionID: strings.TrimSpace(p.ReferenceExternalTransactionID),
		RoundID:                        strings.TrimSpace(p.RoundID),
		WalletID:                       strings.TrimSpace(p.WalletID),
	}

	data, err := json.Marshal(cj)
	if err != nil {
		return "", fmt.Errorf("failed to marshal canonical JSON: %w", err)
	}

	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}
