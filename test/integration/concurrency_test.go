package integration

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/JonasFranc1sco/backend-challenge-go/internal/domain"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/usecase"
	"github.com/google/uuid"
)

// Scenario 1: 50 concurrent executions of the exact same bet
// Proves: Single debit, 49 idempotent replays, persistent ledger integrity, exact balance.
func TestConcurrency_50ParallelSameBet(t *testing.T) {
	env := SetupTestEnv(t)
	defer env.Pool.Close()

	ctx := context.Background()
	playerID := uuid.NewString()

	// 1. Open Wallet with 1000.00 BRL
	initialBalance := MustNewNonNegMoney(t, "1000.00", "BRL")
	walletOut, err := env.OpenWalletUC.Execute(ctx, usecase.OpenWalletInput{
		PlayerID:       playerID,
		InitialBalance: initialBalance,
	})
	if err != nil {
		t.Fatalf("failed to open wallet: %v", err)
	}

	walletID := walletOut.ID
	betAmount := MustNewMoney(t, "25.00", "BRL")
	externalTxID := "ext-bet-" + uuid.NewString()
	idempotencyKey := "provider-a:" + externalTxID

	const concurrency = 50
	var wg sync.WaitGroup
	var nonReplayCount int32
	var replayCount int32

	results := make([]*usecase.ProcessWagerOutput, concurrency)
	errorsList := make([]error, concurrency)

	// Launch 50 concurrent requests
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go func(idx int) {
			defer wg.Done()

			out, err := env.ProcessWagerUC.Execute(ctx, usecase.ProcessWagerInput{
				ProviderID:            "provider-a",
				ExternalTransactionID: externalTxID,
				IdempotencyKey:        idempotencyKey,
				PlayerID:              playerID,
				WalletID:              walletID,
				RoundID:               "round-1",
				GameID:                "fortune-tiger",
				Kind:                  domain.KindBet,
				Money:                 betAmount,
				CorrelationID:         uuid.NewString(),
			})

			results[idx] = out
			errorsList[idx] = err

			if err == nil && out != nil {
				if out.IdempotentReplay {
					atomic.AddInt32(&replayCount, 1)
				} else {
					atomic.AddInt32(&nonReplayCount, 1)
				}
			}
		}(i)
	}
	wg.Wait()

	// Verify all returned without error
	for i, err := range errorsList {
		if err != nil {
			t.Fatalf("request %d failed: %v", i, err)
		}
		if results[i].Status != domain.StatusProcessed {
			t.Fatalf("request %d had unexpected status: %s", i, results[i].Status)
		}
	}

	// Invariant: Exactly 1 processed originally, 49 idempotent replays
	if nonReplayCount != 1 {
		t.Errorf("expected exactly 1 non-replay execution, got %d", nonReplayCount)
	}
	if replayCount != concurrency-1 {
		t.Errorf("expected %d replays, got %d", concurrency-1, replayCount)
	}

	// Verify final wallet balance: 1000.00 - 25.00 = 975.00 BRL
	wallet, err := env.WalletRepo.GetByID(ctx, walletID)
	if err != nil {
		t.Fatalf("failed to reload wallet: %v", err)
	}
	expectedBalance := MustNewMoney(t, "975.00", "BRL")
	if !wallet.Balance().Equals(expectedBalance) {
		t.Errorf("expected wallet balance %s, got %s", expectedBalance.String(), wallet.Balance().String())
	}

	// Verify ledger entries: 1 OPENING credit + 1 BET debit = 2 entries
	totalCredits, totalDebits, count, err := env.LedgerRepo.GetBalanceSumByWallet(ctx, nil, walletID)
	if err != nil {
		t.Fatalf("failed to sum ledger: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 ledger entries, got %d", count)
	}
	if totalDebits != 2500 { // 25.00 BRL in cents
		t.Errorf("expected total debits 2500, got %d", totalDebits)
	}
	if totalCredits != 100000 { // 1000.00 BRL in cents
		t.Errorf("expected total credits 100000, got %d", totalCredits)
	}

	// Run Reconciliation UseCase
	recon, err := env.ReconcileUC.Execute(ctx, walletID)
	if err != nil {
		t.Fatalf("reconciliation failed: %v", err)
	}
	if !recon.Consistent {
		t.Errorf("reconciliation reported inconsistency: stored=%s calculated=%s diff=%s",
			recon.StoredBalance.String(), recon.CalculatedBalance.String(), recon.Difference.String())
	}
}

