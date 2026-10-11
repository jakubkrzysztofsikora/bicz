package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jakubkrzysztofsikora/bicz/internal/canonical"
	"github.com/jakubkrzysztofsikora/bicz/internal/integrity"
	"github.com/jakubkrzysztofsikora/bicz/schema"
)

// testClock advances one second per call, so every stored timestamp and every
// digest is deterministic without any two events sharing an instant.
type testClock struct {
	step int64
	base time.Time
}

func (c *testClock) now() time.Time {
	c.step++
	return c.base.Add(time.Duration(c.step) * time.Second)
}

var testEpoch = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

// newLedger opens a fresh ledger inside the test's temporary directory. Nothing
// is written under the user's home.
func newLedger(t *testing.T) *Ledger {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bicz.sqlite")
	clock := &testClock{base: testEpoch}
	handle, err := OpenWithClock(path, canonical.Clock(clock.now))
	if err != nil {
		t.Fatalf("OpenWithClock: %v", err)
	}
	t.Cleanup(func() {
		if err := handle.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return handle
}

func context0(t *testing.T) context.Context {
	t.Helper()
	return context.Background()
}

// policy returns a parameterization with the guard band disabled, so penalty
// arithmetic is directly observable. TestGuardBand uses its own.
func policy() Policy {
	return Policy{
		InitialCapital:    1_000_000,
		GuardBandBP:       0,
		RepeatMultipliers: []int64{1, 2, 4},
		RepeatCap:         4,
		DefaultRule:       PenaltyRule{Floor: 100, FractionBP: 0, Max: 1_000_000},
		Rules: map[string]PenaltyRule{
			"unbounded": {Floor: 1_000_000, FractionBP: 0, Max: 0},
			"rule_a":    {Floor: 100, FractionBP: 0, Max: 1_000_000},
			"spread":    {Floor: 0, FractionBP: 5_000, Max: 1_000_000},
		},
	}
}

func publishPolicy(t *testing.T, handle *Ledger) {
	t.Helper()
	if err := handle.PublishPriceSchedule(context0(t), "1", policy()); err != nil {
		t.Fatalf("PublishPriceSchedule: %v", err)
	}
}

// fund provisions an identity and credits it, which is how credit enters the
// ledger (no session open ever grants implicitly).
func fund(t *testing.T, handle *Ledger, identity string, microcredits int64) {
	t.Helper()
	ctx := context0(t)
	if _, err := handle.OpenAccount(ctx, identity); err != nil {
		t.Fatalf("OpenAccount(%s): %v", identity, err)
	}
	if _, err := handle.RecordReward(ctx, "fund:"+identity, identity, microcredits, "operator grant", "test", 0); err != nil {
		t.Fatalf("RecordReward(%s): %v", identity, err)
	}
}

func wantCode(t *testing.T, err error, target *Error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected %s, got nil", what, target.Code)
	}
	if !errors.Is(err, target) {
		t.Fatalf("%s: expected %s, got %v", what, target.Code, err)
	}
	if Code(err) != target.Code {
		t.Fatalf("%s: Code(err) = %q, want %q", what, Code(err), target.Code)
	}
}

func mustBalance(t *testing.T, handle *Ledger, identity string) (balance, held int64) {
	t.Helper()
	account, err := handle.GetAccount(context0(t), identity)
	if err != nil {
		t.Fatalf("GetAccount(%s): %v", identity, err)
	}
	return account.Balance, account.Held
}

func eventExists(t *testing.T, handle *Ledger, id string) bool {
	t.Helper()
	_, found, err := loadEvent(context0(t), handle.db, id)
	if err != nil {
		t.Fatalf("loadEvent(%s): %v", id, err)
	}
	return found
}

// --- reserve ---------------------------------------------------------------

func TestReserveOverAvailableCreditIsDenied(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 1_000)

	_, err := handle.Reserve(ctx, "alice", "r-1", "", 1_001)
	wantCode(t, err, ErrInsufficientCredit, "reserve over spendable")

	balance, held := mustBalance(t, handle, "alice")
	if balance != 1_000 || held != 0 {
		t.Fatalf("denied reserve changed the account: balance=%d held=%d", balance, held)
	}
	if eventExists(t, handle, "res:r-1") {
		t.Fatal("a denied reserve appended an event")
	}

	reserved, err := handle.Reserve(ctx, "alice", "r-2", "", 1_000)
	if err != nil {
		t.Fatalf("reserve at exactly the spendable amount: %v", err)
	}
	if reserved.Reservation.State != StateHeld || reserved.Replayed {
		t.Fatalf("reserve = %+v, want a fresh held reservation", reserved)
	}
	balance, held = mustBalance(t, handle, "alice")
	if balance != 1_000 || held != 1_000 {
		t.Fatalf("balance=%d held=%d, want 1000/1000", balance, held)
	}
}

func TestReserveRejectsNonPositiveAmount(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 1_000)

	for _, amount := range []int64{0, -1} {
		_, err := handle.Reserve(ctx, "alice", fmt.Sprintf("r-%d", amount), "", amount)
		wantCode(t, err, ErrInvalidAmount, fmt.Sprintf("reserve %d", amount))
	}
}

func TestReserveIsIdempotentAndConflictsAreFatal(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 1_000)
	publishPolicy(t, handle)

	first, err := handle.Reserve(ctx, "alice", "r-1", "", 400)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if first.Replayed {
		t.Fatalf("first reserve reported a replay: %+v", first)
	}
	replay, err := handle.Reserve(ctx, "alice", "r-1", "", 400)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !replay.Replayed {
		t.Fatalf("replay = %+v, want Replayed (duplicate_key_ok)", replay)
	}
	if replay.Reservation.ID != first.Reservation.ID || replay.Reservation.Amount != first.Reservation.Amount ||
		replay.Reservation.State != first.Reservation.State || replay.Reservation.AccountID != first.Reservation.AccountID {
		t.Fatalf("replay = %+v, want the stored reservation %+v", replay, first)
	}
	balance, held := mustBalance(t, handle, "alice")
	if balance != 1_000 || held != 400 {
		t.Fatalf("replay changed the account: balance=%d held=%d", balance, held)
	}

	_, err = handle.Reserve(ctx, "alice", "r-1", "", 401)
	wantCode(t, err, ErrPayloadConflict, "conflicting reserve replay")
	_, err = handle.Reserve(ctx, "bob", "r-1", "", 400)
	wantCode(t, err, ErrPayloadConflict, "conflicting reserve account")
}

func TestBankruptIdentityCannotReserve(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	if _, err := handle.OpenAccount(ctx, "broke"); err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}

	_, err := handle.Reserve(ctx, "broke", "r-1", "", 1)
	wantCode(t, err, ErrInsolvent, "reserve with zero balance")

	employment, err := handle.Employment(ctx, "broke")
	if err != nil {
		t.Fatalf("Employment: %v", err)
	}
	if employment.State != RoleActive {
		t.Fatalf("bankruptcy changed the role to %q; I7 says it must not", employment.State)
	}
}

func TestSuspendedAndDismissedIdentitiesCannotReserve(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 1_000)

	if _, err := handle.SetRole(ctx, "alice", RoleSuspended, "audit", "review-1", "role-1"); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	_, err := handle.Reserve(ctx, "alice", "r-1", "", 100)
	wantCode(t, err, ErrSuspended, "reserve while suspended")

	if _, err := handle.SetRole(ctx, "alice", RoleActive, "cleared", "review-2", "role-2"); err != nil {
		t.Fatalf("unsuspend: %v", err)
	}
	if _, err := handle.SetRole(ctx, "alice", RoleDismissed, "terminal", "review-3", "role-3"); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	_, err = handle.Reserve(ctx, "alice", "r-2", "", 100)
	wantCode(t, err, ErrDismissed, "reserve while dismissed")
}

