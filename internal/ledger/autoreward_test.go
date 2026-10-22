package ledger

import (
	"testing"
)

func TestAutorewardCeilingPreventsAutofarming(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)

	// Baseline ceiling is 100_000_000 mc (100 credits)
	if _, err := handle.OpenAccount(ctx, "agent", WithInitialCapital(100_000_000)); err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}

	// 1. When balance is already at baseline ceiling (100_000_000 mc):
	// Autoreward MUST grant 0 credits! Cannot farm surplus!
	res, err := handle.RecordAutoreward(ctx, "auto-1", "agent", 50_000, "test_pass", "passing test", "sha256:test1")
	if err != nil {
		t.Fatalf("RecordAutoreward: %v", err)
	}
	if res.AmountGranted != 0 {
		t.Fatalf("AmountGranted = %d, want 0 (cannot exceed baseline ceiling!)", res.AmountGranted)
	}
	if res.BalanceAfter != 100_000_000 {
		t.Fatalf("BalanceAfter = %d, want 100_000_000", res.BalanceAfter)
	}

	// 2. Spend 200_000 mc on tool execution
	r1, err := handle.Reserve(ctx, "agent", "res-tool", "", 200_000)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if _, err := handle.Settle(ctx, r1.Reservation.ID, 200_000, ClassCompleted, "sha256:evidence"); err != nil {
		t.Fatalf("Settle: %v", err)
	}

	acc, _ := handle.GetAccount(ctx, "agent")
	if acc.Balance != 99_800_000 {
		t.Fatalf("balance = %d, want 99_800_000", acc.Balance)
	}

	// 3. Autoreward replenishes some of the spent credits (50_000 mc for passing test)
	res2, err := handle.RecordAutoreward(ctx, "auto-2", "agent", 50_000, "test_pass", "passing test rebate", "sha256:test2")
	if err != nil {
		t.Fatalf("RecordAutoreward: %v", err)
	}
	if res2.AmountGranted != 50_000 {
		t.Fatalf("AmountGranted = %d, want 50_000", res2.AmountGranted)
	}
	if res2.BalanceAfter != 99_850_000 {
		t.Fatalf("BalanceAfter = %d, want 99_850_000", res2.BalanceAfter)
	}

	// 4. Autoreward near ceiling clamps to headroom:
	// Suppose headroom is 150_000 mc and agent requests 500_000 mc
	res3, err := handle.RecordAutoreward(ctx, "auto-3", "agent", 500_000, "todo_complete", "todo item completed", "sha256:todo1")
	if err != nil {
		t.Fatalf("RecordAutoreward: %v", err)
	}
	if res3.AmountGranted != 150_000 {
		t.Fatalf("AmountGranted = %d, want 150_000 (clamped to headroom)", res3.AmountGranted)
	}
	if res3.BalanceAfter != 100_000_000 {
		t.Fatalf("BalanceAfter = %d, want 100_000_000 (hit ceiling exactly)", res3.BalanceAfter)
	}

	// 5. Deduplication / Idempotency: replaying auto-2 grants nothing extra
	replayed, err := handle.RecordAutoreward(ctx, "auto-2", "agent", 50_000, "test_pass", "passing test rebate", "sha256:test2")
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !replayed.Replayed {
		t.Fatal("expected Replayed=true")
	}
	if replayed.AmountGranted != 50_000 {
		t.Fatalf("replayed AmountGranted = %d, want 50_000", replayed.AmountGranted)
	}
}

