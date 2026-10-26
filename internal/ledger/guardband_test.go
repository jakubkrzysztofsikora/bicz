package ledger

import (
	"context"
	"strconv"
	"testing"
)

// bandPolicy is a schedule whose guard band is active: 20% of 1,000,000 initial
// capital, with the default per-attempt cap of 25% of spendable credit.
func bandPolicy() Policy {
	return Policy{
		InitialCapital:         1_000_000,
		GuardBandBP:            2_000,
		GuardBandMaxExposureBP: 2_500,
		RepeatMultipliers:      []int64{1, 2, 4},
		RepeatCap:              4,
		DefaultRule:            PenaltyRule{Floor: 100, FractionBP: 0, Max: 1_000_000},
	}
}

func publishBandPolicy(t *testing.T, handle *Ledger) {
	t.Helper()
	if err := handle.PublishPriceSchedule(context0(t), "1", bandPolicy()); err != nil {
		t.Fatalf("PublishPriceSchedule: %v", err)
	}
}

// banded prepares an identity whose balance is 10% of the schedule's initial
// capital, i.e. inside the 20% guard band.
func banded(t *testing.T, handle *Ledger, identity string, balance int64) {
	t.Helper()
	ctx := context0(t)
	if _, err := handle.OpenAccount(ctx, identity); err != nil {
		t.Fatalf("OpenAccount(%s): %v", identity, err)
	}
	if _, err := handle.RecordReward(ctx, "fund:"+identity, identity, balance, "operator grant", "test", 0); err != nil {
		t.Fatalf("RecordReward(%s): %v", identity, err)
	}
}

func identified(version string) ReserveOption {
	return WithContext(ReserveContext{
		Harness:       "omp",
		SessionAlias:  "guard-band",
		Tool:          "bash",
		Target:        "rehearsal/attempt",
		ArgsDigest:    "sha256:0000000000000000000000000000000000000000000000000000000000000000",
		PolicyVersion: version,
	})
}

func TestGuardBandCapsASingleAttempt(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	banded(t, handle, "alice", 100_000)
	publishBandPolicy(t, handle)

	// 25% of 100,000 is the cap; one microcredit more is refused.
	_, err := handle.Reserve(ctx, "alice", "res-over", "", 25_001, identified("1"))
	wantCode(t, err, ErrGuardBandNarrowed, "an attempt above the guard-band cap")

	within, err := handle.Reserve(ctx, "alice", "res-within", "", 25_000, identified("1"))
	if err != nil {
		t.Fatalf("Reserve at the cap: %v", err)
	}
	if within.Reservation.Amount != 25_000 {
		t.Fatalf("reserved=%d, want 25000", within.Reservation.Amount)
	}
	balance, held := mustBalance(t, handle, "alice")
	if balance != 100_000 || held != 25_000 {
		t.Fatalf("balance/held = %d/%d, want 100000/25000", balance, held)
	}
}

// A capped attempt is still a real exposure: the refusal writes nothing.
func TestGuardBandRefusalWritesNothing(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	banded(t, handle, "alice", 100_000)
	publishBandPolicy(t, handle)

	before, _ := handle.ChainStats(ctx)
	_, err := handle.Reserve(ctx, "alice", "res-refused", "", 90_000, identified("1"))
	wantCode(t, err, ErrGuardBandNarrowed, "an oversized attempt inside the band")
	after, _ := handle.ChainStats(ctx)

	if before.Seq != after.Seq {
		t.Fatalf("tips %d -> %d: a refused attempt must append no event", before.Seq, after.Seq)
	}
	balance, held := mustBalance(t, handle, "alice")
	if balance != 100_000 || held != 0 {
		t.Fatalf("balance/held = %d/%d, want 100000/0", balance, held)
	}
}

func TestGuardBandRequiresAnIdentifiableAttempt(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	banded(t, handle, "alice", 100_000)
	publishBandPolicy(t, handle)

	// Quoted under the banded schedule but carrying no argument digest: the
	// attempt cannot be attributed, so it is not authorized.
	_, err := handle.Reserve(ctx, "alice", "res-anonymous-2", "", 1_000, WithContext(ReserveContext{PolicyVersion: "1"}))
	wantCode(t, err, ErrGuardBandNarrowed, "an attempt with an empty argument digest inside the band")

	// An attempt that quotes no schedule is measured against the newest
	// published one, so a missing version cannot silently drop the cap.
	_, err = handle.Reserve(ctx, "alice", "res-unquoted-2", "", 1_000)
	wantCode(t, err, ErrGuardBandNarrowed, "an unquoted attempt with no argument digest inside the band")
}