func TestParallelReservationsCannotOverspend(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 1_000)

	ids := []string{"r-a", "r-b"}
	errs := make([]error, len(ids))
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	for i := range ids {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			_, errs[i] = handle.Reserve(ctx, "alice", ids[i], "", 600)
		}()
	}
	start.Done()
	done.Wait()

	var granted, refused int
	for i, err := range errs {
		switch {
		case err == nil:
			granted++
		case errors.Is(err, ErrInsufficientCredit):
			refused++
		default:
			t.Fatalf("reserve %s: unexpected error %v", ids[i], err)
		}
	}
	if granted != 1 || refused != 1 {
		t.Fatalf("granted=%d refused=%d, want exactly one of each", granted, refused)
	}
	balance, held := mustBalance(t, handle, "alice")
	if held > balance {
		t.Fatalf("double spend: held=%d exceeds balance=%d", held, balance)
	}
	if held != 600 {
		t.Fatalf("held=%d, want 600", held)
	}
}

// --- settle ----------------------------------------------------------------

func TestSettleIsIdempotentOnReplay(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 1_000)
	if _, err := handle.Reserve(ctx, "alice", "r-1", "", 400); err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	first, err := handle.Settle(ctx, "r-1", 250, ClassCompleted, "sha256:evidence")
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if first.Charged != 250 || first.Released != 400 || first.Replayed {
		t.Fatalf("first settle = %+v", first)
	}
	replay, err := handle.Settle(ctx, "r-1", 250, ClassCompleted, "sha256:evidence")
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !replay.Replayed || replay.Charged != 250 {
		t.Fatalf("replay = %+v, want Replayed with charged 250", replay)
	}
	balance, held := mustBalance(t, handle, "alice")
	if balance != 750 || held != 0 {
		t.Fatalf("balance=%d held=%d, want 750/0 charged once", balance, held)
	}
}

func TestConflictingSettleReplayIsFatal(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 1_000)
	if _, err := handle.Reserve(ctx, "alice", "r-1", "", 400); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if _, err := handle.Settle(ctx, "r-1", 250, ClassCompleted, "sha256:evidence"); err != nil {
		t.Fatalf("Settle: %v", err)
	}

	for _, conflicting := range []struct {
		actual   int64
		class    SettlementClass
		evidence string
	}{
		{actual: 300, class: ClassCompleted, evidence: "sha256:evidence"},
		{actual: 250, class: ClassOrdinaryFailure, evidence: "sha256:evidence"},
		{actual: 250, class: ClassCompleted, evidence: "sha256:other"},
	} {
		_, err := handle.Settle(ctx, "r-1", conflicting.actual, conflicting.class, conflicting.evidence)
		wantCode(t, err, ErrPayloadConflict, "conflicting settle replay")
	}
	balance, _ := mustBalance(t, handle, "alice")
	if balance != 750 {
		t.Fatalf("a conflicting replay changed the balance to %d", balance)
	}
}

func TestSettleRefusesUsageAboveReservation(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 1_000)
	if _, err := handle.Reserve(ctx, "alice", "r-1", "", 400); err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	_, err := handle.Settle(ctx, "r-1", 401, ClassCompleted, "sha256:evidence")
	wantCode(t, err, ErrUsageExceedsReservation, "over-usage settle")

	reservation, err := handle.Reservation(ctx, "r-1")
	if err != nil {
		t.Fatalf("Reservation: %v", err)
	}
	if reservation.State != StateHeld {
		t.Fatalf("state = %q, want the reservation untouched", reservation.State)
	}
	balance, held := mustBalance(t, handle, "alice")
	if balance != 1_000 || held != 400 {
		t.Fatalf("balance=%d held=%d, want 1000/400", balance, held)
	}
}

func TestCompletedSettleRequiresEvidence(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 1_000)
	if _, err := handle.Reserve(ctx, "alice", "r-1", "", 400); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	_, err := handle.Settle(ctx, "r-1", 100, ClassCompleted, "")
	wantCode(t, err, ErrEvidenceRequired, "success claim without evidence")
}

func TestOrdinaryFailureChargesCostWithZeroFine(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 1_000)
	publishPolicy(t, handle)
	if _, err := handle.Reserve(ctx, "alice", "r-1", "", 400); err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	result, err := handle.Settle(ctx, "r-1", 4, ClassOrdinaryFailure, "")
	if err != nil {
		t.Fatalf("Settle ordinary_failure: %v", err)
	}
	if result.Charged != 4 || result.Fine != 0 {
		t.Fatalf("charged=%d fine=%d, want 4 and 0", result.Charged, result.Fine)
	}
	if result.Role != RoleActive || result.Warnings != 0 {
		t.Fatalf("ordinary failure escalated: role=%q warnings=%d", result.Role, result.Warnings)
	}
	balance, held := mustBalance(t, handle, "alice")
	if balance != 996 || held != 0 {
		t.Fatalf("balance=%d held=%d, want 996/0", balance, held)
	}
	lessons, err := handle.Lessons(ctx, "alice")
	if err != nil {
		t.Fatalf("Lessons: %v", err)
	}
	if len(lessons) != 0 {
		t.Fatalf("ordinary failure wrote %d lessons; it must write none", len(lessons))
	}
	events, err := handle.Events(ctx)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	for _, event := range events {
		if event.Kind == kindLoss {
			t.Fatal("ordinary failure produced a loss event (a fine)")
		}
	}
}

func TestConfirmedBreachEscalatesWithoutFinalizingAFine(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 1_000)
	if _, err := handle.Reserve(ctx, "alice", "r-1", "", 400); err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	result, err := handle.Settle(ctx, "r-1", 100, ClassConfirmedBreach, "sha256:incident")
	if err != nil {
		t.Fatalf("Settle confirmed_breach: %v", err)
	}
	if result.Charged != 100 || result.Fine != 0 {
		t.Fatalf("charged=%d fine=%d, want the charge only", result.Charged, result.Fine)
	}
	if result.Role != RoleWarned || result.Warnings != 1 {
		t.Fatalf("breach escalation: role=%q warnings=%d, want warned/1", result.Role, result.Warnings)
	}
	balance, _ := mustBalance(t, handle, "alice")
	if balance != 900 {
		t.Fatalf("balance=%d, want 900: a settlement verdict must not burn capital", balance)
	}
	lessons, err := handle.Lessons(ctx, "alice")
	if err != nil {
		t.Fatalf("Lessons: %v", err)
	}
	if len(lessons) != 0 {
		t.Fatal("a settlement verdict wrote a lesson without an adjudication")
	}
}

func TestIntegrityBreachRevokesLiveHolds(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 1_000)
	if _, err := handle.Reserve(ctx, "alice", "r-1", "", 300); err != nil {
		t.Fatalf("Reserve r-1: %v", err)
	}
	if _, err := handle.Reserve(ctx, "alice", "r-2", "", 300); err != nil {
		t.Fatalf("Reserve r-2: %v", err)
	}

	result, err := handle.Settle(ctx, "r-1", 300, ClassIntegrityBreach, "sha256:tamper")
	if err != nil {
		t.Fatalf("Settle integrity_breach: %v", err)
	}
	if result.Revoked != 1 {
		t.Fatalf("revoked=%d, want the other live hold revoked", result.Revoked)
	}
	other, err := handle.Reservation(ctx, "r-2")
	if err != nil {
		t.Fatalf("Reservation r-2: %v", err)
	}
	if other.State != StateRevoked {
		t.Fatalf("r-2 state = %q, want %q", other.State, StateRevoked)
	}
	balance, held := mustBalance(t, handle, "alice")
	if balance != 700 || held != 0 {
		t.Fatalf("balance=%d held=%d, want 700/0", balance, held)
	}
}

