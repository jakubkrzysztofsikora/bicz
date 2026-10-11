package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Account is an identity's durable capital position.
type Account struct {
	ID        string
	CreatedAt string
	Balance   int64 // earned capital K, microcredits
	Held      int64 // sum of live reservation holds H, microcredits
}

// Spendable is balance − held: what a new reservation may draw on before any
// task sponsorship is added (docs/CONTRACTS.md §1).
func (a Account) Spendable() int64 { return a.Balance - a.Held }

// Employment is the role state, deliberately separate from solvency.
type Employment struct {
	State    string
	Warnings int64
}

// Role states, fixed by the employment.state CHECK in schema/001_init.sql.
const (
	RoleActive    = "active"
	RoleWarned    = "warned"
	RoleProbation = "probation"
	RoleSuspended = "suspended"
	RoleDismissed = "dismissed"
)

// RoleStates is the closed set of employment states.
var RoleStates = []string{RoleActive, RoleWarned, RoleProbation, RoleSuspended, RoleDismissed}

// ValidRole reports whether state is one of the five closed role states.
func ValidRole(state string) bool {
	for _, candidate := range RoleStates {
		if candidate == state {
			return true
		}
	}
	return false
}

type accountConfig struct {
	initialCapital int64
}

// AccountOption tunes OpenAccount.
type AccountOption func(*accountConfig)

// WithInitialCapital provisions the identity with earned capital in the same
// transaction, recorded as one `grant` event (docs/CONTRACTS.md §4, "operator
// grant"). It is the only way credit enters a new identity.
func WithInitialCapital(microcredits int64) AccountOption {
	return func(cfg *accountConfig) { cfg.initialCapital = microcredits }
}

// OpenAccount provisions identity id with a zero-by-default balance and an
// `active` employment row.
//
// It is idempotent: opening an identity that already exists returns the existing
// position and writes nothing, so a retried provisioning call cannot grant
// twice.
func (l *Ledger) OpenAccount(ctx context.Context, id string, opts ...AccountOption) (Account, error) {
	if id == "" {
		return Account{}, errf(ErrInvalidArgument, "account id is empty")
	}
	cfg := accountConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.initialCapital < 0 {
		return Account{}, errf(ErrInvalidAmount, "initial capital %d", cfg.initialCapital)
	}
	var account Account
	err := l.mutate(ctx, func(tx *sql.Tx) error {
		existing, found, err := loadAccount(ctx, tx, id)
		if err != nil {
			return err
		}
		if found {
			account = existing
			return nil
		}
		at := l.clock.Timestamp()
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO accounts (id, created_at, balance, held) VALUES (?, ?, ?, 0)`,
			id, at, cfg.initialCapital); err != nil {
			return fmt.Errorf("ledger: create account %s: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO employment (account_id, state, warnings) VALUES (?, ?, 0)`,
			id, RoleActive); err != nil {
			return fmt.Errorf("ledger: create employment %s: %w", id, err)
		}
		_, _, err = appendEvent(ctx, tx, l.clock, eventSpec{
			id:        "grant:" + id,
			accountID: &id,
			kind:      kindGrant,
			amount:    cfg.initialCapital,
			payload: grantPayload{
				Phase:          "provision",
				AccountID:      id,
				InitialCapital: cfg.initialCapital,
			},
			actor:  l.actor,
			reason: "provision identity",
		})
		if err != nil {
			return err
		}
		account = Account{ID: id, CreatedAt: at, Balance: cfg.initialCapital, Held: 0}
		return nil
	})
	if err != nil {
		return Account{}, err
	}
	return account, nil
}

// GetAccount reads an identity's capital position.
func (l *Ledger) GetAccount(ctx context.Context, id string) (Account, error) {
	account, found, err := loadAccount(ctx, l.db, id)
	if err != nil {
		return Account{}, err
	}
	if !found {
		return Account{}, errf(ErrUnknownIdentity, "%q", id)
	}
	return account, nil
}

func loadAccount(ctx context.Context, q querier, id string) (Account, bool, error) {
	row := q.QueryRowContext(ctx, `SELECT id, created_at, balance, held FROM accounts WHERE id = ?`, id)
	var account Account
	err := row.Scan(&account.ID, &account.CreatedAt, &account.Balance, &account.Held)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, false, nil
	}
	if err != nil {
		return Account{}, false, fmt.Errorf("ledger: load account %s: %w", id, err)
	}
	return account, true, nil
}

func loadEmployment(ctx context.Context, q querier, accountID string) (Employment, error) {
	row := q.QueryRowContext(ctx, `SELECT state, warnings FROM employment WHERE account_id = ?`, accountID)
	var employment Employment
	err := row.Scan(&employment.State, &employment.Warnings)
	if errors.Is(err, sql.ErrNoRows) {
		return Employment{}, errf(ErrUnknownIdentity, "%q has no employment row", accountID)
	}
	if err != nil {
		return Employment{}, fmt.Errorf("ledger: load employment %s: %w", accountID, err)
	}
	return employment, nil
}

func setEmployment(ctx context.Context, tx *sql.Tx, accountID string, employment Employment) error {
	result, err := tx.ExecContext(ctx,
		`UPDATE employment SET state = ?, warnings = ? WHERE account_id = ?`,
		employment.State, employment.Warnings, accountID)
	if err != nil {
		return fmt.Errorf("ledger: update employment %s: %w", accountID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("ledger: update employment %s: %w", accountID, err)
	}
	if affected != 1 {
		return errf(ErrUnknownIdentity, "%q has no employment row", accountID)
	}
	return nil
}

// gateRole refuses ordinary execution for a suspended or dismissed identity.
// Bankruptcy is a separate, capital-side check (docs/CONTRACTS.md §1 I7).
func gateRole(accountID string, employment Employment) error {
	switch employment.State {
	case RoleSuspended:
		return errf(ErrSuspended, "%q is suspended", accountID)
	case RoleDismissed:
		return errf(ErrDismissed, "%q is dismissed", accountID)
	default:
		return nil
	}
}
