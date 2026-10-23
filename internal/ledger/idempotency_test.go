package ledger

import (
	"errors"
	"testing"
)

// A state-changing call replayed under the same key with a different
// effect-defining payload is a payload_conflict with the contract's stable code,
// never an untyped storage error an adapter cannot switch on
// (docs/CONTRACTS.md §2, §5.5).
func TestConflictingReplayIsATypedPayloadConflict(t *testing.T) {
	ctx := context0(t)

	cases := []struct {
		name  string
		first func(t *testing.T, handle *Ledger)
		again func(t *testing.T, handle *Ledger) error
	}{
		{
			name: "reward amount",
			first: func(t *testing.T, handle *Ledger) {
				if _, err := handle.RecordReward(ctx, "k", "alice", 100, "reason", "evidence", 0); err != nil {
					t.Fatalf("RecordReward: %v", err)
				}
			},
			again: func(t *testing.T, handle *Ledger) error {
				_, err := handle.RecordReward(ctx, "k", "alice", 999, "reason", "evidence", 0)
				return err
			},
		},
		{
			name: "role transition",
			first: func(t *testing.T, handle *Ledger) {
				if _, err := handle.SetRole(ctx, "alice", RoleWarned, "reason", "evidence", "k"); err != nil {
					t.Fatalf("SetRole: %v", err)
				}
			},
			again: func(t *testing.T, handle *Ledger) error {
				_, err := handle.SetRole(ctx, "alice", RoleSuspended, "reason", "evidence", "k")
				return err
			},
		},
		{
			name: "reset target",
			first: func(t *testing.T, handle *Ledger) {
				if _, err := handle.ResetBudget(ctx, "k", "alice", 5_000, "reason", "evidence"); err != nil {
					t.Fatalf("ResetBudget: %v", err)
				}
			},
			again: func(t *testing.T, handle *Ledger) error {
				_, err := handle.ResetBudget(ctx, "k", "alice", 9_000, "reason", "evidence")
				return err
			},
		},
		{
			// The key belongs to the lease that was opened. Reusing it for a
			// second lease used to surface as "UNIQUE constraint failed:
			// events.id", which no adapter can interpret.
			name: "lease identity",
			first: func(t *testing.T, handle *Ledger) {
				if _, err := handle.OpenTask(ctx, "k", "task-1", "alice", 1_000, "reason", "evidence"); err != nil {
					t.Fatalf("OpenTask: %v", err)
				}
			},
			again: func(t *testing.T, handle *Ledger) error {
				_, err := handle.OpenTask(ctx, "k", "task-2", "alice", 2_000, "reason", "evidence")
				return err
			},
		},
		{
			name: "lease sponsorship",
			first: func(t *testing.T, handle *Ledger) {
				if _, err := handle.OpenTask(ctx, "k", "task-1", "alice", 1_000, "reason", "evidence"); err != nil {
					t.Fatalf("OpenTask: %v", err)
				}
			},
			again: func(t *testing.T, handle *Ledger) error {
				_, err := handle.OpenTask(ctx, "k", "task-1", "alice", 5_000, "reason", "evidence")
				return err
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handle := newLedger(t)
			if _, err := handle.OpenAccount(ctx, "alice", WithInitialCapital(10_000)); err != nil {
				t.Fatalf("OpenAccount: %v", err)
			}
			tc.first(t, handle)
			before, _ := handle.ChainStats(ctx)

			err := tc.again(t, handle)
			if err == nil {
				t.Fatalf("a conflicting replay succeeded, want %s", ErrPayloadConflict.Code)
			}
			var ledgerErr *Error
			if !errors.As(err, &ledgerErr) {
				t.Fatalf("error %v is not a typed ledger error, so an adapter cannot switch on its code", err)
			}
			if ledgerErr.Code != ErrPayloadConflict.Code {
				t.Fatalf("code=%s, want %s", ledgerErr.Code, ErrPayloadConflict.Code)
			}
			after, _ := handle.ChainStats(ctx)
			if before.Seq != after.Seq {
				t.Fatalf("tip %d -> %d: a refused replay must append no event", before.Seq, after.Seq)
			}
		})
	}
}

// Revoking an already-revoked lease is a documented no-op, not a conflict: it
// writes nothing and returns the lease as it stands (docs/CONTRACTS.md §4).
func TestRevokingARevokedLeaseIsANoOp(t *testing.T) {
	ctx := context0(t)
	handle := newLedger(t)
	if _, err := handle.OpenAccount(ctx, "alice", WithInitialCapital(10_000)); err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	if _, err := handle.OpenTask(ctx, "task-open", "task-1", "alice", 1_000, "reason", "evidence"); err != nil {
		t.Fatalf("OpenTask: %v", err)
	}
	if _, err := handle.RevokeTask(ctx, "revoke-1", "task-1", "reason", "evidence"); err != nil {
		t.Fatalf("RevokeTask: %v", err)
	}
	before, _ := handle.ChainStats(ctx)

	task, err := handle.RevokeTask(ctx, "revoke-2", "task-1", "another reason", "other evidence")
	if err != nil {
		t.Fatalf("second RevokeTask: %v", err)
	}
	if task.State != TaskRevoked {
		t.Fatalf("state=%s, want %s", task.State, TaskRevoked)
	}
	after, _ := handle.ChainStats(ctx)
	if before.Seq != after.Seq {
		t.Fatalf("tip %d -> %d: a no-op revoke must append no event", before.Seq, after.Seq)
	}
}
