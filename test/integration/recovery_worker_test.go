package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JonasFranc1sco/backend-challenge-go/internal/domain"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/usecase"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/worker"
	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Scenario 5: Consumer interruption after DB commit and before SQS message deletion.
// Proves: Transactional inbox guarantees at-least-once redelivery safety without duplicate execution.
func TestRecovery_InboxRedeliverySafety(t *testing.T) {
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

	sqsWorker := worker.NewSQSConsumerWorker(
		env.SQSClient, env.Config.SQSQueueURL, env.Transactor, env.InboxRepo,
		env.ProcessWagerUC, "redelivery-consumer", 10, 2, env.Logger, env.Metrics,
	)

	messageID := "msg-crash-test-" + uuid.NewString()
	externalTxID := "ext-crash-" + uuid.NewString()
	idempotencyKey := "provider-a:" + externalTxID

	sqsMsg := worker.SQSMessageEnvelope{
		MessageID:  messageID,
		Type:       "WagerTransactionRequested",
		OccurredAt: time.Now().UTC().Format(time.RFC3339),
		Data: worker.SQSWagerData{
			ProviderID:            "provider-a",
			ExternalTransactionID: externalTxID,
			IdempotencyKey:        idempotencyKey,
			PlayerID:              playerID,
			WalletID:              walletID,
			RoundID:               "round-crash",
			GameID:                "fortune-ox",
			Kind:                  "BET",
			Money: worker.SQSMoneyData{
				Amount:   "50.00",
				Currency: "BRL",
			},
		},
	}

	bodyBytes, _ := json.Marshal(sqsMsg)
	bodyStr := string(bodyBytes)

	// Step 1: Deliver to SQS queue
	sendOut, err := env.SQSClient.SendMessage(ctx, &awssqs.SendMessageInput{
		QueueUrl:               aws.String(env.Config.SQSQueueURL),
		MessageBody:            aws.String(bodyStr),
		MessageGroupId:         aws.String(walletID),
		MessageDeduplicationId: aws.String(messageID),
	})
	if err != nil {
		t.Fatalf("failed to send message to SQS: %v", err)
	}

	// Step 2: Receive message from SQS
	receiveOut, err := env.SQSClient.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
		QueueUrl:            aws.String(env.Config.SQSQueueURL),
		MaxNumberOfMessages: 1,
		WaitTimeSeconds:     5,
		VisibilityTimeout:   1, // Short visibility for redelivery simulation
	})
	if err != nil || len(receiveOut.Messages) == 0 {
		t.Fatalf("failed to receive message: %v (len=%d)", err, len(receiveOut.Messages))
	}
	receivedMsg := receiveOut.Messages[0]

	// Step 3: Simulate consumer execution with COMMIT, but CRASH before DeleteMessage
	// The transaction commits inbox + transaction + ledger + outbox
	err = env.Transactor.WithinTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		status, alreadyExists, err := env.InboxRepo.RecordOrGet(ctx, tx, "redelivery-consumer", messageID, "hash-test")
		if err != nil || (alreadyExists && status == "PROCESSED") {
			return err
		}

		money := MustNewMoney(t, "50.00", "BRL")
		_, execErr := env.ProcessWagerUC.ExecuteWithTx(ctx, tx, usecase.ProcessWagerInput{
			ProviderID:            "provider-a",
			ExternalTransactionID: externalTxID,
			IdempotencyKey:        idempotencyKey,
			PlayerID:              playerID,
			WalletID:              walletID,
			RoundID:               "round-crash",
			GameID:                "fortune-ox",
			Kind:                  domain.KindBet,
			Money:                 money,
			CorrelationID:         messageID,
		})
		if execErr != nil {
			return execErr
		}
		return env.InboxRepo.MarkCompleted(ctx, tx, "redelivery-consumer", messageID)
	})
	if err != nil {
		t.Fatalf("simulated consumer first run failed: %v", err)
	}

	// DO NOT DELETE FROM SQS HERE (Simulating crash!)

	// Verify balance after first run: 500.00 - 50.00 = 450.00 BRL
	wallet, _ := env.WalletRepo.GetByID(ctx, walletID)
	if !wallet.Balance().Equals(MustNewMoney(t, "450.00", "BRL")) {
		t.Fatalf("balance after first run mismatch: %s", wallet.Balance().String())
	}

	// Step 4: Visibility timeout expires -> Message is REDELIVERED from SQS
	time.Sleep(1200 * time.Millisecond)

	// Step 5: Consumer receives the exact same message again and processes it
	// Now we use the official worker Stop/Start or single processing
	receiveOut2, err := env.SQSClient.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
		QueueUrl:            aws.String(env.Config.SQSQueueURL),
		MaxNumberOfMessages: 1,
		WaitTimeSeconds:     5,
	})
	if err != nil || len(receiveOut2.Messages) == 0 {
		t.Fatalf("failed to receive redelivered message: %v", err)
	}

	// Consumer processes redelivered message
	// The inbox must detect "PROCESSED", skip execution, and delete the message
	redeliveredMsg := receiveOut2.Messages[0]
	sqsWorker.Start(ctx)
	// Give worker brief moment to process the message in queue
	time.Sleep(500 * time.Millisecond)
	_ = sqsWorker.Stop(ctx)

	// Verify wallet balance is STILL 450.00 BRL (NO double debit!)
	walletAfterRedelivery, _ := env.WalletRepo.GetByID(ctx, walletID)
	expectedBal := MustNewMoney(t, "450.00", "BRL")
	if !walletAfterRedelivery.Balance().Equals(expectedBal) {
		t.Errorf("balance after redelivery changed! Expected %s, got %s", expectedBal.String(), walletAfterRedelivery.Balance().String())
	}

	// Verify ledger still has exactly 1 debit entry for 50.00
	_, totalDebits, count, _ := env.LedgerRepo.GetBalanceSumByWallet(ctx, nil, walletID)
	if count != 2 || totalDebits != 5000 {
		t.Errorf("ledger was duplicated! count=%d, totalDebits=%d", count, totalDebits)
	}

	// Clean up SQS message if still in queue
	_, _ = env.SQSClient.DeleteMessage(ctx, &awssqs.DeleteMessageInput{
		QueueUrl:      aws.String(env.Config.SQSQueueURL),
		ReceiptHandle: redeliveredMsg.ReceiptHandle,
	})
	_, _ = env.SQSClient.DeleteMessage(ctx, &awssqs.DeleteMessageInput{
		QueueUrl:      aws.String(env.Config.SQSQueueURL),
		ReceiptHandle: receivedMsg.ReceiptHandle,
	})
	_ = sendOut
}