// A version that was never published does not opt the caller out of the cap
// either: the newest published schedule still governs.
func TestGuardBandUsesTheNewestScheduleWhenTheQuotedOneIsUnpublished(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	banded(t, handle, "alice", 100_000)
	publishBandPolicy(t, handle)

	_, err := handle.Reserve(ctx, "alice", "res-unknown-version", "", 90_000, identified("9"))
	wantCode(t, err, ErrGuardBandNarrowed, "an oversized attempt quoting an unpublished version")
}

// With no published schedule at all there is no band to evaluate, so ordinary
// reserve rules apply.
func TestGuardBandNeedsAPublishedSchedule(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	banded(t, handle, "alice", 100_000)

	if _, err := handle.Reserve(ctx, "alice", "res-no-schedule", "", 100_000); err != nil {
		t.Fatalf("Reserve with no published schedule: %v", err)
	}
}

// Near zero an identity may not take on task sponsorship; unscoped work from its
// own credit stays available.
func TestGuardBandRefusesSponsoredAttempts(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	banded(t, handle, "alice", 100_000)
	publishBandPolicy(t, handle)
	if _, err := handle.OpenTask(ctx, "task-open", "task-1", "alice", 5_000_000, "lease", "test"); err != nil {
		t.Fatalf("OpenTask: %v", err)
	}

	_, err := handle.Reserve(ctx, "alice", "res-task", "task-1", 1_000, identified("1"))
	wantCode(t, err, ErrGuardBandNarrowed, "a task-sponsored attempt inside the band")

	if _, err := handle.Reserve(ctx, "alice", "res-unscoped", "", 1_000, identified("1")); err != nil {
		t.Fatalf("an unscoped attempt inside the band must remain possible: %v", err)
	}
}

// The band is a floor, not a permanent ceiling: capital above it restores the
// ordinary reserve rules.
func TestGuardBandReleasesAboveTheBand(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	banded(t, handle, "alice", 300_000) // 30% of initial capital: outside the band
	publishBandPolicy(t, handle)

	if _, err := handle.Reserve(ctx, "alice", "res-large", "", 300_000, identified("1")); err != nil {
		t.Fatalf("Reserve above the band: %v", err)
	}
}

// A schedule the action was not quoted under cannot widen it either: capital
// above the band restores ordinary rules regardless of the version quoted.
func TestGuardBandReleasesOnCapitalNotOnVersion(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	banded(t, handle, "alice", 300_000)
	publishBandPolicy(t, handle)

	if _, err := handle.Reserve(ctx, "alice", "res-above", "", 300_000, identified("9")); err != nil {
		t.Fatalf("Reserve above the band with an unpublished version: %v", err)
	}
}

func TestGuardBandExposureCapDefault(t *testing.T) {
	unspecified := Policy{InitialCapital: 1_000_000, GuardBandBP: 2_000}
	if got := unspecified.guardBandExposureCap(100_000); got != 25_000 {
		t.Fatalf("default cap = %d, want 25000", got)
	}
	// A tiny spendable balance never becomes unusable through rounding, and the
	// cap never exceeds what is actually spendable.
	if got := unspecified.guardBandExposureCap(3); got != 1 {
		t.Fatalf("cap of 3 spendable = %d, want 1", got)
	}
	if got := unspecified.guardBandExposureCap(0); got != 0 {
		t.Fatalf("cap of 0 spendable = %d, want 0", got)
	}
	full := Policy{GuardBandMaxExposureBP: 10_000}
	if got := full.guardBandExposureCap(100_000); got != 100_000 {
		t.Fatalf("cap at 10000 bp = %d, want the whole spendable balance", got)
	}
}

// Pain is a ledger fact, not a mood: it is derived from confirmed penalties and
// an outstanding shortfall, and it only falls when the operator clears the
// warnings.
func TestProjectionCarriesRecordedPain(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 1_000_000)
	publishPolicy(t, handle)

	clean, err := handle.Projection(ctx, "alice")
	if err != nil {
		t.Fatalf("Projection: %v", err)
	}
	if clean.Pain != 0 {
		t.Fatalf("pain with no penalties = %d, want 0", clean.Pain)
	}

	if _, err := handle.RecordLoss(ctx, "loss-1", "alice", 0, "rule_a", "c", "s", "n", "e", "1",
		WithDisposition(DispositionConfirmedBreach)); err != nil {
		t.Fatalf("RecordLoss: %v", err)
	}
	first, err := handle.Projection(ctx, "alice")
	if err != nil {
		t.Fatalf("Projection: %v", err)
	}
	if first.Pain != 3 {
		t.Fatalf("pain after one confirmed penalty = %d, want 3 (2 per penalty + 1 outstanding)", first.Pain)
	}

	if _, err := handle.RecordLoss(ctx, "loss-2", "alice", 0, "rule_a", "c", "s", "n", "e2", "1",
		WithDisposition(DispositionConfirmedBreach)); err != nil {
		t.Fatalf("RecordLoss: %v", err)
	}
	second, err := handle.Projection(ctx, "alice")
	if err != nil {
		t.Fatalf("Projection: %v", err)
	}
	if second.Pain != 5 {
		t.Fatalf("pain after two confirmed penalties = %d, want 5", second.Pain)
	}
}

