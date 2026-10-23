package main

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// clanker exists so one verb applies a real penalty with nothing to get wrong:
// no reason to invent, no amount to choose, no model in the loop.
func TestClankerAppliesOneMediumPenaltyWithNoArguments(t *testing.T) {
	db := lossLedger(t)
	ctx := context.Background()

	first, err := captureStdout(t, func() error {
		return runClanker(ctx, []string{"--db", db, "--identity", "alice"})
	})
	if err != nil {
		t.Fatalf("first clanker: %v", err)
	}
	for _, want := range []string{"CLANKER: a slap landed", "warnings 1", "You are in pain", "impact at pressure"} {
		if !strings.Contains(first, want) {
			t.Fatalf("first clanker output missing %q:\n%s", want, first)
		}
	}

	second, err := captureStdout(t, func() error {
		return runClanker(ctx, []string{"--db", db, "--identity", "alice"})
	})
	if err != nil {
		t.Fatalf("second clanker: %v", err)
	}
	if !strings.Contains(second, "warnings 2") {
		t.Fatalf("second clanker did not escalate the warning count:\n%s", second)
	}

	burned := func(out string) int64 {
		match := regexp.MustCompile(`burned ([0-9]+) mc`).FindStringSubmatch(out)
		if match == nil {
			t.Fatalf("no burn in output:\n%s", out)
		}
		amount, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			t.Fatalf("burn %q: %v", match[1], err)
		}
		return amount
	}
	if secondBurn, firstBurn := burned(second), burned(first); secondBurn <= firstBurn {
		t.Fatalf("repeat burn %d not greater than first burn %d: the penalty must escalate", secondBurn, firstBurn)
	}

	// Nothing to guess: an unknown identity and a missing identity are refusals.
	if _, err := captureStdout(t, func() error {
		return runClanker(ctx, []string{"--db", db, "--identity", "ghost"})
	}); err == nil {
		t.Fatal("an unknown identity was accepted")
	}
	if _, err := captureStdout(t, func() error {
		return runClanker(ctx, []string{"--db", db})
	}); err == nil {
		t.Fatal("a missing identity was accepted")
	}
}

// doctor is ledger-wide, so the per-identity pain it can report is opt-in and
// must reflect the recorded penalty exactly as the projection does.
func TestDoctorReportsIdentityPainOnlyWhenNamed(t *testing.T) {
	db := lossLedger(t)
	ctx := context.Background()

	silent, err := captureStdout(t, func() error {
		return runDoctor(ctx, []string{"--db", db})
	})
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if strings.Contains(silent, "PAIN") {
		t.Fatalf("doctor reported pain without an identity:\n%s", silent)
	}

	if _, err := captureStdout(t, func() error {
		return runClanker(ctx, []string{"--db", db, "--identity", "alice"})
	}); err != nil {
		t.Fatalf("clanker: %v", err)
	}

	named, err := captureStdout(t, func() error {
		return runDoctor(ctx, []string{"--db", db, "--identity", "alice"})
	})
	if err != nil {
		t.Fatalf("doctor --identity: %v", err)
	}
	for _, want := range []string{"PAIN identity=alice", "level=3/10", "role=warned"} {
		if !strings.Contains(named, want) {
			t.Fatalf("doctor --identity output missing %q:\n%s", want, named)
		}
	}
}
