package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Task is a task lease and its task-scoped sponsorship G.
//
// Sponsorship is quoted to exactly one task: it cannot be transferred to another
// task and does not accumulate across tasks (docs/CONTRACTS.md §1 I5), so nothing
// in this package ever adds a task's sponsor to another task or to a balance.
type Task struct {
	ID        string
	AccountID string
	State     string
	Sponsor   int64
	CreatedAt string
}

// Task lease states, fixed by the tasks.state CHECK in schema/001_init.sql.
const (
	TaskOpen    = "open"
	TaskDone    = "done"
	TaskRevoked = "revoked"
)

// OpenTask creates a task lease owned by accountID with sponsorship sponsor.
//
// It is idempotent on taskID: re-opening an existing lease returns it unchanged
// and writes nothing, so a retried lease cannot duplicate sponsorship.
func (l *Ledger) OpenTask(ctx context.Context, eventID, taskID, accountID string, sponsor int64, reason, evidence string) (Task, error) {
	if taskID == "" {
		return Task{}, errf(ErrInvalidArgument, "task id is empty")
	}
	if accountID == "" {
		return Task{}, errf(ErrUnknownIdentity, "task account id is empty")
	}
	if sponsor < 0 {
		return Task{}, errf(ErrInvalidAmount, "sponsorship %d must not be negative", sponsor)
	}
	if reason == "" {
		return Task{}, errf(ErrReasonRequired, "lease %q", taskID)
	}
	if eventID == "" {
		eventID = "task:open:" + taskID
	}
	var task Task
	err := l.mutate(ctx, func(tx *sql.Tx) error {
		existing, found, err := loadTask(ctx, tx, taskID)
		if err != nil {
			return err
		}
		if found {
			if existing.AccountID != accountID {
				return errf(ErrPayloadConflict, "task %q belongs to %q", taskID, existing.AccountID)
			}
			task = existing
			return nil
		}
		if _, found, err := loadAccount(ctx, tx, accountID); err != nil {
			return err
		} else if !found {
			return errf(ErrUnknownIdentity, "%q", accountID)
		}
		at := l.clock.Timestamp()
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO tasks (id, account_id, state, sponsor, created_at) VALUES (?, ?, ?, ?, ?)`,
			taskID, accountID, TaskOpen, sponsor, at); err != nil {
			return fmt.Errorf("ledger: insert task %s: %w", taskID, err)
		}
		if _, _, err := appendEvent(ctx, tx, l.clock, eventSpec{
			id:        eventID,
			accountID: &accountID,
			kind:      kindTask,
			amount:    0,
			payload:   taskPayload{Phase: "open", TaskID: taskID, AccountID: accountID, State: TaskOpen, Sponsor: sponsor},
			actor:     l.actor,
			reason:    reason + ": " + evidence,
		}); err != nil {
			return err
		}
		task = Task{ID: taskID, AccountID: accountID, State: TaskOpen, Sponsor: sponsor, CreatedAt: at}
		return nil
	})
	if err != nil {
		return Task{}, err
	}
	return task, nil
}

// RevokeTask revokes a task lease, which denies further reservations against it
// while leaving the identity's role and balance untouched (revocation is a
// scoped capability limit, not a dismissal). Replaying the same eventID on an
// already-revoked lease is a no-op.
func (l *Ledger) RevokeTask(ctx context.Context, eventID, taskID, reason, evidence string) (Task, error) {
	if taskID == "" {
		return Task{}, errf(ErrInvalidArgument, "task id is empty")
	}
	if reason == "" {
		return Task{}, errf(ErrReasonRequired, "revoke of task %q", taskID)
	}
	if eventID == "" {
		eventID = "task:revoke:" + taskID
	}
	var task Task
	err := l.mutate(ctx, func(tx *sql.Tx) error {
		existing, found, err := loadTask(ctx, tx, taskID)
		if err != nil {
			return err
		}
		if !found {
			return errf(ErrUnknownTask, "%q", taskID)
		}
		if existing.State == TaskRevoked {
			task = existing
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET state = ? WHERE id = ?`, TaskRevoked, taskID); err != nil {
			return fmt.Errorf("ledger: revoke task %s: %w", taskID, err)
		}
		if _, _, err := appendEvent(ctx, tx, l.clock, eventSpec{
			id:        eventID,
			accountID: &existing.AccountID,
			kind:      kindTask,
			amount:    0,
			payload:   taskPayload{Phase: "revoke", TaskID: taskID, AccountID: existing.AccountID, State: TaskRevoked, Sponsor: existing.Sponsor},
			actor:     l.actor,
			reason:    reason + ": " + evidence,
		}); err != nil {
			return err
		}
		existing.State = TaskRevoked
		task = existing
		return nil
	})
	if err != nil {
		return Task{}, err
	}
	return task, nil
}

// Task reads one lease.
func (l *Ledger) Task(ctx context.Context, taskID string) (Task, error) {
	task, found, err := loadTask(ctx, l.db, taskID)
	if err != nil {
		return Task{}, err
	}
	if !found {
		return Task{}, errf(ErrUnknownTask, "%q", taskID)
	}
	return task, nil
}

func loadTask(ctx context.Context, q querier, taskID string) (Task, bool, error) {
	row := q.QueryRowContext(ctx, `SELECT id, account_id, state, sponsor, created_at FROM tasks WHERE id = ?`, taskID)
	var task Task
	err := row.Scan(&task.ID, &task.AccountID, &task.State, &task.Sponsor, &task.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, false, nil
	}
	if err != nil {
		return Task{}, false, fmt.Errorf("ledger: load task %s: %w", taskID, err)
	}
	return task, true, nil
}