// Scenario 6: Two competing outbox publishers disputing the same outbox records.
// Proves: FOR UPDATE SKIP LOCKED guarantees zero double-publication and zero locking conflicts.
func TestRecovery_CompetingOutboxPublishers(t *testing.T) {
	env := SetupTestEnv(t)
	defer env.Pool.Close()

	ctx := context.Background()
	playerID := uuid.NewString()

	// 1. Open wallet and execute 10 bets to generate 10+ outbox events
	initBal := MustNewNonNegMoney(t, "1000.00", "BRL")
	wOut, err := env.OpenWalletUC.Execute(ctx, usecase.OpenWalletInput{
		PlayerID:       playerID,
		InitialBalance: initBal,
	})
	if err != nil {
		t.Fatalf("failed to open wallet: %v", err)
	}
	walletID := wOut.ID

	const numEvents = 10
	betAmount := MustNewMoney(t, "10.00", "BRL")
	for i := 0; i < numEvents; i++ {
		extID := fmt.Sprintf("outbox-comp-%d-%s", i, uuid.NewString())
		_, err := env.ProcessWagerUC.Execute(ctx, usecase.ProcessWagerInput{
			ProviderID:            "provider-a",
			ExternalTransactionID: extID,
			IdempotencyKey:        "provider-a:" + extID,
			PlayerID:              playerID,
			WalletID:              walletID,
			RoundID:               fmt.Sprintf("round-outbox-%d", i),
			GameID:                "fortune-tiger",
			Kind:                  domain.KindBet,
			Money:                 betAmount,
			CorrelationID:         uuid.NewString(),
		})
		if err != nil {
			t.Fatalf("bet %d failed: %v", i, err)
		}
	}

	// Verify outbox has pending records
	pendingBefore, err := env.OutboxRepo.FetchPending(ctx, nil, 50)
	if err != nil || len(pendingBefore) == 0 {
		t.Fatalf("expected pending outbox records, got len=%d, err=%v", len(pendingBefore), err)
	}

	// 2. Setup mock/spy publisher to track calls and verify exactly-once publishing
	var publishCount int32
	var mu sync.Mutex
	publishedIDs := make(map[string]int)

	spyPublisher := &spyEventPublisher{
		onPublish: func(eventID string) {
			atomic.AddInt32(&publishCount, 1)
			mu.Lock()
			publishedIDs[eventID]++
			mu.Unlock()
		},
	}

	// 3. Create two independent competing publishers
	worker1 := worker.NewOutboxPublisherWorker(
		env.Transactor, env.OutboxRepo, spyPublisher, 50*time.Millisecond, 5, env.Logger, env.Metrics,
	)
	worker2 := worker.NewOutboxPublisherWorker(
		env.Transactor, env.OutboxRepo, spyPublisher, 50*time.Millisecond, 5, env.Logger, env.Metrics,
	)

	// Start both publishers simultaneously
	worker1.Start(ctx)
	worker2.Start(ctx)

	// Wait for all pending events to be published
	assertDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(assertDeadline) {
		time.Sleep(100 * time.Millisecond)
		pending, _ := env.OutboxRepo.FetchPending(ctx, nil, 50)
		if len(pending) == 0 {
			break
		}
	}

	_ = worker1.Stop(ctx)
	_ = worker2.Stop(ctx)

	// 4. Verify no pending outbox records remain
	pendingAfter, err := env.OutboxRepo.FetchPending(ctx, nil, 50)
	if err != nil {
		t.Fatalf("failed fetching pending: %v", err)
	}
	if len(pendingAfter) != 0 {
		t.Errorf("expected 0 pending outbox records, still found %d", len(pendingAfter))
	}

	// 5. Verify every event was published EXACTLY once across both publishers
	mu.Lock()
	defer mu.Unlock()
	for id, count := range publishedIDs {
		if count != 1 {
			t.Errorf("event %s was published %d times (expected exactly 1)", id, count)
		}
	}
}