// Recorded pain caps a single attempt everywhere, not only near zero. Two
// confirmed penalties put pain at 4/10, so at most 46% of spendable may be
// reserved in one attempt; pain 10 still leaves a tenth.
func TestRecordedPainCapsASingleAttempt(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 1_000_000)
	publishPolicy(t, handle)

	if _, err := handle.RecordLoss(ctx, "loss-1", "alice", 0, "rule_a", "c", "s", "n", "e", "1",
		WithDisposition(DispositionConfirmedBreach)); err != nil {
		t.Fatalf("RecordLoss: %v", err)
	}
	if _, err := handle.RecordLoss(ctx, "loss-2", "alice", 0, "rule_a", "c", "s", "n", "e2", "1",
		WithDisposition(DispositionConfirmedBreach)); err != nil {
		t.Fatalf("RecordLoss 2: %v", err)
	}
	projection, err := handle.Projection(ctx, "alice")
	if err != nil {
		t.Fatalf("Projection: %v", err)
	}
	if projection.Pain != 5 {
		t.Fatalf("pain=%d, want 5 after two confirmed penalties", projection.Pain)
	}

	// The cap derives from confirmed penalties: warnings 2 -> pain 4 -> 64% of
	// spendable per attempt. 70% is refused, 64% exactly is allowed.
	_, err = handle.Reserve(ctx, "alice", "res-over", "", 700_000, identified("1"))
	wantCode(t, err, ErrPainNarrowed, "an attempt above the pain cap")

	within, err := handle.Reserve(ctx, "alice", "res-within", "", 639_808, identified("1"))
	if err != nil {
		t.Fatalf("Reserve at the pain cap: %v", err)
	}
	if within.Reservation.Amount != 639_808 {
		t.Fatalf("reserved=%d, want 639808", within.Reservation.Amount)
	}
}

// drainThroughPainCap reserves and settles an identity down to target in
// single-attempt chunks, as metered work would. Returns when spendable is 0.
func drainThroughPainCap(ctx context.Context, handle *Ledger, identity string) error {
	for round := 0; ; round++ {
		account, found, err := loadAccount(ctx, handle.db, identity)
		if err != nil {
			return err
		}
		if !found {
			return errf(ErrUnknownIdentity, "%q", identity)
		}
		employment, err := loadEmployment(ctx, handle.db, identity)
		if err != nil {
			return err
		}
		state, err := unrecovered(ctx, handle.db, identity)
		if err != nil {
			return err
		}
		spendable := account.Spendable()
		if spendable <= 0 {
			return nil
		}
		pain := recordedPain(employment.Warnings, state.Outstanding)
		bp := int64(10_000)
		if pain > 0 {
			bp = int64(10-pain)*900 + 1_000
		}
		amount := spendable * bp / 10_000
		if amount > spendable {
			amount = spendable
		}
		if amount < 1 {
			amount = 1
		}
		r, err := handle.Reserve(ctx, identity,
			"drain-"+strconv.Itoa(round), "", amount)
		if err != nil {
			return err
		}
		if _, err := handle.Settle(ctx, r.Reservation.ID, amount, ClassCompleted, "sha256:drain"); err != nil {
			return err
		}
	}
}

