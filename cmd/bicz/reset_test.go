package main

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestResetCLIUnblocksInsolventIdentityWithoutTouchingOutstandingLoss(t *testing.T) {
	for _, name := range []string{"BICZ_AUTHORITY", "BICZ_ACTOR", "BICZ_OPERATOR"} {
		os.Unsetenv(name)
	}
	db := lossLedger(t)

	// Incur a loss of 1,000,000 mc. Alice's initial balance was 5,000,000 mc.
	// Balance becomes 4,000,000 mc, outstanding loss is 1,000,000 mc.
	_, err := loss(t, db, "loss-setup")
	if err != nil {
		t.Fatalf("loss setup: %v", err)
	}

	// Incur losses to deplete Alice's balance to 0.
	// Initial capital was 5,000,000 mc.
	// Loss 1: applied 1,000,000 mc -> balance 4,000,000 mc, outstanding 1,000,000 mc.
	// Loss 2: multiplier 2 -> applied 2,000,000 mc -> balance 2,000,000 mc, outstanding 3,000,000 mc.
	// Loss 3: multiplier 4 -> formula 4,000,000 mc -> applied 2,000,000 mc (all available) -> balance 0,
	//         outstanding 5,000,000 mc (shortfall 2,000,000 mc).
	_, err = loss(t, db, "loss-3")
	if err != nil {
		t.Fatalf("loss 3: %v", err)
	}
	_, err = loss(t, db, "loss-4")
	if err != nil {
		t.Fatalf("loss 4: %v", err)
	}

	// Verify Alice is at balance 0 and has outstanding loss.
	statusOut, err := captureStdout(t, func() error {
		return runStatus(context.Background(), []string{"--db", db, "--identity", "alice"})
	})
	if err != nil {
		t.Fatalf("runStatus: %v", err)
	}
	if !strings.Contains(statusOut, `"balance": 0`) {
		t.Fatalf("expected balance 0, got:\n%s", statusOut)
	}

	// Run reset CLI: resets balance to default baseline (100 credits = 100_000_000 mc).
	resetOut, err := captureStdout(t, func() error {
		return runReset(context.Background(), []string{
			"--db", db,
			"--identity", "alice",
			"--reason", "unblocking stuck agent",
			"--event", "reset-1",
		})
	})
	if err != nil {
		t.Fatalf("runReset: %v", err)
	}

	for _, want := range []string{"RESET event=reset-1", "account=alice", "delta=100000000", "balance_before=0", "balance_after=100000000", "replayed=false"} {
		if !strings.Contains(resetOut, want) {
			t.Errorf("reset output lacks %q:\n%s", want, resetOut)
		}
	}

	// Replay must be idempotent and add 0 delta.
	replayOut, err := captureStdout(t, func() error {
		return runReset(context.Background(), []string{
			"--db", db,
			"--identity", "alice",
			"--reason", "unblocking stuck agent",
			"--event", "reset-1",
		})
	})
	if err != nil {
		t.Fatalf("runReset replay: %v", err)
	}
	if !strings.Contains(replayOut, "replayed=true") {
		t.Fatalf("replay must have replayed=true:\n%s", replayOut)
	}

	// Status check: outstanding loss must still be intact (never reduced by reset).
	statusAfter, err := captureStdout(t, func() error {
		return runStatus(context.Background(), []string{"--db", db, "--identity", "alice"})
	})
	if err != nil {
		t.Fatalf("runStatus after: %v", err)
	}
	if !strings.Contains(statusAfter, `"balance": 100000000`) {
		t.Fatalf("expected balance 100000000 mc (100 credits), got:\n%s", statusAfter)
	}
	if !strings.Contains(statusAfter, `"outstanding_loss": 5000000`) {
		t.Fatalf("outstanding loss must remain untouched (5000000 mc), got:\n%s", statusAfter)
	}
}

func TestReviewSHIPVerdictGrantsAutoreward(t *testing.T) {
	for _, name := range []string{"BICZ_AUTHORITY", "BICZ_ACTOR", "BICZ_OPERATOR"} {
		os.Unsetenv(name)
	}
	db := lossLedger(t)

	// Alice initial balance is 5 credits (5_000_000 mc), well below baseline ceiling (100 credits).
	out, err := captureStdout(t, func() error {
		return runReview(context.Background(), []string{
			"--db", db,
			"--identity", "alice",
			"--verdict", "SHIP",
			"--reason", "adversarial review passed with ship verdict",
			"--event", "rev-ship-1",
		})
	})
	if err != nil {
		t.Fatalf("runReview: %v", err)
	}

	for _, want := range []string{`verdict="SHIP"`, "autoreward=1000000", "replayed=false"} {
		if !strings.Contains(out, want) {
			t.Errorf("runReview SHIP output lacks %q:\n%s", want, out)
		}
	}
}