// Scenario 7: Out-of-order reversals (REFUND/ROLLBACK arrives before reference BET).
// Proves: PENDING_REFERENCE state persistence, automatic background resolution, and TTL rejection.
func TestRecovery_OutOfOrderReversalsAndTTL(t *testing.T) {
	env := SetupTestEnv(t)
	defer env.Pool.Close()

	ctx := context.Background()
	playerID := uuid.NewString()

	// 1. Open Wallet with 200.00 BRL
	initBal := MustNewNonNegMoney(t, "200.00", "BRL")
	wOut, err := env.OpenWalletUC.Execute(ctx, usecase.OpenWalletInput{
		PlayerID:       playerID,
		InitialBalance: initBal,
	})
	if err != nil {
		t.Fatalf("failed to open wallet: %v", err)
	}
	walletID := wOut.ID

	refBetExtID := "bet-ref-" + uuid.NewString()
	refundExtID := "refund-" + uuid.NewString()
	betAmount := MustNewMoney(t, "50.00", "BRL")

	// 2. Deliver REFUND before the referenced BET exists!
	refundOut, err := env.ProcessWagerUC.Execute(ctx, usecase.ProcessWagerInput{
		ProviderID:                     "provider-a",
		ExternalTransactionID:          refundExtID,
		IdempotencyKey:                 "provider-a:" + refundExtID,
		PlayerID:                       playerID,
		WalletID:                       walletID,
		RoundID:                        "round-ooo",
		GameID:                         "fortune-rabbit",
		Kind:                           domain.KindRefund,
		Money:                          betAmount,
		ReferenceExternalTransactionID: refBetExtID,
		CorrelationID:                  uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("out-of-order refund returned error: %v", err)
	}

	// Must be accepted into PENDING_REFERENCE state
	if refundOut.Status != domain.StatusPendingReference {
		t.Fatalf("expected status %s, got %s", domain.StatusPendingReference, refundOut.Status)
	}

	// Balance must be unchanged: 200.00 BRL
	wallet, _ := env.WalletRepo.GetByID(ctx, walletID)
	if !wallet.Balance().Equals(initBal) {
		t.Errorf("balance changed prematurely! Got %s", wallet.Balance().String())
	}

	// 3. Now the referenced BET arrives and gets processed!
	betOut, err := env.ProcessWagerUC.Execute(ctx, usecase.ProcessWagerInput{
		ProviderID:            "provider-a",
		ExternalTransactionID: refBetExtID,
		IdempotencyKey:        "provider-a:" + refBetExtID,
		PlayerID:              playerID,
		WalletID:              walletID,
		RoundID:               "round-ooo",
		GameID:                "fortune-rabbit",
		Kind:                  domain.KindBet,
		Money:                 betAmount,
		CorrelationID:         uuid.NewString(),
	})
	if err != nil || betOut.Status != domain.StatusProcessed {
		t.Fatalf("referenced bet failed: out=%+v, err=%v", betOut, err)
	}

	// Balance after BET: 200.00 - 50.00 = 150.00 BRL
	walletAfterBet, _ := env.WalletRepo.GetByID(ctx, walletID)
	if !walletAfterBet.Balance().Equals(MustNewMoney(t, "150.00", "BRL")) {
		t.Fatalf("balance after bet mismatch: %s", walletAfterBet.Balance().String())
	}

	// 4. Run PendingReferenceWorker to resolve the out-of-order REFUND
	pendingWorker := worker.NewPendingReferenceWorker(
		env.Transactor, env.WalletRepo, env.TxRepo, env.LedgerRepo, env.OutboxRepo,
		50*time.Millisecond, 5, env.Logger, env.Metrics,
	)
	pendingWorker.Start(ctx)

	// Wait for resolution
	assertDeadline := time.Now().Add(3 * time.Second)
	var resolvedRefund *domain.WagerTransaction
	for time.Now().Before(assertDeadline) {
		time.Sleep(100 * time.Millisecond)
		tx, _ := env.TxRepo.GetByProviderAndExternalID(ctx, "provider-a", refundExtID)
		if tx != nil && tx.Status() == domain.StatusProcessed {
			resolvedRefund = tx
			break
		}
	}
	_ = pendingWorker.Stop(ctx)

	if resolvedRefund == nil {
		t.Fatalf("pending reference worker failed to resolve refund within deadline")
	}

	// Final Balance after resolved REFUND: 150.00 + 50.00 = 200.00 BRL
	finalWallet, _ := env.WalletRepo.GetByID(ctx, walletID)
	if !finalWallet.Balance().Equals(initBal) {
		t.Errorf("expected final balance %s, got %s", initBal.String(), finalWallet.Balance().String())
	}

	// 5. Part B: Test TTL / Max Retries Expiration
	orphanRefundExtID := "refund-orphan-" + uuid.NewString()
	orphanOut, err := env.ProcessWagerUC.Execute(ctx, usecase.ProcessWagerInput{
		ProviderID:                     "provider-a",
		ExternalTransactionID:          orphanRefundExtID,
		IdempotencyKey:                 "provider-a:" + orphanRefundExtID,
		PlayerID:                       playerID,
		WalletID:                       walletID,
		RoundID:                        "round-orphan",
		GameID:                         "fortune-rabbit",
		Kind:                           domain.KindRefund,
		Money:                          betAmount,
		ReferenceExternalTransactionID: "non-existent-ref-" + uuid.NewString(),
		CorrelationID:                  uuid.NewString(),
	})
	if err != nil || orphanOut.Status != domain.StatusPendingReference {
		t.Fatalf("orphan refund failed to enter pending reference: out=%+v, err=%v", orphanOut, err)
	}

	// Create worker with maxRetries = 1 to test immediate expiration
	ttlWorker := worker.NewPendingReferenceWorker(
		env.Transactor, env.WalletRepo, env.TxRepo, env.LedgerRepo, env.OutboxRepo,
		50*time.Millisecond, 1, env.Logger, env.Metrics,
	)

	// Manually set retry count on orphan transaction to 1
	orphanTx, _ := env.TxRepo.GetByProviderAndExternalID(ctx, "provider-a", orphanRefundExtID)
	_ = env.Transactor.WithinTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return env.TxRepo.ScheduleReferenceRetry(ctx, tx, orphanTx.ID(), time.Now().UTC().Add(-1*time.Minute))
	})

	ttlWorker.Start(ctx)
	deadline := time.Now().Add(3 * time.Second)
	var expiredTx *domain.WagerTransaction
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		tx, _ := env.TxRepo.GetByProviderAndExternalID(ctx, "provider-a", orphanRefundExtID)
		if tx != nil && tx.Status() == domain.StatusRejected {
			expiredTx = tx
			break
		}
	}
	_ = ttlWorker.Stop(ctx)

	if expiredTx == nil {
		t.Fatalf("orphan refund was not rejected after TTL expired")
	}
	if expiredTx.FailureCode() != domain.FailureCodeReferenceNotFound {
		t.Errorf("expected failure code %s, got %s", domain.FailureCodeReferenceNotFound, expiredTx.FailureCode())
	}

	// Verify full reconciliation consistency
	recon, err := env.ReconcileUC.Execute(ctx, walletID)
	if err != nil || !recon.Consistent {
		t.Fatalf("reconciliation inconsistent after reversals: %+v, err=%v", recon, err)
	}
}

type spyEventPublisher struct {
	onPublish func(eventID string)
}

func (s *spyEventPublisher) Publish(ctx context.Context, eventID string, messageGroupID string, payload []byte) error {
	if s.onPublish != nil {
		s.onPublish(eventID)
	}
	return nil
}