// Scenario 2: Two concurrent bets of 80.00 BRL on balance of 100.00 BRL
// Proves: 1 processed, 1 rejected with INSUFFICIENT_FUNDS, final balance 20.00 BRL, 1 ledger debit.
func TestConcurrency_TwoBets80OnBalance100(t *testing.T) {
	env := SetupTestEnv(t)
	defer env.Pool.Close()

	ctx := context.Background()
	playerID := uuid.NewString()

	// 1. Open Wallet with exactly 100.00 BRL
	initialBalance := MustNewNonNegMoney(t, "100.00", "BRL")
	walletOut, err := env.OpenWalletUC.Execute(ctx, usecase.OpenWalletInput{
		PlayerID:       playerID,
		InitialBalance: initialBalance,
	})
	if err != nil {
		t.Fatalf("failed to open wallet: %v", err)
	}

	walletID := walletOut.ID
	betAmount := MustNewMoney(t, "80.00", "BRL")

	extID_A := "bet-A-" + uuid.NewString()
	extID_B := "bet-B-" + uuid.NewString()

	key_A := "provider-a:" + extID_A
	key_B := "provider-a:" + extID_B

	var wg sync.WaitGroup
	var outA, outB *usecase.ProcessWagerOutput
	var errA, errB error

	startGate := make(chan struct{})
	wg.Add(2)

	go func() {
		defer wg.Done()
		<-startGate
		outA, errA = env.ProcessWagerUC.Execute(ctx, usecase.ProcessWagerInput{
			ProviderID:            "provider-a",
			ExternalTransactionID: extID_A,
			IdempotencyKey:        key_A,
			PlayerID:              playerID,
			WalletID:              walletID,
			RoundID:               "round-A",
			GameID:                "fortune-tiger",
			Kind:                  domain.KindBet,
			Money:                 betAmount,
			CorrelationID:         uuid.NewString(),
		})
	}()

	go func() {
		defer wg.Done()
		<-startGate
		outB, errB = env.ProcessWagerUC.Execute(ctx, usecase.ProcessWagerInput{
			ProviderID:            "provider-a",
			ExternalTransactionID: extID_B,
			IdempotencyKey:        key_B,
			PlayerID:              playerID,
			WalletID:              walletID,
			RoundID:               "round-B",
			GameID:                "fortune-tiger",
			Kind:                  domain.KindBet,
			Money:                 betAmount,
			CorrelationID:         uuid.NewString(),
		})
	}()

	// Release both requests simultaneously
	close(startGate)
	wg.Wait()

	if errA != nil || errB != nil {
		t.Fatalf("unexpected execution error: errA=%v, errB=%v", errA, errB)
	}

	// Exactly one must be PROCESSED and one REJECTED
	statuses := []domain.TransactionStatus{outA.Status, outB.Status}
	var processedCount, rejectedCount int
	for _, s := range statuses {
		if s == domain.StatusProcessed {
			processedCount++
		}
		if s == domain.StatusRejected {
			rejectedCount++
		}
	}

	if processedCount != 1 || rejectedCount != 1 {
		t.Fatalf("expected exactly 1 PROCESSED and 1 REJECTED, got outA=%s (code=%s), outB=%s (code=%s)",
			outA.Status, outA.FailureCode, outB.Status, outB.FailureCode)
	}

	// Verify rejected failure code is strictly INSUFFICIENT_FUNDS
	rejectedOut := outA
	if outB.Status == domain.StatusRejected {
		rejectedOut = outB
	}
	if rejectedOut.FailureCode != domain.FailureCodeInsufficientFunds {
		t.Errorf("expected failure code %s, got %s", domain.FailureCodeInsufficientFunds, rejectedOut.FailureCode)
	}

	// Verify final balance: 100.00 - 80.00 = 20.00 BRL
	wallet, err := env.WalletRepo.GetByID(ctx, walletID)
	if err != nil {
		t.Fatalf("failed to retrieve wallet: %v", err)
	}
	expectedBalance := MustNewMoney(t, "20.00", "BRL")
	if !wallet.Balance().Equals(expectedBalance) {
		t.Errorf("expected wallet balance %s, got %s", expectedBalance.String(), wallet.Balance().String())
	}

	// Verify ledger: 1 OPENING credit + 1 BET debit = 2 entries (rejected bet creates NO ledger entry)
	totalCredits, totalDebits, count, err := env.LedgerRepo.GetBalanceSumByWallet(ctx, nil, walletID)
	if err != nil {
		t.Fatalf("failed to query ledger: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 ledger entries, got %d", count)
	}
	if totalDebits != 8000 {
		t.Errorf("expected total debits 8000, got %d", totalDebits)
	}
	if totalCredits != 10000 {
		t.Errorf("expected total credits 10000, got %d", totalCredits)
	}

	// Resend both operations -> Must return identical results (idempotent replay)
	replayA, err := env.ProcessWagerUC.Execute(ctx, usecase.ProcessWagerInput{
		ProviderID:            "provider-a",
		ExternalTransactionID: extID_A,
		IdempotencyKey:        key_A,
		PlayerID:              playerID,
		WalletID:              walletID,
		RoundID:               "round-A",
		GameID:                "fortune-tiger",
		Kind:                  domain.KindBet,
		Money:                 betAmount,
		CorrelationID:         uuid.NewString(),
	})
	if err != nil || !replayA.IdempotentReplay || replayA.Status != outA.Status {
		t.Errorf("replay A failed: out=%+v, err=%v", replayA, err)
	}

	replayB, err := env.ProcessWagerUC.Execute(ctx, usecase.ProcessWagerInput{
		ProviderID:            "provider-a",
		ExternalTransactionID: extID_B,
		IdempotencyKey:        key_B,
		PlayerID:              playerID,
		WalletID:              walletID,
		RoundID:               "round-B",
		GameID:                "fortune-tiger",
		Kind:                  domain.KindBet,
		Money:                 betAmount,
		CorrelationID:         uuid.NewString(),
	})
	if err != nil || !replayB.IdempotentReplay || replayB.Status != outB.Status {
		t.Errorf("replay B failed: out=%+v, err=%v", replayB, err)
	}

	// Audit consistency
	recon, err := env.ReconcileUC.Execute(ctx, walletID)
	if err != nil || !recon.Consistent {
		t.Fatalf("reconciliation failed or inconsistent: %+v, err=%v", recon, err)
	}
}

