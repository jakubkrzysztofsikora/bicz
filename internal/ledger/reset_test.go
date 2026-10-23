package ledger

import (
	"testing"
)

func TestResetBudgetUnblocksInsolventIdentityWithoutRunningReward(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)

	// Open identity with initial capital 1000 mc
	if _, err := handle.OpenAccount(ctx, "agent", WithInitialCapital(1_000)); err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	publishPolicy(t, handle)

	// Record a loss
	lossRes, err := handle.RecordLoss(ctx, "loss-1", "agent", 100, "no_fake_pass", "Run tests", "", "", "evidence", "1", WithDisposition(DispositionConfirmedBreach))
	if err != nil {
		t.Fatalf("RecordLoss: %v", err)
	}
	if lossRes.OutstandingLoss != 100 {
		t.Fatalf("outstanding = %d, want 100", lossRes.OutstandingLoss)
	}
	if lossRes.Role != RoleWarned {
		t.Fatalf("role = %s, want warned", lossRes.Role)
	}

	// Drain remaining balance (900 mc) in capped attempts: recorded pain limits
	// what one attempt may reserve, so drain like real metered work.
	if err := drainThroughPainCap(ctx, handle, "agent"); err != nil {
		t.Fatalf("drain: %v", err)
	}

	// Verify identity is now insolvent
	acc, err := handle.GetAccount(ctx, "agent")
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if acc.Balance != 0 || acc.Spendable() != 0 {
		t.Fatalf("balance=%d spendable=%d, want 0/0 (insolvent)", acc.Balance, acc.Spendable())
	}

	// Next reservation is blocked with ErrInsolvent
	_, err = handle.Reserve(ctx, "agent", "res-2", "", 100)
	wantCode(t, err, ErrInsolvent, "reserve when balance is 0")

	// Reset budget to 1,000 mc (without running reward logic!)
	resetResult, err := handle.ResetBudget(ctx, "reset-1", "agent", 1_000, "unblock agent", "cli")
	if err != nil {
		t.Fatalf("ResetBudget: %v", err)
	}

	if resetResult.BalanceBefore != 0 {
		t.Fatalf("BalanceBefore = %d, want 0", resetResult.BalanceBefore)
	}
	if resetResult.BalanceAfter != 1_000 {
		t.Fatalf("BalanceAfter = %d, want 1000", resetResult.BalanceAfter)
	}
	if resetResult.Delta != 1_000 {
		t.Fatalf("Delta = %d, want 1000", resetResult.Delta)
	}

	// CRITICAL INVARIANT: Outstanding loss MUST NOT be reduced or repaid!
	if resetResult.OutstandingLoss != 100 {
		t.Fatalf("OutstandingLoss = %d, want 100 (unchanged!)", resetResult.OutstandingLoss)
	}

	// CRITICAL INVARIANT: Role MUST NOT be reset (stays warned)!
	if resetResult.Role != RoleWarned {
		t.Fatalf("Role = %s, want warned (unchanged!)", resetResult.Role)
	}

	// Verify projection confirms unblocked spendable, but loss still active
	proj, err := handle.Projection(ctx, "agent")
	if err != nil {
		t.Fatalf("Projection: %v", err)
	}
	if proj.Balance != 1_000 || proj.Spendable != 1_000 {
		t.Fatalf("proj balance=%d spendable=%d, want 1000/1000", proj.Balance, proj.Spendable)
	}
	if proj.OutstandingLoss != 100 {
		t.Fatalf("proj outstanding=%d, want 100", proj.OutstandingLoss)
	}
	if proj.Role != RoleWarned {
		t.Fatalf("proj role=%s, want warned", proj.Role)
	}
	if len(proj.Lessons) != 1 || proj.Lessons[0].Rule != "no_fake_pass" {
		t.Fatalf("lessons must be preserved: %+v", proj.Lessons)
	}

	// Now agent is UNBLOCKED: reservation succeeds!
	auth2, err := handle.Reserve(ctx, "agent", "res-unblocked", "", 100)
	if err != nil {
		t.Fatalf("Reserve after reset failed: %v", err)
	}
	if auth2.Reservation.Amount != 100 {
		t.Fatalf("Reservation amount = %d, want 100", auth2.Reservation.Amount)
	}

	// Test idempotency: replaying reset-1 returns same receipt with Replayed=true
	replayed, err := handle.ResetBudget(ctx, "reset-1", "agent", 1_000, "unblock agent", "cli")
	if err != nil {
		t.Fatalf("ResetBudget replay: %v", err)
	}
	if !replayed.Replayed {
		t.Fatal("expected Replayed=true")
	}
	if replayed.Delta != 1_000 {
		t.Fatalf("replayed Delta = %d, want 1000", replayed.Delta)
	}
}

func TestResetBudgetWhenAlreadyFunded(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)

	if _, err := handle.OpenAccount(ctx, "agent", WithInitialCapital(1_500)); err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}

	// Reset to 1,000 when balance is 1,500
	resetResult, err := handle.ResetBudget(ctx, "reset-funded", "agent", 1_000, "check", "")
	if err != nil {
		t.Fatalf("ResetBudget: %v", err)
	}
	if resetResult.Delta != 0 {
		t.Fatalf("Delta = %d, want 0 (already funded)", resetResult.Delta)
	}
	if resetResult.BalanceAfter != 1_500 {
		t.Fatalf("BalanceAfter = %d, want 1500", resetResult.BalanceAfter)
	}
}

func TestResetBudgetReplayConflict(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)

	if _, err := handle.OpenAccount(ctx, "agent", WithInitialCapital(500)); err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}

	if _, err := handle.ResetBudget(ctx, "reset-conflict-key", "agent", 1_000, "unblock", ""); err != nil {
		t.Fatalf("ResetBudget: %v", err)
	}

	// Replay with identical payload is OK
	replayed, err := handle.ResetBudget(ctx, "reset-conflict-key", "agent", 1_000, "unblock", "")
	if err != nil || !replayed.Replayed {
		t.Fatalf("identical replay failed: err=%v replayed=%v", err, replayed.Replayed)
	}

	// Replay with different target balance MUST return ErrPayloadConflict
	_, err = handle.ResetBudget(ctx, "reset-conflict-key", "agent", 2_000, "unblock", "")
	if err == nil {
		t.Fatal("expected ErrPayloadConflict on different targetBalance, got nil")
	}
}