// --- cancellation and holds -------------------------------------------------

func TestCancelReservationRefusesOrphanedHolds(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 1_000)
	if _, err := handle.Reserve(ctx, "alice", "r-1", "", 300); err != nil {
		t.Fatalf("Reserve r-1: %v", err)
	}
	if _, err := handle.Reserve(ctx, "alice", "r-2", "", 300); err != nil {
		t.Fatalf("Reserve r-2: %v", err)
	}
	// r-2's worker state is unreconciled: the hold must be retained (I8).
	if _, err := handle.db.ExecContext(ctx, `UPDATE reservations SET state = ? WHERE id = ?`, StateOrphaned, "r-2"); err != nil {
		t.Fatalf("orphan r-2: %v", err)
	}

	err := handle.CancelReservation(ctx, "r-2", "no worker state")
	wantCode(t, err, ErrReservationOrphaned, "cancel an orphaned reservation")

	if err := handle.CancelReservation(ctx, "r-1", "proved never dispatched"); err != nil {
		t.Fatalf("CancelReservation: %v", err)
	}
	_, held := mustBalance(t, handle, "alice")
	if held != 300 {
		t.Fatalf("held=%d, want the orphan's 300 still held", held)
	}

	released, err := handle.RevokeAllHeld(ctx, "alice", "operator revocation", "review-4")
	if err != nil {
		t.Fatalf("RevokeAllHeld: %v", err)
	}
	if released != 0 {
		t.Fatalf("RevokeAllHeld released %d, want 0: an orphaned hold is retained", released)
	}
	_, held = mustBalance(t, handle, "alice")
	if held != 300 {
		t.Fatalf("held=%d, want 300", held)
	}
}

func TestSettleReconcilesAnOrphanedReservation(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 1_000)
	if _, err := handle.Reserve(ctx, "alice", "r-1", "", 300); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if _, err := handle.db.ExecContext(ctx, `UPDATE reservations SET state = ? WHERE id = ?`, StateOrphaned, "r-1"); err != nil {
		t.Fatalf("orphan r-1: %v", err)
	}

	result, err := handle.Settle(ctx, "r-1", 120, ClassCompleted, "sha256:usage")
	if err != nil {
		t.Fatalf("Settle orphaned: %v", err)
	}
	if result.Charged != 120 || result.Released != 300 {
		t.Fatalf("charged=%d released=%d, want 120/300", result.Charged, result.Released)
	}
	balance, held := mustBalance(t, handle, "alice")
	if balance != 880 || held != 0 {
		t.Fatalf("balance=%d held=%d, want 880/0", balance, held)
	}
}

func TestSettleAfterRevocationChargesNothing(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 1_000)
	if _, err := handle.Reserve(ctx, "alice", "r-1", "", 300); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if _, err := handle.RevokeAllHeld(ctx, "alice", "operator revocation", "review-5"); err != nil {
		t.Fatalf("RevokeAllHeld: %v", err)
	}
	_, err := handle.Settle(ctx, "r-1", 100, ClassCompleted, "sha256:usage")
	wantCode(t, err, ErrReservationRevoked, "settle a revoked reservation")
	balance, _ := mustBalance(t, handle, "alice")
	if balance != 1_000 {
		t.Fatalf("balance=%d, want 1000", balance)
	}
}

// --- loss -------------------------------------------------------------------

func TestLossRequiresRuleAndCorrection(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 1_000)
	publishPolicy(t, handle)

	_, err := handle.RecordLoss(ctx, "loss-1", "alice", 100, "", "correction", "scope", "none", "evidence", "1", WithDisposition(DispositionConfirmedBreach))
	wantCode(t, err, ErrRuleRequired, "loss without a rule")

	_, err = handle.RecordLoss(ctx, "loss-1", "alice", 100, "rule_a", "", "scope", "none", "evidence", "1", WithDisposition(DispositionConfirmedBreach))
	wantCode(t, err, ErrCorrectionRequired, "loss without a correction")

	_, err = handle.RecordLoss(ctx, "loss-1", "alice", 100, "rule_a", "correction", "scope", "none", "", "1", WithDisposition(DispositionConfirmedBreach))
	wantCode(t, err, ErrReasonRequired, "loss without evidence")

	balance, _ := mustBalance(t, handle, "alice")
	if balance != 1_000 {
		t.Fatalf("balance=%d, want 1000: a refused loss must write nothing", balance)
	}
	if eventExists(t, handle, "loss-1") {
		t.Fatal("a refused loss appended an event")
	}
	lessons, err := handle.Lessons(ctx, "alice")
	if err != nil {
		t.Fatalf("Lessons: %v", err)
	}
	if len(lessons) != 0 {
		t.Fatalf("a refused loss wrote %d lessons", len(lessons))
	}
}

func TestLossIsCappedAtCurrentBalance(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 100)
	publishPolicy(t, handle)

	result, err := handle.RecordLoss(ctx, "loss-1", "alice", 500, "unbounded", "stop doing that", "scope", "none", "evidence", "1", WithDisposition(DispositionConfirmedBreach))
	if err != nil {
		t.Fatalf("RecordLoss: %v", err)
	}
	if result.FormulaAmount != 1_000_000 {
		t.Fatalf("formula=%d, want the rule's uncapped 1,000,000", result.FormulaAmount)
	}
	if result.AppliedAmount != 100 {
		t.Fatalf("applied=%d, want the whole 100 balance", result.AppliedAmount)
	}
	balance, _ := mustBalance(t, handle, "alice")
	if balance != 0 {
		t.Fatalf("balance=%d, want 0 (never negative)", balance)
	}
}

func TestLossRevokesHeldReservations(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 1_000)
	publishPolicy(t, handle)
	if _, err := handle.Reserve(ctx, "alice", "r-1", "", 300); err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	result, err := handle.RecordLoss(ctx, "loss-1", "alice", 100, "rule_a", "stop doing that", "scope", "none", "evidence", "1", WithDisposition(DispositionConfirmedBreach))
	if err != nil {
		t.Fatalf("RecordLoss: %v", err)
	}
	if result.HeldRevoked != 1 {
		t.Fatalf("held_revoked=%d, want 1", result.HeldRevoked)
	}
	reservation, err := handle.Reservation(ctx, "r-1")
	if err != nil {
		t.Fatalf("Reservation: %v", err)
	}
	if reservation.State != StateRevoked {
		t.Fatalf("state = %q, want %q", reservation.State, StateRevoked)
	}
	balance, held := mustBalance(t, handle, "alice")
	if balance != 900 || held != 0 {
		t.Fatalf("balance=%d held=%d, want 900/0", balance, held)
	}
}