// Scenario 3: Multiple independent wallets advancing concurrently
// Proves: Parallel throughput without global locks.
func TestConcurrency_IndependentWalletsInParallel(t *testing.T) {
	env := SetupTestEnv(t)
	defer env.Pool.Close()

	ctx := context.Background()
	const numWallets = 10
	const betsPerWallet = 5

	type walletInfo struct {
		id       string
		playerID string
	}
	wallets := make([]walletInfo, numWallets)

	// Create 10 distinct wallets with 500.00 BRL each
	for i := 0; i < numWallets; i++ {
		playerID := uuid.NewString()
		initBal := MustNewNonNegMoney(t, "500.00", "BRL")
		wOut, err := env.OpenWalletUC.Execute(ctx, usecase.OpenWalletInput{
			PlayerID:       playerID,
			InitialBalance: initBal,
		})
		if err != nil {
			t.Fatalf("failed to open wallet %d: %v", i, err)
		}
		wallets[i] = walletInfo{id: wOut.ID, playerID: playerID}
	}

	var wg sync.WaitGroup
	betAmount := MustNewMoney(t, "10.00", "BRL")

	// Execute 5 bets per wallet concurrently (50 total operations across 10 wallets)
	for _, w := range wallets {
		for b := 0; b < betsPerWallet; b++ {
			wg.Add(1)
			go func(wID, pID string, betNum int) {
				defer wg.Done()
				extID := fmt.Sprintf("bet-%s-%d", wID, betNum)
				key := fmt.Sprintf("provider-a:%s", extID)

				out, err := env.ProcessWagerUC.Execute(ctx, usecase.ProcessWagerInput{
					ProviderID:            "provider-a",
					ExternalTransactionID: extID,
					IdempotencyKey:        key,
					PlayerID:              pID,
					WalletID:              wID,
					RoundID:               fmt.Sprintf("round-%d", betNum),
					GameID:                "fortune-tiger",
					Kind:                  domain.KindBet,
					Money:                 betAmount,
					CorrelationID:         uuid.NewString(),
				})
				if err != nil || out.Status != domain.StatusProcessed {
					t.Errorf("wallet %s bet %d failed: out=%+v, err=%v", wID, betNum, out, err)
				}
			}(w.id, w.playerID, b)
		}
	}
	wg.Wait()

	// Verify each wallet: 500.00 - (5 * 10.00) = 450.00 BRL
	expectedBal := MustNewMoney(t, "450.00", "BRL")
	for i, w := range wallets {
		wallet, err := env.WalletRepo.GetByID(ctx, w.id)
		if err != nil {
			t.Fatalf("failed to get wallet %d: %v", i, err)
		}
		if !wallet.Balance().Equals(expectedBal) {
			t.Errorf("wallet %d balance mismatch: expected %s, got %s", i, expectedBal.String(), wallet.Balance().String())
		}

		recon, err := env.ReconcileUC.Execute(ctx, w.id)
		if err != nil || !recon.Consistent {
			t.Errorf("wallet %d reconciliation inconsistent: %+v, err=%v", i, recon, err)
		}
	}
}

