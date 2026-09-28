package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	deliveryhttp "github.com/JonasFranc1sco/backend-challenge-go/internal/delivery/http"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/domain"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/usecase"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/worker"
	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"
)

// Scenario 8: Cross-Protocol Concurrency (HTTP and SQS submitted at the same time).
// Proves: Unified domain idempotency across HTTP and SQS entry points.
func TestCrossProtocol_HTTPAndSQSParallel(t *testing.T) {
	env := SetupTestEnv(t)
	defer env.Pool.Close()

	ctx := context.Background()
	playerID := uuid.NewString()

	// 1. Open Wallet with 500.00 BRL
	initBal := MustNewNonNegMoney(t, "500.00", "BRL")
	wOut, err := env.OpenWalletUC.Execute(ctx, usecase.OpenWalletInput{
		PlayerID:       playerID,
		InitialBalance: initBal,
	})
	if err != nil {
		t.Fatalf("failed to open wallet: %v", err)
	}
	walletID := wOut.ID

	extID := "cross-proto-" + uuid.NewString()
	idempotencyKey := "provider-a:" + extID

	// Get real token for provider-a
	providerToken := FetchKeycloakToken(t, "provider-a", "provider-a-secret-123")

	// Build HTTP request body
	httpPayload := deliveryhttp.ProcessTransactionRequest{
		ProviderID:            "provider-a",
		ExternalTransactionID: extID,
		PlayerID:              playerID,
		WalletID:              walletID,
		RoundID:               "round-cross",
		GameID:                "fortune-chimp",
		Kind:                  string(domain.KindBet),
		Money: deliveryhttp.MoneyDTO{
			Amount:   "25.00",
			Currency: "BRL",
		},
	}
	httpBytes, _ := json.Marshal(httpPayload)

	// Build SQS message envelope
	messageID := "sqs-cross-" + uuid.NewString()
	sqsEnvelope := worker.SQSMessageEnvelope{
		MessageID:  messageID,
		Type:       "WagerTransactionRequested",
		OccurredAt: time.Now().UTC().Format(time.RFC3339),
		Data: worker.SQSWagerData{
			ProviderID:            "provider-a",
			ExternalTransactionID: extID,
			IdempotencyKey:        idempotencyKey,
			PlayerID:              playerID,
			WalletID:              walletID,
			RoundID:               "round-cross",
			GameID:                "fortune-chimp",
			Kind:                  "BET",
			Money: worker.SQSMoneyData{
				Amount:   "25.00",
				Currency: "BRL",
			},
		},
	}
	sqsBytes, _ := json.Marshal(sqsEnvelope)

	// SQS Consumer Worker
	sqsWorker := worker.NewSQSConsumerWorker(
		env.SQSClient, env.Config.SQSQueueURL, env.Transactor, env.InboxRepo,
		env.ProcessWagerUC, "cross-proto-consumer", 10, 2, env.Logger, env.Metrics,
	)

	var wg sync.WaitGroup
	var httpStatus int
	var httpBody deliveryhttp.TransactionResponse

	// Launch HTTP and SQS dispatch concurrently
	wg.Add(2)

	go func() {
		defer wg.Done()
		req := httptest.NewRequest(http.MethodPost, "/wagering/transactions", bytes.NewReader(httpBytes))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+providerToken)
		req.Header.Set("Idempotency-Key", idempotencyKey)

		rec := httptest.NewRecorder()
		env.HTTPHandler.ServeHTTP(rec, req)

		httpStatus = rec.Code
		_ = json.NewDecoder(rec.Body).Decode(&httpBody)
	}()

	go func() {
		defer wg.Done()
		_, err := env.SQSClient.SendMessage(ctx, &awssqs.SendMessageInput{
			QueueUrl:               aws.String(env.Config.SQSQueueURL),
			MessageBody:            aws.String(string(sqsBytes)),
			MessageGroupId:         aws.String(walletID),
			MessageDeduplicationId: aws.String(messageID),
		})
		if err != nil {
			t.Errorf("failed to enqueue cross-protocol message: %v", err)
		}
	}()

	wg.Wait()

	if httpStatus != http.StatusOK {
		t.Fatalf("HTTP request failed with status %d: %+v", httpStatus, httpBody)
	}

	// Start consumer to process SQS entry
	sqsWorker.Start(ctx)
	time.Sleep(500 * time.Millisecond)
	_ = sqsWorker.Stop(ctx)

	// Invariant: Final wallet balance must be EXACTLY 475.00 BRL (one single debit of 25.00)
	wallet, _ := env.WalletRepo.GetByID(ctx, walletID)
	expectedBalance := MustNewMoney(t, "475.00", "BRL")
	if !wallet.Balance().Equals(expectedBalance) {
		t.Errorf("cross-protocol balance mismatch: expected %s, got %s", expectedBalance.String(), wallet.Balance().String())
	}

	// Invariant: Exactly 1 debit in the ledger
	_, totalDebits, count, _ := env.LedgerRepo.GetBalanceSumByWallet(ctx, nil, walletID)
	if count != 2 || totalDebits != 2500 {
		t.Errorf("cross-protocol ledger mismatch: count=%d (expected 2), totalDebits=%d (expected 2500)", count, totalDebits)
	}

	// Full reconciliation audit
	recon, err := env.ReconcileUC.Execute(ctx, walletID)
	if err != nil || !recon.Consistent {
		t.Fatalf("cross-protocol reconciliation inconsistent: %+v, err=%v", recon, err)
	}
}

