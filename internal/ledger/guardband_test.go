package ledger

import (
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

	// A call that quotes no schedule is not narrowed: the band is a property of
	// the published version, so it cannot be evaluated and nothing is assumed.
	if _, err := handle.Reserve(ctx, "alice", "res-unquoted-2", "", 1_000); err != nil {
		t.Fatalf("an unquoted attempt must not be narrowed by an assumed band: %v", err)
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

// A schedule the action was not quoted under cannot narrow it: the band is a
// property of the published version, not of the caller's absence of one.
func TestGuardBandAppliesOnlyToAQuotedSchedule(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	banded(t, handle, "alice", 100_000)
	publishBandPolicy(t, handle)

	if _, err := handle.Reserve(ctx, "alice", "res-unquoted", "", 100_000, identified("9")); err != nil {
		t.Fatalf("Reserve under an unpublished version: %v", err)
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