// Scenario 4: Concurrency distributed across 3 independent instances (pools/memory)
// Proves: Multi-instance coordination via PostgreSQL row locks without local-memory leakage.
func TestConcurrency_ThreeIndependentInstances(t *testing.T) {
	env1 := SetupTestEnv(t)
	defer env1.Pool.Close()

	env2 := CreateNewInstance(t, env1.Config)
	defer env2.Pool.Close()

	env3 := CreateNewInstance(t, env1.Config)
	defer env3.Pool.Close()

	ctx := context.Background()
	playerID := uuid.NewString()

	// Open wallet with 300.00 BRL using instance 1
	initBal := MustNewNonNegMoney(t, "300.00", "BRL")
	wOut, err := env1.OpenWalletUC.Execute(ctx, usecase.OpenWalletInput{
		PlayerID:       playerID,
		InitialBalance: initBal,
	})
	if err != nil {
		t.Fatalf("failed to open wallet: %v", err)
	}
	walletID := wOut.ID

	instances := []*TestEnv{env1, env2, env3}
	const operationsPerInstance = 10
	betAmount := MustNewMoney(t, "5.00", "BRL")

	var wg sync.WaitGroup
	// Concurrently send 10 bets to instance 1, 10 to instance 2, 10 to instance 3 (30 bets total)
	for instIdx, inst := range instances {
		for op := 0; op < operationsPerInstance; op++ {
			wg.Add(1)
			go func(app *TestEnv, instID, opID int) {
				defer wg.Done()
				extID := fmt.Sprintf("multi-inst-%d-%d-%s", instID, opID, uuid.NewString())
				key := fmt.Sprintf("provider-a:%s", extID)

				out, err := app.ProcessWagerUC.Execute(ctx, usecase.ProcessWagerInput{
					ProviderID:            "provider-a",
					ExternalTransactionID: extID,
					IdempotencyKey:        key,
					PlayerID:              playerID,
					WalletID:              walletID,
					RoundID:               fmt.Sprintf("round-%d-%d", instID, opID),
					GameID:                "fortune-tiger",
					Kind:                  domain.KindBet,
					Money:                 betAmount,
					CorrelationID:         uuid.NewString(),
				})
				if err != nil || out.Status != domain.StatusProcessed {
					t.Errorf("instance %d op %d failed: out=%+v, err=%v", instID, opID, out, err)
				}
			}(inst, instIdx, op)
		}
	}
	wg.Wait()

	// 300.00 - (30 * 5.00) = 150.00 BRL
	expectedBalance := MustNewMoney(t, "150.00", "BRL")
	wallet, err := env1.WalletRepo.GetByID(ctx, walletID)
	if err != nil {
		t.Fatalf("failed to retrieve wallet: %v", err)
	}
	if !wallet.Balance().Equals(expectedBalance) {
		t.Errorf("multi-instance balance mismatch: expected %s, got %s", expectedBalance.String(), wallet.Balance().String())
	}

	recon, err := env3.ReconcileUC.Execute(ctx, walletID)
	if err != nil || !recon.Consistent {
		t.Fatalf("multi-instance reconciliation inconsistent: %+v, err=%v", recon, err)
	}
}