// Scenario 9: Real Keycloak OAuth 2.0 / OIDC Authentication, Role Enforcement, and Provider Tenancy.
func TestAuth_KeycloakRealIntegration(t *testing.T) {
	env := SetupTestEnv(t)
	defer env.Pool.Close()

	ctx := context.Background()

	// 1. Fetch real tokens from Keycloak
	internalToken := FetchKeycloakToken(t, "internal-service", "internal-secret-123")
	providerAToken := FetchKeycloakToken(t, "provider-a", "provider-a-secret-123")
	providerBToken := FetchKeycloakToken(t, "provider-b", "provider-b-secret-123")

	// Test 1: Request with missing token -> 401 Unauthorized
	openWalletBody := `{"playerId":"` + uuid.NewString() + `","initialBalance":{"amount":"100.00","currency":"BRL"}}`
	reqNoAuth := httptest.NewRequest(http.MethodPost, "/wallets", bytes.NewBufferString(openWalletBody))
	recNoAuth := httptest.NewRecorder()
	env.HTTPHandler.ServeHTTP(recNoAuth, reqNoAuth)
	if recNoAuth.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for unauthenticated request, got %d", recNoAuth.Code)
	}

	// Test 2: Request with invalid token -> 401 Unauthorized
	reqInvalidAuth := httptest.NewRequest(http.MethodPost, "/wallets", bytes.NewBufferString(openWalletBody))
	reqInvalidAuth.Header.Set("Authorization", "Bearer invalid-tampered-token")
	recInvalidAuth := httptest.NewRecorder()
	env.HTTPHandler.ServeHTTP(recInvalidAuth, reqInvalidAuth)
	if recInvalidAuth.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for invalid token, got %d", recInvalidAuth.Code)
	}

	// Test 3: Provider trying to access internal endpoint /wallets -> 403 Forbidden
	reqProviderOnInternal := httptest.NewRequest(http.MethodPost, "/wallets", bytes.NewBufferString(openWalletBody))
	reqProviderOnInternal.Header.Set("Authorization", "Bearer "+providerAToken)
	recProviderOnInternal := httptest.NewRecorder()
	env.HTTPHandler.ServeHTTP(recProviderOnInternal, reqProviderOnInternal)
	if recProviderOnInternal.Code != http.StatusForbidden {
		t.Errorf("expected 403 for provider accessing internal endpoint, got %d", recProviderOnInternal.Code)
	}

	// Test 4: Internal service accessing /wallets -> 201 Created
	playerID := uuid.NewString()
	validOpenWalletBody := `{"playerId":"` + playerID + `","initialBalance":{"amount":"100.00","currency":"BRL"}}`
	reqInternal := httptest.NewRequest(http.MethodPost, "/wallets", bytes.NewBufferString(validOpenWalletBody))
	reqInternal.Header.Set("Authorization", "Bearer "+internalToken)
	recInternal := httptest.NewRecorder()
	env.HTTPHandler.ServeHTTP(recInternal, reqInternal)
	if recInternal.Code != http.StatusCreated {
		t.Fatalf("expected 201 for internal service opening wallet, got %d (body=%s)", recInternal.Code, recInternal.Body.String())
	}

	var createdWallet deliveryhttp.WalletResponse
	_ = json.NewDecoder(recInternal.Body).Decode(&createdWallet)
	walletID := createdWallet.ID

	// Test 5: Provider A accessing Provider B's transaction payload -> 403 Forbidden (Tenancy Isolation)
	txPayloadMismatched := fmt.Sprintf(`{
		"providerId": "provider-b",
		"externalTransactionId": "bet-mismatch-1",
		"playerId": "%s",
		"walletId": "%s",
		"roundID": "round-1",
		"gameId": "fortune-chimp",
		"kind": "BET",
		"money": {"amount": "10.00", "currency": "BRL"}
	}`, playerID, walletID)

	reqTenancyMismatch := httptest.NewRequest(http.MethodPost, "/wagering/transactions", bytes.NewBufferString(txPayloadMismatched))
	reqTenancyMismatch.Header.Set("Authorization", "Bearer "+providerAToken)
	reqTenancyMismatch.Header.Set("Idempotency-Key", "provider-b:bet-mismatch-1")
	recTenancyMismatch := httptest.NewRecorder()
	env.HTTPHandler.ServeHTTP(recTenancyMismatch, reqTenancyMismatch)
	if recTenancyMismatch.Code != http.StatusForbidden {
		t.Errorf("expected 403 for provider tenancy mismatch, got %d", recTenancyMismatch.Code)
	}

	// Test 6: Provider A accessing its own transaction -> 200 OK
	extID := "bet-valid-a-" + uuid.NewString()
	txPayloadValid := fmt.Sprintf(`{
		"providerId": "provider-a",
		"externalTransactionId": "%s",
		"playerId": "%s",
		"walletId": "%s",
		"roundID": "round-1",
		"gameId": "fortune-chimp",
		"kind": "BET",
		"money": {"amount": "10.00", "currency": "BRL"}
	}`, extID, playerID, walletID)

	reqValidProvider := httptest.NewRequest(http.MethodPost, "/wagering/transactions", bytes.NewBufferString(txPayloadValid))
	reqValidProvider.Header.Set("Authorization", "Bearer "+providerAToken)
	reqValidProvider.Header.Set("Idempotency-Key", "provider-a:"+extID)
	recValidProvider := httptest.NewRecorder()
	env.HTTPHandler.ServeHTTP(recValidProvider, reqValidProvider)
	if recValidProvider.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid provider transaction, got %d (body=%s)", recValidProvider.Code, recValidProvider.Body.String())
	}

	// Test 7: Provider B trying to read Provider A's transaction -> 403 Forbidden
	reqProviderBReadA := httptest.NewRequest(http.MethodGet, "/providers/provider-a/wagering/transactions/"+extID, nil)
	reqProviderBReadA.Header.Set("Authorization", "Bearer "+providerBToken)
	recProviderBReadA := httptest.NewRecorder()
	env.HTTPHandler.ServeHTTP(recProviderBReadA, reqProviderBReadA)
	if recProviderBReadA.Code != http.StatusForbidden {
		t.Errorf("expected 403 for Provider B attempting to read Provider A transaction, got %d", recProviderBReadA.Code)
	}

	_ = ctx
}