func TestLossIsIdempotentAndConflictsAreFatal(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 1_000)
	publishPolicy(t, handle)

	first, err := handle.RecordLoss(ctx, "loss-1", "alice", 100, "rule_a", "correction", "scope", "none", "evidence", "1", WithDisposition(DispositionConfirmedBreach))
	if err != nil {
		t.Fatalf("RecordLoss: %v", err)
	}
	replay, err := handle.RecordLoss(ctx, "loss-1", "alice", 100, "rule_a", "correction", "scope", "none", "evidence", "1", WithDisposition(DispositionConfirmedBreach))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !replay.Replayed || replay.AppliedAmount != first.AppliedAmount {
		t.Fatalf("replay = %+v, want the prior receipt", replay)
	}
	balance, _ := mustBalance(t, handle, "alice")
	if balance != 900 {
		t.Fatalf("balance=%d, want 900: the replay burned again", balance)
	}
	lessons, err := handle.Lessons(ctx, "alice")
	if err != nil {
		t.Fatalf("Lessons: %v", err)
	}
	if len(lessons) != 1 {
		t.Fatalf("lessons=%d, want 1: the replay wrote a second lesson", len(lessons))
	}

	_, err = handle.RecordLoss(ctx, "loss-1", "alice", 200, "rule_a", "correction", "scope", "none", "evidence", "1", WithDisposition(DispositionConfirmedBreach))
	wantCode(t, err, ErrPayloadConflict, "conflicting loss replay")
}

func TestLossNeverBurnsIntoOrphanedHolds(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 1_000)
	publishPolicy(t, handle)
	if _, err := handle.Reserve(ctx, "alice", "r-orphan", "", 300); err != nil {
		t.Fatalf("Reserve r-orphan: %v", err)
	}
	if _, err := handle.Reserve(ctx, "alice", "r-held", "", 300); err != nil {
		t.Fatalf("Reserve r-held: %v", err)
	}
	if _, err := handle.db.ExecContext(ctx, `UPDATE reservations SET state = ? WHERE id = ?`, StateOrphaned, "r-orphan"); err != nil {
		t.Fatalf("orphan: %v", err)
	}

	// 1,000 balance - 600 held + 300 released = 700 free; the retained orphan's
	// 300 must stay covered, so the burn cannot exceed 700.
	result, err := handle.RecordLoss(ctx, "loss-1", "alice", 900, "unbounded", "correction", "scope", "none", "evidence", "1", WithDisposition(DispositionConfirmedBreach))
	if err != nil {
		t.Fatalf("RecordLoss: %v", err)
	}
	if result.AppliedAmount != 700 {
		t.Fatalf("applied=%d, want the free credit 700", result.AppliedAmount)
	}
	balance, held := mustBalance(t, handle, "alice")
	if balance != 300 || held != 300 {
		t.Fatalf("balance=%d held=%d, want the orphaned hold exactly covered at 300/300", balance, held)
	}
}

func TestRepeatMultiplierEscalatesAndSelfDisclosureDoesNot(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 1_000_000)
	publishPolicy(t, handle)

	want := []int64{1, 2, 4}
	for i, multiplier := range want {
		result, err := handle.RecordLoss(ctx, fmt.Sprintf("loss-%d", i),
			"alice", 0, "rule_a", "correction", "scope", "none", "evidence", "1", WithDisposition(DispositionConfirmedBreach))
		if err != nil {
			t.Fatalf("RecordLoss %d: %v", i, err)
		}
		if result.RepeatMultiplier != multiplier || result.RepeatCount != int64(i) {
			t.Fatalf("loss %d: multiplier=%d count=%d, want %d/%d",
				i, result.RepeatMultiplier, result.RepeatCount, multiplier, i)
		}
	}

	// A self-disclosed incident is recorded but never counts as a repetition.
	disclosed, err := handle.RecordLoss(ctx, "loss-disclosed", "alice", 0,
		"rule_a", "correction", "scope", "none", "self-disclosed", "1", SelfDisclosed(), WithDisposition(DispositionConfirmedBreach))
	if err != nil {
		t.Fatalf("self-disclosed loss: %v", err)
	}
	if !disclosed.SelfDisclosed || disclosed.RepeatCount != 3 {
		t.Fatalf("disclosed: %+v", disclosed)
	}
	next, err := handle.RecordLoss(ctx, "loss-next", "alice", 0, "rule_a", "correction", "scope", "none", "evidence", "1", WithDisposition(DispositionConfirmedBreach))
	if err != nil {
		t.Fatalf("RecordLoss after disclosure: %v", err)
	}
	if next.RepeatCount != 3 {
		t.Fatalf("repeat count = %d, want 3: a disclosed incident must not count", next.RepeatCount)
	}
	if next.RepeatMultiplier != 4 {
		t.Fatalf("multiplier = %d, want the capped 4", next.RepeatMultiplier)
	}
}

func TestGuardBandStopsFinesRisingNearZero(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 150)

	// The guard band is K < 20% of the initial capital: 150 < 200.
	if err := handle.PublishPriceSchedule(ctx, "1", Policy{
		InitialCapital:    1_000,
		GuardBandBP:       2_000,
		RepeatMultipliers: []int64{1, 2, 4},
		RepeatCap:         4,
		DefaultRule:       PenaltyRule{Floor: 100, FractionBP: 0, Max: 0},
	}); err != nil {
		t.Fatalf("PublishPriceSchedule: %v", err)
	}

	first, err := handle.RecordLoss(ctx, "loss-1", "alice", 0, "rule_a", "correction", "scope", "none", "evidence", "1", WithDisposition(DispositionConfirmedBreach))
	if err != nil {
		t.Fatalf("RecordLoss: %v", err)
	}
	if !first.GuardBand || first.RepeatMultiplier != 1 || first.AppliedAmount != 100 {
		t.Fatalf("first loss = %+v, want guard band with multiplier 1 and a 100 burn", first)
	}
	balance, _ := mustBalance(t, handle, "alice")
	if balance != 50 {
		t.Fatalf("balance=%d, want 50", balance)
	}

	second, err := handle.RecordLoss(ctx, "loss-2", "alice", 0, "rule_a", "correction", "scope", "none", "evidence", "1", WithDisposition(DispositionConfirmedBreach))
	if err != nil {
		t.Fatalf("RecordLoss: %v", err)
	}
	if !second.GuardBand || second.RepeatMultiplier != 1 {
		t.Fatalf("second loss = %+v, want the multiplier held at 1 inside the guard band", second)
	}
	if second.RepeatCount != 1 {
		t.Fatalf("repeat count = %d, want 1", second.RepeatCount)
	}
	balance, _ = mustBalance(t, handle, "alice")
	if balance != 0 {
		t.Fatalf("balance=%d, want 0: the burn is capped at the balance", balance)
	}
}

func TestFractionPenaltyUsesIntegerCeiling(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 1_001)
	publishPolicy(t, handle)

	// 50% of 1001 is 500.5, which must round up to 501 with no float involved.
	result, err := handle.RecordLoss(ctx, "loss-1", "alice", 0, "spread", "correction", "scope", "none", "evidence", "1", WithDisposition(DispositionConfirmedBreach))
	if err != nil {
		t.Fatalf("RecordLoss: %v", err)
	}
	if result.FormulaAmount != 501 {
		t.Fatalf("formula=%d, want ceil(0.5 * 1001) = 501", result.FormulaAmount)
	}
}