func TestAutorewardNeverReducesOutstandingLoss(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)

	if _, err := handle.OpenAccount(ctx, "agent", WithInitialCapital(100_000_000)); err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	publishPolicy(t, handle)

	// Burn with policy
	lossRes, err := handle.RecordLoss(ctx, "loss-1", "agent", 100, "follow_explicit_instruction", "Follow rules", "", "", "evidence", "1", WithDisposition(DispositionConfirmedBreach))
	if err != nil {
		t.Fatalf("RecordLoss: %v", err)
	}
	if lossRes.OutstandingLoss != 100 {
		t.Fatalf("outstanding = %d, want 100", lossRes.OutstandingLoss)
	}

	// Spend 1_000_000 mc
	r1, err := handle.Reserve(ctx, "agent", "res-1", "", 1_000_000)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if _, err := handle.Settle(ctx, r1.Reservation.ID, 1_000_000, ClassCompleted, "sha256:evidence"); err != nil {
		t.Fatalf("Settle: %v", err)
	}

	// Autoreward of 100_000 mc
	res, err := handle.RecordAutoreward(ctx, "auto-loss-check", "agent", 100_000, "test_pass", "test rebate", "sha256:test")
	if err != nil {
		t.Fatalf("RecordAutoreward: %v", err)
	}
	if res.AmountGranted != 100_000 {
		t.Fatalf("AmountGranted = %d, want 100_000", res.AmountGranted)
	}

	// CRITICAL INVARIANT: Outstanding loss is STILL 100 mc!
	if res.OutstandingLoss != 100 {
		t.Fatalf("OutstandingLoss = %d, want 100 (must NEVER decrease from autoreward!)", res.OutstandingLoss)
	}

	proj, err := handle.Projection(ctx, "agent")
	if err != nil {
		t.Fatalf("Projection: %v", err)
	}
	if proj.OutstandingLoss != 100 {
		t.Fatalf("Projection OutstandingLoss = %d, want 100", proj.OutstandingLoss)
	}
	if proj.Role != RoleWarned {
		t.Fatalf("Role = %s, want warned", proj.Role)
	}
}

func TestReviewShipVerdictGrantsAutoreward(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)

	if _, err := handle.OpenAccount(ctx, "agent", WithInitialCapital(100_000_000)); err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}

	// Spend 2_000_000 mc
	r1, err := handle.Reserve(ctx, "agent", "res-review", "", 2_000_000)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if _, err := handle.Settle(ctx, r1.Reservation.ID, 2_000_000, ClassCompleted, "sha256:evidence"); err != nil {
		t.Fatalf("Settle: %v", err)
	}

	// Review with verdict "SHIP"
	reviewRes, err := handle.RecordReview(ctx, "rev-1", "agent", "SHIP", "adversarial review passed", "test digest")
	if err != nil {
		t.Fatalf("RecordReview: %v", err)
	}

	// Check that SHIP verdict granted autoreward (1_000_000 mc = 1 credit)
	if reviewRes.AutorewardGranted != 1_000_000 {
		t.Fatalf("AutorewardGranted = %d, want 1_000_000", reviewRes.AutorewardGranted)
	}

	acc, err := handle.GetAccount(ctx, "agent")
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	// Balance went from 98_000_000 to 99_000_000
	if acc.Balance != 99_000_000 {
		t.Fatalf("Balance = %d, want 99_000_000", acc.Balance)
	}

	// Review with a non-SHIP verdict (e.g. "NEEDS_WORK") grants NO autoreward
	reviewRes2, err := handle.RecordReview(ctx, "rev-2", "agent", "NEEDS_WORK", "more tests needed", "test digest")
	if err != nil {
		t.Fatalf("RecordReview non-ship: %v", err)
	}
	if reviewRes2.AutorewardGranted != 0 {
		t.Fatalf("AutorewardGranted = %d, want 0 for non-SHIP verdict", reviewRes2.AutorewardGranted)
	}

	// Review with SHIP on a dismissed account grants NO autoreward
	if _, err := handle.SetRole(ctx, "agent", RoleDismissed, "dismissal", "review-dismiss", "role-dismiss"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	reviewDismissed, err := handle.RecordReview(ctx, "rev-dismissed", "agent", "SHIP", "great but dismissed", "test digest")
	if err != nil {
		t.Fatalf("RecordReview dismissed: %v", err)
	}
	if reviewDismissed.AutorewardGranted != 0 {
		t.Fatalf("AutorewardGranted = %d, want 0 for dismissed account", reviewDismissed.AutorewardGranted)
	}
}