// Scenario 10: PostgreSQL Schema & Trigger Constraints.
// Proves: Ledger append-only immutability, balance non-negativity, and unique constraints enforced at the database layer.
func TestDatabase_ConstraintsAndTriggers(t *testing.T) {
	env := SetupTestEnv(t)
	defer env.Pool.Close()

	ctx := context.Background()
	playerID := uuid.NewString()

	// 1. Open Wallet with 100.00 BRL
	initBal := MustNewNonNegMoney(t, "100.00", "BRL")
	wOut, err := env.OpenWalletUC.Execute(ctx, usecase.OpenWalletInput{
		PlayerID:       playerID,
		InitialBalance: initBal,
	})
	if err != nil {
		t.Fatalf("failed to open wallet: %v", err)
	}
	walletID := wOut.ID

	// 1. Test Ledger UPDATE Immutability
	// Directly execute UPDATE on wallet_ledger_entries -> Trigger trg_ledger_immutable must abort with error!
	_, err = env.Pool.Exec(ctx, "UPDATE wallet_ledger_entries SET amount = 999999 WHERE wallet_id = $1", walletID)
	if err == nil {
		t.Fatalf("expected PostgreSQL trigger trg_ledger_immutable to block UPDATE, but query succeeded!")
	}

	// 2. Test Ledger DELETE Immutability
	// Directly execute DELETE on wallet_ledger_entries -> Trigger must abort with error!
	_, err = env.Pool.Exec(ctx, "DELETE FROM wallet_ledger_entries WHERE wallet_id = $1", walletID)
	if err == nil {
		t.Fatalf("expected PostgreSQL trigger trg_ledger_immutable to block DELETE, but query succeeded!")
	}

	// 3. Test Non-Negative Balance Database Constraint
	// Directly attempt to force a negative balance via SQL -> Constraint chk_wallets_balance_non_negative must abort!
	_, err = env.Pool.Exec(ctx, "UPDATE wallets SET balance = -100 WHERE id = $1", walletID)
	if err == nil {
		t.Fatalf("expected PostgreSQL constraint chk_wallets_balance_non_negative to block negative balance, but query succeeded!")
	}

	// 4. Test (playerId, currency) unique constraint on wallets
	// Directly attempt to insert a duplicate wallet for the same player and currency
	_, err = env.Pool.Exec(ctx,
		"INSERT INTO wallets (id, player_id, currency, balance, version, created_at, updated_at) VALUES ($1, $2, $3, $4, 1, NOW(), NOW())",
		uuid.NewString(), playerID, "BRL", 5000,
	)
	if err == nil {
		t.Fatalf("expected unique constraint idx_wallets_player_currency to block duplicate wallet, but query succeeded!")
	}
}
