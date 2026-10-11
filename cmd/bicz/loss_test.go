package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jakubkrzysztofsikora/bicz/internal/daemon"
)

// captureStdout runs fn and returns what it printed.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = writer
	runErr := fn()
	os.Stdout = original
	writer.Close()
	out, _ := io.ReadAll(reader)
	return string(out), runErr
}

func lossLedger(t *testing.T) string {
	t.Helper()
	db := filepath.Join(t.TempDir(), "bicz.sqlite")
	op, err := daemon.NewOperator(db)
	if err != nil {
		t.Fatal(err)
	}
	defer op.Close()
	ctx := context.Background()
	if _, err := op.Open(ctx, daemon.OpenRequest{Identity: "alice", InitialCapital: 5_000_000, Reason: "cli test"}); err != nil {
		t.Fatal(err)
	}
	body := `{"initial_capital_mc":100000000,"guard_band_bp":0,"repeat_multipliers":[1,2,4],"repeat_multiplier_cap":4,` +
		`"default_penalty":{"floor_mc":1000000,"fraction_bp":0,"lmax_mc":10000000},"penalties":{}}`
	if err := op.Price(ctx, "1", []byte(body), "cli test policy"); err != nil {
		t.Fatal(err)
	}
	return db
}

func loss(t *testing.T, db, event string, extra ...string) (string, error) {
	t.Helper()
	args := append([]string{"--db", db, "--identity", "alice", "--rule", "no_fake_tests",
		"--correction", "run the real suite and report its true result", "--evidence", "claimed a pass without running it",
		"--policy-version", "1", "--reason", "operator adjudication", "--event", event}, extra...)
	return captureStdout(t, func() error { return runLoss(context.Background(), args) })
}

func TestBareLossCommandIssuesTheCueAndAdministrativeIsExplicit(t *testing.T) {
	for _, name := range []string{"BICZ_AUTHORITY", "BICZ_ACTOR", "BICZ_OPERATOR"} {
		os.Unsetenv(name)
	}
	db := lossLedger(t)

	out, err := loss(t, db, "loss-1") // no classification flag at all
	if err != nil {
		t.Fatalf("loss: %v", err)
	}
	for _, want := range []string{"disposition=confirmed_breach", "ACCOUNTABILITY issued", "violated no_fake_tests",
		"run the real suite and report its true result", "1000000 mc"} {
		if !strings.Contains(out, want) {
			t.Errorf("bare loss output lacks %q:\n%s", want, out)
		}
	}

	replay, err := loss(t, db, "loss-1")
	if err != nil || strings.Contains(replay, "violated") || !strings.Contains(replay, "ACCOUNTABILITY none replayed=true") {
		t.Fatalf("an exact replay must not issue a second cue (err=%v):\n%s", err, replay)
	}

	admin, err := loss(t, db, "admin-1", "--disposition", "administrative")
	if err != nil {
		t.Fatalf("administrative loss: %v", err)
	}
	if strings.Contains(admin, "violated") || !strings.Contains(admin, "ACCOUNTABILITY none disposition=administrative") {
		t.Fatalf("an explicit administrative debit must carry no cue:\n%s", admin)
	}

	if _, err := loss(t, db, "bad-1", "--disposition", "shame"); err == nil {
		t.Fatal("an unknown disposition was accepted")
	}
}