func TestAutorewardReplayDifferentPayloadReturnsConflict(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)

	if _, err := handle.OpenAccount(ctx, "agent", WithInitialCapital(50_000_000)); err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}

	if _, err := handle.RecordAutoreward(ctx, "auto-replay-key", "agent", 10_000, "test_pass", "passing test", "digest1"); err != nil {
		t.Fatalf("RecordAutoreward: %v", err)
	}

	// Replay with identical payload is OK
	replayed, err := handle.RecordAutoreward(ctx, "auto-replay-key", "agent", 10_000, "test_pass", "passing test", "digest1")
	if err != nil || !replayed.Replayed {
		t.Fatalf("identical replay failed: err=%v replayed=%v", err, replayed.Replayed)
	}

	// Replay with different amount MUST return ErrPayloadConflict
	_, err = handle.RecordAutoreward(ctx, "auto-replay-key", "agent", 20_000, "test_pass", "passing test", "digest1")
	if err == nil {
		t.Fatal("expected ErrPayloadConflict on different amount replay, got nil")
	}

	// Replay with different trigger MUST return ErrPayloadConflict
	_, err = handle.RecordAutoreward(ctx, "auto-replay-key", "agent", 10_000, "todo_complete", "passing test", "digest1")
	if err == nil {
		t.Fatal("expected ErrPayloadConflict on different trigger replay, got nil")
	}
}

// A retried PostToolUse carries a fresh idempotency key but quotes the same
// observed action digest, so the event key alone does not stop a second
// credit (docs/CONTRACTS.md §6.5).
func TestAutorewardDeduplicatesTheActionDigestAcrossEventKeys(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	if _, err := handle.OpenAccount(ctx, "agent", WithInitialCapital(1_000_000)); err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}

	const digest = "sha256:identical-action"
	first, err := handle.RecordAutoreward(ctx, "auto-key-a", "agent", 100_000, "test_pass", "passing test", digest)
	if err != nil {
		t.Fatalf("first autoreward: %v", err)
	}
	if first.AmountGranted != 100_000 || first.Replayed {
		t.Fatalf("first = %+v, want a fresh 100000 grant", first)
	}

	second, err := handle.RecordAutoreward(ctx, "auto-key-b", "agent", 100_000, "test_pass", "passing test", digest)
	if err != nil {
		t.Fatalf("second autoreward: %v", err)
	}
	if !second.Replayed || second.EventID != first.EventID {
		t.Fatalf("second = %+v, want the prior receipt under event %s", second, first.EventID)
	}
	balance, _ := mustBalance(t, handle, "agent")
	if balance != 1_100_000 {
		t.Fatalf("balance = %d, want 1100000: one observed action earns one credit", balance)
	}

	// The refused key is bound to the action it quoted, so it cannot be spent
	// on a different one later (docs/CONTRACTS.md §2 idempotency rule).
	_, err = handle.RecordAutoreward(ctx, "auto-key-b", "agent", 100_000, "test_pass", "passing test", "sha256:later-action")
	wantCode(t, err, ErrPayloadConflict, "reusing a deduped key with a different action digest")
	balance, _ = mustBalance(t, handle, "agent")
	if balance != 1_100_000 {
		t.Fatalf("balance = %d, want the conflicting replay to write nothing", balance)
	}

	// One key returns one prior result: retrying B quotes the same action and
	// must yield the same receipt the first call to B returned.
	retry, err := handle.RecordAutoreward(ctx, "auto-key-b", "agent", 100_000, "test_pass", "passing test", digest)
	if err != nil {
		t.Fatalf("retry of the deduped key: %v", err)
	}
	if second != retry {
		t.Fatalf("retry = %+v, want the first call's receipt %+v", retry, second)
	}

	// The dedup is scoped to the observed action: a different action is
	// creditable, and so is the same digest quoted by another identity.
	other, err := handle.RecordAutoreward(ctx, "auto-key-c", "agent", 100_000, "test_pass", "passing test", "sha256:other-action")
	if err != nil {
		t.Fatalf("distinct action: %v", err)
	}
	if other.AmountGranted != 100_000 || other.Replayed {
		t.Fatalf("distinct action = %+v, want a fresh 100000 grant", other)
	}
	if _, err := handle.OpenAccount(ctx, "other", WithInitialCapital(1_000_000)); err != nil {
		t.Fatalf("OpenAccount(other): %v", err)
	}
	otherIdentity, err := handle.RecordAutoreward(ctx, "auto-key-d", "other", 100_000, "test_pass", "passing test", digest)
	if err != nil {
		t.Fatalf("other identity: %v", err)
	}
	if otherIdentity.AmountGranted != 100_000 || otherIdentity.Replayed {
		t.Fatalf("other identity = %+v, want a fresh 100000 grant", otherIdentity)
	}

	// Evidence nobody observed identifies no action, so it dedups nothing and
	// only the event key governs those calls.
	for _, id := range []string{"auto-no-evidence-a", "auto-no-evidence-b"} {
		result, err := handle.RecordAutoreward(ctx, id, "agent", 100_000, "test_pass", "passing test", "")
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if result.Replayed || result.AmountGranted != 100_000 {
			t.Fatalf("%s = %+v, want a fresh grant", id, result)
		}
	}
}