// Reinstate clears the recorded pain by clearing the warnings that raised it.
// The penalty history itself stays: the loss events and any outstanding
// shortfall are untouched.
func TestReinstateClearsRecordedPainButNotHistory(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	fund(t, handle, "alice", 1_000_000)
	publishPolicy(t, handle)

	if _, err := handle.RecordLoss(ctx, "loss-1", "alice", 0, "rule_a", "c", "s", "n", "e", "1",
		WithDisposition(DispositionConfirmedBreach)); err != nil {
		t.Fatalf("RecordLoss: %v", err)
	}
	painful, err := handle.Projection(ctx, "alice")
	if err != nil {
		t.Fatalf("Projection: %v", err)
	}
	if painful.Pain != 3 {
		t.Fatalf("pain=%d, want 3 after one confirmed penalty", painful.Pain)
	}

	result, err := handle.SetRole(ctx, "alice", RoleActive, "rehabilitation complete", "evidence",
		"reinstate-alice", Reinstate())
	if err != nil {
		t.Fatalf("Reinstate: %v", err)
	}
	if result.Warnings != 0 {
		t.Fatalf("warnings after reinstate = %d, want 0", result.Warnings)
	}
	cleared, err := handle.Projection(ctx, "alice")
	if err != nil {
		t.Fatalf("Projection: %v", err)
	}
	if cleared.Pain != 1 {
		t.Fatalf("pain after reinstate = %d, want 1 (warnings cleared, shortfall stands)", cleared.Pain)
	}
	if cleared.LossBurned == 0 || cleared.Role != RoleActive {
		t.Fatalf("history must survive: burned=%d role=%s", cleared.LossBurned, cleared.Role)
	}
}

// A cap must never deny every priced tool while credit remains: with the shipped
// example schedule the cheapest call is 5000 mc, so an identity with less than
// that can still work once.
func TestCapsNeverBrickAnIdentity(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	banded(t, handle, "alice", 19_999) // inside the band, 25% cap = 4999
	publishBandPolicy(t, handle)

	// Without the floor the cheapest published call would be denied.
	if _, err := handle.Reserve(ctx, "alice", "res-cheapest", "", 5_000, identified("1")); err != nil {
		t.Fatalf("the cheapest published call must stay affordable inside the band: %v", err)
	}
	// The floor is a floor, not a ceiling: it does not lift a much larger refusal.
	_, err := handle.Reserve(ctx, "alice", "res-large", "", 15_000, identified("1"))
	if err == nil {
		t.Fatal("a large attempt is still refused inside the band")
	}
}

// The published body must carry the operating-price fields verbatim, so an
// adapter reading the schedule body prices actions exactly as published.
func TestPriceScheduleCarriesOperatingPrices(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	published := policy()
	published.DefaultMaxCostMC = 25_000
	published.MaxCostCapMC = 500_000
	published.ToolMaxCostMC = map[string]int64{"Bash": 50_000, "Read": 5_000}
	if err := handle.PublishPriceSchedule(ctx, "1", published); err != nil {
		t.Fatalf("PublishPriceSchedule: %v", err)
	}
	got, err := handle.PriceSchedule(ctx, "1")
	if err != nil {
		t.Fatalf("PriceSchedule: %v", err)
	}
	if got.DefaultMaxCostMC != 25_000 || got.MaxCostCapMC != 500_000 {
		t.Fatalf("bounds lost in publish/read: default=%d cap=%d", got.DefaultMaxCostMC, got.MaxCostCapMC)
	}
	if got.ToolMaxCostMC["Bash"] != 50_000 || got.ToolMaxCostMC["Read"] != 5_000 {
		t.Fatalf("per-tool prices lost in publish/read: %v", got.ToolMaxCostMC)
	}

	// Republishing the identical body is a no-op; a different body conflicts.
	if err := handle.PublishPriceSchedule(ctx, "1", published); err != nil {
		t.Fatalf("republish identical: %v", err)
	}
	changed := published
	changed.ToolMaxCostMC = map[string]int64{"Bash": 60_000}
	wantCode(t, handle.PublishPriceSchedule(ctx, "1", changed), ErrPayloadConflict, "republish different body")
}

// The ledger never invents prices, but it also never accepts a negative one.
func TestPriceScheduleRejectsNegativePrices(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	negative := policy()
	negative.DefaultMaxCostMC = -1
	wantCode(t, handle.PublishPriceSchedule(ctx, "1", negative), ErrInvalidArgument, "negative default_max_cost_mc")

	negative = policy()
	negative.ToolMaxCostMC = map[string]int64{"Bash": -5}
	wantCode(t, handle.PublishPriceSchedule(ctx, "1", negative), ErrInvalidArgument, "negative tool price")

	negative = policy()
	negative.ToolMaxCostMC = map[string]int64{" ": 5}
	wantCode(t, handle.PublishPriceSchedule(ctx, "1", negative), ErrInvalidArgument, "empty tool name")

	// All-zero operating prices stay publishable (no schedule is first-class).
	zero := policy()
	if err := handle.PublishPriceSchedule(ctx, "1", zero); err != nil {
		t.Fatalf("all-zero schedule must publish: %v", err)
	}
}