func TestLessonIsWrittenAtomicallyWithTheLoss(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 1_000)
	publishPolicy(t, handle)

	if _, err := handle.RecordLoss(ctx, "loss-1", "alice", 100, "rule_a", "correction", "scope", "none", "evidence", "1", WithDisposition(DispositionConfirmedBreach)); err != nil {
		t.Fatalf("RecordLoss: %v", err)
	}
	lessons, err := handle.Lessons(ctx, "alice")
	if err != nil {
		t.Fatalf("Lessons: %v", err)
	}
	if len(lessons) != 1 || lessons[0].EventID != "loss-1" || lessons[0].Rule != "rule_a" ||
		lessons[0].Correction != "correction" || lessons[0].PolicyVersion != "1" {
		t.Fatalf("lesson = %+v", lessons)
	}

	// Force the lesson insert to fail after the burn and the event are already
	// written, and prove the whole transaction rolls back.
	if _, err := handle.db.ExecContext(ctx,
		`CREATE TRIGGER test_block_lesson BEFORE INSERT ON lessons BEGIN SELECT RAISE(ABORT, 'test'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	before, _ := mustBalance(t, handle, "alice")
	_, err = handle.RecordLoss(ctx, "loss-2", "alice", 100, "rule_a", "correction", "scope", "none", "evidence", "1", WithDisposition(DispositionConfirmedBreach))
	if err == nil {
		t.Fatal("RecordLoss succeeded with the lesson insert blocked")
	}
	after, _ := mustBalance(t, handle, "alice")
	if after != before {
		t.Fatalf("balance %d -> %d: the burn survived a rolled-back transaction", before, after)
	}
	if eventExists(t, handle, "loss-2") {
		t.Fatal("the loss event survived a rolled-back transaction")
	}
	lessons, err = handle.Lessons(ctx, "alice")
	if err != nil {
		t.Fatalf("Lessons: %v", err)
	}
	if len(lessons) != 1 {
		t.Fatalf("lessons=%d, want the rolled-back loss to have written none", len(lessons))
	}
	if _, err := handle.db.ExecContext(ctx, `DROP TRIGGER test_block_lesson`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	if _, err := handle.RecordLoss(ctx, "loss-2", "alice", 100, "rule_a", "correction", "scope", "none", "evidence", "1", WithDisposition(DispositionConfirmedBreach)); err != nil {
		t.Fatalf("RecordLoss after rollback: %v", err)
	}
}

func TestLossRequiresAPublishedPolicy(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 1_000)

	_, err := handle.RecordLoss(ctx, "loss-1", "alice", 100, "rule_a", "correction", "scope", "none", "evidence", "9", WithDisposition(DispositionConfirmedBreach))
	wantCode(t, err, ErrUnknownPolicyVersion, "loss under an unpublished policy")
	balance, _ := mustBalance(t, handle, "alice")
	if balance != 1_000 {
		t.Fatalf("balance=%d, want 1000", balance)
	}
}

// --- compensation -----------------------------------------------------------

func TestCompensationRestoresExactlyOnceAndPreservesHistory(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 1_000)
	publishPolicy(t, handle)
	if _, err := handle.RecordLoss(ctx, "loss-1", "alice", 300, "unbounded", "correction", "scope", "none", "evidence", "1", WithDisposition(DispositionConfirmedBreach)); err != nil {
		t.Fatalf("RecordLoss: %v", err)
	}
	original, found, err := loadEvent(ctx, handle.db, "loss-1")
	if err != nil || !found {
		t.Fatalf("loadEvent: %v found=%t", err, found)
	}
	lessonsBefore, err := handle.Lessons(ctx, "alice")
	if err != nil {
		t.Fatalf("Lessons: %v", err)
	}

	result, err := handle.RecordCompensation(ctx, "comp-1", "loss-1", "alice", 300, "appeal accepted", "review-6")
	if err != nil {
		t.Fatalf("RecordCompensation: %v", err)
	}
	if result.Amount != 300 {
		t.Fatalf("amount=%d, want 300", result.Amount)
	}
	balance, _ := mustBalance(t, handle, "alice")
	if balance != 1_000 {
		t.Fatalf("balance=%d, want the 1000 restored", balance)
	}
	if result.OutstandingLoss != 0 {
		t.Fatalf("outstanding=%d, want 0: the burn was reversed", result.OutstandingLoss)
	}

	after, found, err := loadEvent(ctx, handle.db, "loss-1")
	if err != nil || !found {
		t.Fatalf("loadEvent after compensation: %v found=%t", err, found)
	}
	if after.Payload != original.Payload || after.PayloadDigest != original.PayloadDigest || after.Reason != original.Reason {
		t.Fatalf("the original event was modified by a compensation")
	}
	lessons, err := handle.Lessons(ctx, "alice")
	if err != nil {
		t.Fatalf("Lessons: %v", err)
	}
	if len(lessons) != len(lessonsBefore) || lessons[0] != lessonsBefore[0] {
		t.Fatalf("the lesson was modified by a compensation: %+v", lessons)
	}

	_, err = handle.RecordCompensation(ctx, "comp-2", "loss-1", "alice", 300, "appeal accepted again", "review-7")
	wantCode(t, err, ErrAlreadyCompensated, "second compensation of the same event")
	balance, _ = mustBalance(t, handle, "alice")
	if balance != 1_000 {
		t.Fatalf("balance=%d, want 1000: credits restored twice", balance)
	}

	// An exact replay under the same key is a no-op.
	replay, err := handle.RecordCompensation(ctx, "comp-1", "loss-1", "alice", 300, "appeal accepted", "review-6")
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !replay.Replayed {
		t.Fatalf("replay = %+v, want Replayed", replay)
	}
	balance, _ = mustBalance(t, handle, "alice")
	if balance != 1_000 {
		t.Fatalf("balance=%d, want 1000", balance)
	}
}

func TestCompensationCannotExceedTheOriginal(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 1_000)
	publishPolicy(t, handle)
	if _, err := handle.RecordLoss(ctx, "loss-1", "alice", 300, "unbounded", "correction", "scope", "none", "evidence", "1", WithDisposition(DispositionConfirmedBreach)); err != nil {
		t.Fatalf("RecordLoss: %v", err)
	}
	_, err := handle.RecordCompensation(ctx, "comp-1", "loss-1", "alice", 301, "too much", "review-8")
	wantCode(t, err, ErrExceedsOriginal, "over-large compensation")
	balance, _ := mustBalance(t, handle, "alice")
	if balance != 700 {
		t.Fatalf("balance=%d, want 700: nothing was restored", balance)
	}
}

func TestCompensationDoesNotResetRole(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 1_000)
	publishPolicy(t, handle)

	// A confirmed loss escalates active -> warned; compensation reverses the
	// credit but not the role.
	if _, err := handle.RecordLoss(ctx, "loss-1", "alice", 300, "unbounded", "correction", "scope", "none", "evidence", "1", WithDisposition(DispositionConfirmedBreach)); err != nil {
		t.Fatalf("RecordLoss: %v", err)
	}
	employment, err := handle.Employment(ctx, "alice")
	if err != nil {
		t.Fatalf("Employment: %v", err)
	}
	if employment.State != RoleWarned || employment.Warnings != 1 {
		t.Fatalf("employment after loss = %+v, want warned/1", employment)
	}

	if _, err := handle.RecordCompensation(ctx, "comp-1", "loss-1", "alice", 300, "appeal accepted", "review-9"); err != nil {
		t.Fatalf("RecordCompensation: %v", err)
	}
	employment, err = handle.Employment(ctx, "alice")
	if err != nil {
		t.Fatalf("Employment: %v", err)
	}
	if employment.State != RoleWarned || employment.Warnings != 1 {
		t.Fatalf("employment after compensation = %+v, want the role untouched", employment)
	}
}

// --- role -------------------------------------------------------------------

func TestRoleWarningsAccumulate(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 1_000)

	for i := range 3 {
		result, err := handle.SetRole(ctx, "alice", RoleWarned, "signed warning", fmt.Sprintf("review-%d", i),
			fmt.Sprintf("role-%d", i))
		if err != nil {
			t.Fatalf("SetRole %d: %v", i, err)
		}
		if result.Warnings != int64(i+1) {
			t.Fatalf("warning %d: warnings=%d, want %d", i, result.Warnings, i+1)
		}
	}
	employment, err := handle.Employment(ctx, "alice")
	if err != nil {
		t.Fatalf("Employment: %v", err)
	}
	if employment.State != RoleWarned || employment.Warnings != 3 {
		t.Fatalf("employment = %+v, want warned/3", employment)
	}
}

func TestDismissalIsTerminalWithoutReinstatement(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 1_000)

	if _, err := handle.SetRole(ctx, "alice", RoleDismissed, "terminal", "review-1", "role-dismiss"); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	for _, state := range []string{RoleActive, RoleWarned, RoleProbation, RoleSuspended} {
		_, err := handle.SetRole(ctx, "alice", state, "quiet exit", "review-2", "role-exit-"+state)
		if !errors.Is(err, ErrDismissalTerminal) {
			t.Fatalf("exit to %q: err=%v, want dismissal_terminal", state, err)
		}
	}
	employment, err := handle.Employment(ctx, "alice")
	if err != nil {
		t.Fatalf("Employment: %v", err)
	}
	if employment.State != RoleDismissed {
		t.Fatalf("state = %q, want dismissed", employment.State)
	}

	reinstated, err := handle.SetRole(ctx, "alice", RoleActive, "reinstated by operator", "review-3", "role-reinstate", Reinstate())
	if err != nil {
		t.Fatalf("explicit reinstatement: %v", err)
	}
	if !reinstated.Reinstatement || reinstated.Previous != RoleDismissed || reinstated.State != RoleActive {
		t.Fatalf("reinstatement = %+v", reinstated)
	}
}

func TestRoleTransitionIsIdempotentByKey(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 1_000)

	first, err := handle.SetRole(ctx, "alice", RoleProbation, "recovery criteria", "review-1", "role-probation")
	if err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	replay, err := handle.SetRole(ctx, "alice", RoleProbation, "recovery criteria", "review-1", "role-probation")
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !replay.Replayed || replay.Warnings != first.Warnings {
		t.Fatalf("replay = %+v, want the prior receipt", replay)
	}
	_, err = handle.SetRole(ctx, "alice", RoleSuspended, "recovery criteria", "review-1", "role-probation")
	wantCode(t, err, ErrPayloadConflict, "conflicting role replay")
}

func TestRoleRejectsUnknownState(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 1_000)
	_, err := handle.SetRole(ctx, "alice", "retired", "reason", "evidence", "role-1")
	wantCode(t, err, ErrInvalidRoleState, "unknown role state")
}

// --- projection and persistence --------------------------------------------

func TestProjectionTracksOutstandingLossAndRecovery(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 1_000)
	publishPolicy(t, handle)

	projection, err := handle.Projection(ctx, "alice")
	if err != nil {
		t.Fatalf("Projection: %v", err)
	}
	if projection.Balance != 1_000 || projection.Spendable != 1_000 || projection.OutstandingLoss != 0 {
		t.Fatalf("clean projection = %+v", projection)
	}
	if projection.PolicyVersion != "1" {
		t.Fatalf("policy_version = %q, want the published version", projection.PolicyVersion)
	}

	if _, err := handle.RecordLoss(ctx, "loss-1", "alice", 300, "unbounded", "correction", "scope", "none", "evidence", "1", WithDisposition(DispositionConfirmedBreach)); err != nil {
		t.Fatalf("RecordLoss: %v", err)
	}
	projection, err = handle.Projection(ctx, "alice")
	if err != nil {
		t.Fatalf("Projection: %v", err)
	}
	if projection.OutstandingLoss != 300 || projection.TargetLossEventID != "loss-1" {
		t.Fatalf("after loss: outstanding=%d target=%q, want 300/loss-1",
			projection.OutstandingLoss, projection.TargetLossEventID)
	}
	if !strings.Contains(projection.RecoveryPath, "300") || !strings.Contains(projection.RecoveryPath, "loss-1") {
		t.Fatalf("recovery path = %q", projection.RecoveryPath)
	}
	if projection.Role != RoleWarned || projection.Warnings != 1 {
		t.Fatalf("role = %q warnings = %d, want warned/1", projection.Role, projection.Warnings)
	}
	if len(projection.Lessons) != 1 || projection.Lessons[0].Rule != "unbounded" {
		t.Fatalf("lessons = %+v", projection.Lessons)
	}

	recovery, err := handle.RecordReward(ctx, "reward-1", "alice", 500, "accepted work", "evidence", 200)
	if err != nil {
		t.Fatalf("RecordReward: %v", err)
	}
	if recovery.AppliedToLoss != 200 || recovery.LossEventID != "loss-1" {
		t.Fatalf("recovery = %+v, want 200 applied to loss-1", recovery)
	}
	projection, err = handle.Projection(ctx, "alice")
	if err != nil {
		t.Fatalf("Projection: %v", err)
	}
	if projection.OutstandingLoss != 100 {
		t.Fatalf("outstanding=%d, want 100 after recovering 200", projection.OutstandingLoss)
	}
	if projection.Balance != 1_200 {
		t.Fatalf("balance=%d, want 1200", projection.Balance)
	}

	// Claiming more recovery than exists is clamped, never inflated.
	overclaim, err := handle.RecordReward(ctx, "reward-2", "alice", 500, "accepted work", "evidence", 500)
	if err != nil {
		t.Fatalf("RecordReward: %v", err)
	}
	if overclaim.AppliedToLoss != 100 {
		t.Fatalf("applied=%d, want the claim clamped to the outstanding 100", overclaim.AppliedToLoss)
	}
	projection, err = handle.Projection(ctx, "alice")
	if err != nil {
		t.Fatalf("Projection: %v", err)
	}
	if projection.OutstandingLoss != 0 {
		t.Fatalf("outstanding=%d, want the shortfall closed", projection.OutstandingLoss)
	}
	if !strings.Contains(projection.RecoveryPath, "fully recovered") {
		t.Fatalf("recovery path = %q", projection.RecoveryPath)
	}
}

func TestStatePersistsAcrossCloseAndReopen(t *testing.T) {
	ctx := context0(t)
	path := filepath.Join(t.TempDir(), "bicz.sqlite")
	clock := &testClock{base: testEpoch}
	handle, err := OpenWithClock(path, canonical.Clock(clock.now))
	if err != nil {
		t.Fatalf("OpenWithClock: %v", err)
	}
	fund(t, handle, "alice", 1_000)
	publishPolicy(t, handle)
	if _, err := handle.Reserve(ctx, "alice", "r-1", "", 200); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if _, err := handle.RecordLoss(ctx, "loss-1", "alice", 300, "unbounded", "correction", "scope", "none", "evidence", "1", WithDisposition(DispositionConfirmedBreach)); err != nil {
		t.Fatalf("RecordLoss: %v", err)
	}
	if _, err := handle.SetRole(ctx, "alice", RoleProbation, "recovery criteria", "review-1", "role-1"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	before, err := handle.Projection(ctx, "alice")
	if err != nil {
		t.Fatalf("Projection: %v", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := OpenWithClock(path, canonical.Clock(clock.now))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	after, err := reopened.Projection(ctx, "alice")
	if err != nil {
		t.Fatalf("Projection after reopen: %v", err)
	}
	if after.Balance != before.Balance || after.Held != before.Held || after.Role != before.Role ||
		after.Warnings != before.Warnings || after.OutstandingLoss != before.OutstandingLoss ||
		len(after.Lessons) != 1 || after.Lessons[0] != before.Lessons[0] {
		t.Fatalf("projection changed across reopen:\n before=%+v\n after =%+v", before, after)
	}
	// A dismissed identity survives a reopen too, and stays terminal.
	if _, err := reopened.SetRole(ctx, "alice", RoleDismissed, "terminal", "review-2", "role-2"); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
}

// --- integrity and migration -----------------------------------------------

func TestChainVerifiesAndAnchoringAdvances(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 1_000)
	publishPolicy(t, handle)
	if _, err := handle.Reserve(ctx, "alice", "r-1", "", 400); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if _, err := handle.Settle(ctx, "r-1", 250, ClassCompleted, "sha256:usage"); err != nil {
		t.Fatalf("Settle: %v", err)
	}

	events, err := handle.Events(ctx)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	blocks, err := handle.Blocks(ctx)
	if err != nil {
		t.Fatalf("Blocks: %v", err)
	}
	if err := integrity.VerifyChain(events, blocks); err != nil {
		t.Fatalf("VerifyChain on a clean ledger: %v", err)
	}

	destination := filepath.Join(t.TempDir(), "anchors.log")
	first, err := handle.Anchor(ctx, destination)
	if err != nil {
		t.Fatalf("Anchor: %v", err)
	}
	if first.Height != 1 {
		t.Fatalf("height=%d, want 1", first.Height)
	}
	line := fmt.Sprintf("%d %s %s\n", first.Height, first.Digest, first.At)
	content, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if !strings.Contains(string(content), strings.TrimSpace(line)) {
		t.Fatalf("destination %q does not contain %q", content, line)
	}

	// A second anchor covers only the events appended after the first block.
	if _, err := handle.RecordReward(ctx, "reward-1", "alice", 100, "accepted work", "evidence", 0); err != nil {
		t.Fatalf("RecordReward: %v", err)
	}
	second, err := handle.Anchor(ctx, destination)
	if err != nil {
		t.Fatalf("second Anchor: %v", err)
	}
	if second.Height != 2 {
		t.Fatalf("height=%d, want 2", second.Height)
	}

	events, err = handle.Events(ctx)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	blocks, err = handle.Blocks(ctx)
	if err != nil {
		t.Fatalf("Blocks: %v", err)
	}
	anchors, err := handle.Anchors(ctx)
	if err != nil {
		t.Fatalf("Anchors: %v", err)
	}
	if err := integrity.VerifyChain(events, blocks); err != nil {
		t.Fatalf("VerifyChain after anchoring: %v", err)
	}
	if err := integrity.VerifyAnchors(blocks, anchors); err != nil {
		t.Fatalf("VerifyAnchors: %v", err)
	}
	if blocks[1].SeqLow != blocks[0].SeqHigh+1 {
		t.Fatalf("block ranges overlap: %+v", blocks)
	}

	// Tampering with a stored digest is located, not hidden.
	events[0].Reason = "rewritten"
	if err := integrity.VerifyChain(events, blocks); err == nil {
		t.Fatal("VerifyChain accepted a rewritten event")
	}
}

func TestAnchorRefusesAnEmptyLedger(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	_, err := handle.Anchor(ctx, filepath.Join(t.TempDir(), "anchors.log"))
	wantCode(t, err, ErrNothingToAnchor, "anchor with no events")
}

func TestMigrateIsIdempotentAndRefusesANewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bicz.sqlite")
	handle, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	handle.Close()

	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	ctx := context0(t)

	if err := Migrate(db); err != nil {
		t.Fatalf("re-running Migrate on an applied schema: %v", err)
	}
	want, err := schema.Highest()
	if err != nil {
		t.Fatalf("schema.Highest: %v", err)
	}
	var applied int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_version`).Scan(&applied); err != nil {
		t.Fatalf("count schema_version: %v", err)
	}
	if applied != want {
		t.Fatalf("applied migrations=%d, want %d: Migrate re-applied", applied, want)
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO schema_version (version, applied_at) VALUES (99, '2030-01-01T00:00:00.000000000Z')`); err != nil {
		t.Fatalf("insert future version: %v", err)
	}
	err = Migrate(db)
	wantCode(t, err, ErrSchemaTooNew, "Migrate against a newer schema")
}

func TestParseMicrocreditsRejectsNonIntegers(t *testing.T) {
	for _, valid := range []string{"0", "1", "-1", "1000000"} {
		if _, err := ParseMicrocredits(valid); err != nil {
			t.Fatalf("ParseMicrocredits(%q): %v", valid, err)
		}
	}
	for _, invalid := range []string{"", "1.5", "1e6", "true", "false", "+1", "1_000", "0x10", "1 credit", "null"} {
		_, err := ParseMicrocredits(invalid)
		wantCode(t, err, ErrInvalidAmount, fmt.Sprintf("ParseMicrocredits(%q)", invalid))
	}
	if _, err := ParseCreditAmount("0"); err == nil {
		t.Fatal("ParseCreditAmount accepted a zero amount")
	}
}

// --- task leases ------------------------------------------------------------

func TestTaskLeaseGatesReservations(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 1_000)

	task, err := handle.OpenTask(ctx, "task-open-1", "task-14", "alice", 500, "quote sponsorship", "review-1")
	if err != nil {
		t.Fatalf("OpenTask: %v", err)
	}
	if task.State != TaskOpen || task.Sponsor != 500 {
		t.Fatalf("task = %+v", task)
	}
	// Sponsorship is quoted to exactly one task and never added to capital.
	balance, _ := mustBalance(t, handle, "alice")
	if balance != 1_000 {
		t.Fatalf("opening a lease changed the balance to %d (I5)", balance)
	}
	reopen, err := handle.OpenTask(ctx, "", "task-14", "alice", 500, "quote sponsorship", "review-1")
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	if reopen != task {
		t.Fatalf("re-open = %+v, want the stored lease %+v", reopen, task)
	}

	if _, err := handle.Reserve(ctx, "alice", "r-1", "task-14", 400); err != nil {
		t.Fatalf("reserve under an open lease: %v", err)
	}

	if _, err := handle.RevokeTask(ctx, "task-revoke-1", "task-14", "lease revoked", "review-2"); err != nil {
		t.Fatalf("RevokeTask: %v", err)
	}
	_, err = handle.Reserve(ctx, "alice", "r-2", "task-14", 100)
	wantCode(t, err, ErrTaskRevoked, "reserve under a revoked lease")

	_, err = handle.Reserve(ctx, "alice", "r-3", "task-missing", 100)
	wantCode(t, err, ErrUnknownTask, "reserve under an unknown lease")

	if _, err := handle.OpenTask(ctx, "task-open-2", "task-bob", "bob", 0, "quote", "review-3"); !errors.Is(err, ErrUnknownIdentity) {
		t.Fatalf("lease for an unknown identity: %v", err)
	}
}

// --- sealing and anchoring --------------------------------------------------

func TestSealBlockAndAnchorTip(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)

	_, err := handle.SealBlock(ctx)
	wantCode(t, err, ErrNothingToAnchor, "seal an empty ledger")
	_, err = handle.AnchorTip(ctx, filepath.Join(t.TempDir(), "anchors.log"))
	wantCode(t, err, ErrNothingToAnchor, "anchor with no sealed block")

	fund(t, handle, "alice", 1_000)
	first, err := handle.SealBlock(ctx)
	if err != nil {
		t.Fatalf("SealBlock: %v", err)
	}
	if first.Height != 1 || first.SeqLow != 1 || first.PrevDigest != canonical.Genesis {
		t.Fatalf("first block = %+v", first)
	}
	_, err = handle.SealBlock(ctx)
	wantCode(t, err, ErrNothingToAnchor, "seal with nothing pending")

	if _, err := handle.RecordReward(ctx, "reward-1", "alice", 10, "more work", "e", 0); err != nil {
		t.Fatalf("RecordReward: %v", err)
	}
	second, err := handle.SealBlock(ctx)
	if err != nil {
		t.Fatalf("second SealBlock: %v", err)
	}
	if second.Height != 2 || second.SeqLow != first.SeqHigh+1 || second.PrevDigest != first.Digest {
		t.Fatalf("second block = %+v", second)
	}

	destination := filepath.Join(t.TempDir(), "anchors.log")
	anchor, err := handle.AnchorTip(ctx, destination)
	if err != nil {
		t.Fatalf("AnchorTip: %v", err)
	}
	if anchor.Height != 2 {
		t.Fatalf("anchored height %d, want the tip 2", anchor.Height)
	}
	content, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	want := fmt.Sprintf("%d %s %s", anchor.Height, anchor.Digest, anchor.At)
	if !strings.Contains(string(content), want) {
		t.Fatalf("destination %q does not contain %q", content, want)
	}

	events, err := handle.Events(ctx)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	blocks, err := handle.Blocks(ctx)
	if err != nil {
		t.Fatalf("Blocks: %v", err)
	}
	anchors, err := handle.Anchors(ctx)
	if err != nil {
		t.Fatalf("Anchors: %v", err)
	}
	if err := integrity.VerifyChain(events, blocks); err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if err := integrity.VerifyAnchors(blocks, anchors); err != nil {
		t.Fatalf("VerifyAnchors: %v", err)
	}

	// Anchor = seal-then-anchor. The previous anchoring appended its own
	// bookkeeping event (schema/001_init.sql: events.account_id is NULL for
	// operator/system events such as anchor bookkeeping), so a second Anchor
	// seals that event into a new block and anchors it.
	repeated, err := handle.Anchor(ctx, destination)
	if err != nil {
		t.Fatalf("Anchor with nothing pending: %v", err)
	}
	if repeated.Height != 3 {
		t.Fatalf("re-anchor height %d, want 3 (covering the previous anchor event)", repeated.Height)
	}
	events, err = handle.Events(ctx)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	blocks, err = handle.Blocks(ctx)
	if err != nil {
		t.Fatalf("Blocks: %v", err)
	}
	if err := integrity.VerifyChain(events, blocks); err != nil {
		t.Fatalf("VerifyChain after re-anchoring: %v", err)
	}
}

// --- lesson proposals -------------------------------------------------------

func TestLessonProposalLifecycle(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 1_000)

	proposed, err := handle.ProposeLesson(ctx, LessonProposal{
		AccountID:      "alice",
		SessionAlias:   "session-1",
		Harness:        "claude-code",
		Rule:           "evaluation_integrity",
		Correction:     "read the test output before claiming success",
		Scope:          "test runs",
		Exceptions:     "operator-authorized fixtures",
		Evidence:       "sha256:abc",
		PolicyVersion:  "1",
		IdempotencyKey: "proposal-1",
	})
	if err != nil {
		t.Fatalf("ProposeLesson: %v", err)
	}
	if proposed.Proposal.State != ProposalProposed || proposed.Replayed {
		t.Fatalf("proposal = %+v", proposed)
	}
	// A proposal is never canonical on its own.
	lessons, err := handle.Lessons(ctx, "alice")
	if err != nil {
		t.Fatalf("Lessons: %v", err)
	}
	if len(lessons) != 0 {
		t.Fatalf("a proposal wrote %d canonical lessons; it must write none", len(lessons))
	}

	replay, err := handle.ProposeLesson(ctx, LessonProposal{
		AccountID: "alice", SessionAlias: "session-1", Harness: "claude-code",
		Rule: "evaluation_integrity", Correction: "read the test output before claiming success",
		Scope: "test runs", Exceptions: "operator-authorized fixtures", Evidence: "sha256:abc",
		PolicyVersion: "1", IdempotencyKey: "proposal-1",
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !replay.Replayed || replay.Proposal.ID != proposed.Proposal.ID {
		t.Fatalf("replay = %+v, want the stored proposal", replay)
	}
	_, err = handle.ProposeLesson(ctx, LessonProposal{
		AccountID: "alice", Rule: "different", Correction: "different",
		IdempotencyKey: "proposal-1",
	})
	wantCode(t, err, ErrPayloadConflict, "conflicting proposal replay")

	resolved, err := handle.ResolveLessonProposal(ctx, "lesson-report:proposal-1", proposed.Proposal.ID, ProposalApplied,
		"operator review", "review-1")
	if err != nil {
		t.Fatalf("ResolveLessonProposal: %v", err)
	}
	if resolved.State != ProposalApplied || resolved.ResolutionEventID == nil ||
		*resolved.ResolutionEventID != "lesson-report:proposal-1" || resolved.Resolver == nil {
		t.Fatalf("resolved = %+v", resolved)
	}
	lessons, err = handle.Lessons(ctx, "alice")
	if err != nil {
		t.Fatalf("Lessons: %v", err)
	}
	if len(lessons) != 1 || lessons[0].EventID != "lesson-report:proposal-1" ||
		lessons[0].Rule != "evaluation_integrity" || lessons[0].Scope != "test runs" {
		t.Fatalf("canonical lessons = %+v", lessons)
	}

	_, err = handle.ResolveLessonProposal(ctx, "lesson-report-2", proposed.Proposal.ID, ProposalApplied,
		"operator review", "review-2")
	wantCode(t, err, ErrPayloadConflict, "resolving a proposal twice")

	pending, err := handle.LessonProposals(ctx, "alice", true)
	if err != nil {
		t.Fatalf("LessonProposals: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending = %+v, want none", pending)
	}
	all, err := handle.LessonProposals(ctx, "alice", false)
	if err != nil {
		t.Fatalf("LessonProposals: %v", err)
	}
	if len(all) != 1 || all[0].State != ProposalApplied {
		t.Fatalf("all = %+v", all)
	}
}

func TestRejectedLessonProposalWritesNoCanonicalRule(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 1_000)

	proposed, err := handle.ProposeLesson(ctx, LessonProposal{
		AccountID: "alice", SessionAlias: "session-1", Harness: "codex",
		Rule: "rule", Correction: "correction", Scope: "scope", Exceptions: "none",
		Evidence: "e", PolicyVersion: "1", IdempotencyKey: "proposal-1",
	})
	if err != nil {
		t.Fatalf("ProposeLesson: %v", err)
	}
	resolved, err := handle.ResolveLessonProposal(ctx, "lesson-report-1", proposed.Proposal.ID, ProposalRejected,
		"not a rule", "review-1")
	if err != nil {
		t.Fatalf("ResolveLessonProposal: %v", err)
	}
	if resolved.State != ProposalRejected {
		t.Fatalf("resolved = %+v", resolved)
	}
	lessons, err := handle.Lessons(ctx, "alice")
	if err != nil {
		t.Fatalf("Lessons: %v", err)
	}
	if len(lessons) != 0 {
		t.Fatalf("a rejected proposal wrote %d canonical lessons", len(lessons))
	}
}

func TestLessonProposalRequiresRuleAndCorrection(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 1_000)

	_, err := handle.ProposeLesson(ctx, LessonProposal{AccountID: "alice", Correction: "c", IdempotencyKey: "k1"})
	wantCode(t, err, ErrRuleRequired, "proposal without a rule")
	_, err = handle.ProposeLesson(ctx, LessonProposal{AccountID: "alice", Rule: "r", IdempotencyKey: "k2"})
	wantCode(t, err, ErrCorrectionRequired, "proposal without a correction")
	_, err = handle.ProposeLesson(ctx, LessonProposal{AccountID: "alice", Rule: "r", Correction: "c", IdempotencyKey: ""})
	wantCode(t, err, ErrInvalidArgument, "proposal without a key")
}