// Asking for a different amount for an action that was already credited is a
// different claim about that action, not a retry of it, so it is refused
// (docs/CONTRACTS.md §2: same key + same payload; conflicting replay is a hard error).
func TestAutorewardRefusesADifferentAmountForAnAlreadyCreditedAction(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	if _, err := handle.OpenAccount(ctx, "agent", WithInitialCapital(1_000_000)); err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}

	const digest = "sha256:one-action"
	first, err := handle.RecordAutoreward(ctx, "auto-key-a", "agent", 100_000, "test_pass", "passing test", digest)
	if err != nil {
		t.Fatalf("first autoreward: %v", err)
	}
	if first.AmountGranted != 100_000 {
		t.Fatalf("first = %+v, want a fresh 100000 grant", first)
	}

	_, err = handle.RecordAutoreward(ctx, "auto-key-b", "agent", 200_000, "test_pass", "passing test", digest)
	wantCode(t, err, ErrPayloadConflict, "a second claim on the same action for a different amount")
	balance, _ := mustBalance(t, handle, "agent")
	if balance != 1_100_000 {
		t.Fatalf("balance = %d, want the refused claim to write nothing", balance)
	}
}

// The adjudication path grants the same microreward the meter path grants, so
// it inherits the same dedup: one adjudication of one evidence earns one credit.
func TestShipReviewGrantsOncePerAdjudicatedEvidence(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	if _, err := handle.OpenAccount(ctx, "agent", WithInitialCapital(1_000_000)); err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	publishPolicy(t, handle)

	const evidence = "sha256:one-adjudication"
	first, err := handle.RecordReview(ctx, "review-a", "agent", "SHIP", "adversarial review passed", evidence)
	if err != nil {
		t.Fatalf("first review: %v", err)
	}
	if first.AutorewardGranted != 1_000_000 {
		t.Fatalf("first review granted %d, want 1000000", first.AutorewardGranted)
	}

	second, err := handle.RecordReview(ctx, "review-b", "agent", "SHIP", "adversarial review passed", evidence)
	if err != nil {
		t.Fatalf("second review: %v", err)
	}
	if second.AutorewardGranted != 0 {
		t.Fatalf("second review granted %d, want 0 for an already-adjudicated action", second.AutorewardGranted)
	}
	balance, _ := mustBalance(t, handle, "agent")
	if balance != 2_000_000 {
		t.Fatalf("balance = %d, want 2000000: one adjudication earned credit twice", balance)
	}

	// Replaying that review returns the verdict's own receipt, grant included.
	retry, err := handle.RecordReview(ctx, "review-b", "agent", "SHIP", "adversarial review passed", evidence)
	if err != nil {
		t.Fatalf("replay of the deduplicated review: %v", err)
	}
	if retry.AutorewardGranted != second.AutorewardGranted || !retry.Replayed {
		t.Fatalf("replay = %+v, want the first call's grant of %d with Replayed",
			retry, second.AutorewardGranted)
	}

	// A different adjudication is still creditable.
	other, err := handle.RecordReview(ctx, "review-c", "agent", "SHIP", "second pass", "sha256:another-adjudication")
	if err != nil {
		t.Fatalf("distinct adjudication: %v", err)
	}
	if other.AutorewardGranted != 1_000_000 {
		t.Fatalf("distinct adjudication granted %d, want 1000000", other.AutorewardGranted)
	}
}

