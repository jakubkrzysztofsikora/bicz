// Package ledger implements the SQL-side core of bicz: the durable,
// append-only action-credit ledger specified by docs/CONTRACTS.md and stored by
// schema/001_init.sql.
//
// Design rules that hold everywhere in this package:
//
//   - Money is an integer count of microcredits (MicrocreditsPerCredit scales to
//     credits). No float appears anywhere on a money path.
//   - Every mutation runs inside one transaction opened with BEGIN IMMEDIATE
//     (see the DSN in dsn()): the write lock is taken before the first read, so
//     two concurrent mutations serialize instead of interleaving their reads.
//   - Every mutation appends at least one `events` row carrying the actor, a
//     non-empty reason, and the timestamp, with prev_digest/payload_digest
//     computed through internal/canonical. A call that performs several distinct
//     state changes (settle + role escalation + hold revocation) appends one
//     event per change, all in the same transaction.
//   - Idempotency is keyed on a primary key: `reservations.id` for reservations,
//     `events.id` for ledger events and role transitions. Same key + same
//     payload is a no-op returning the prior result; same key + different
//     payload is ErrPayloadConflict and nothing is written. (docs/CONTRACTS.md §2)
//   - History is append-only. Corrections are new `compensation` events; an
//     original event, its balance effect, and its lesson are never edited or
//     deleted. (docs/CONTRACTS.md §1 I11)
//
// Which invariants live where:
//
//	I1 balance >= 0, I2 0 <= held <= balance  -> SQLite CHECK constraints in
//	    schema/001_init.sql. They are unrepresentable states, so the writes fail
//	    loudly instead of clamping. The Go side additionally pre-checks in the
//	    reserve/settle/burn paths so the ordinary failure is a typed error rather
//	    than a constraint violation.
//	I3, I9  a reservation is created only when amount <= balance - held, inside
//	    BEGIN IMMEDIATE -> Go (Reserve).
//	I4  dismissal is terminal without operator reinstatement -> Go (SetRole).
//	I5, I6  no sponsorship rollup, no auto-replenishment -> Go: no code path in
//	    this package mutates tasks.sponsor or grants credit outside an explicit
//	    operator event.
//	I7  bankruptcy != dismissal -> Go: the capital gate and the role gate are two
//	    separate checks in Reserve.
//	I8  unresolved running work retains its hold -> Go: only 'held' reservations
//	    are ever released; 'orphaned' holds are retained by CancelReservation,
//	    RevokeAllHeld, and the loss revocation path.
//	I10 exact replay idempotent, conflicting replay fatal -> Go + the events.id /
//	    reservations.id primary keys.
//	I11 compensation never resets role or history -> Go + SQLite CHECK on
//	    employment.state.
package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/user"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver; no cgo.

	"github.com/jakubkrzysztofsikora/bicz/internal/canonical"
	"github.com/jakubkrzysztofsikora/bicz/schema"
)

const (
	// MicrocreditsPerCredit is the frozen unit conversion: 1 credit is
	// 1,000,000 microcredits. Every stored amount is microcredits.
	MicrocreditsPerCredit int64 = 1_000_000

	// ActorSystem is the recorded actor of trusted in-process meters (reserve,
	// settle, cancel). Operator events record the local OS user instead.
	ActorSystem = "system"
)

// Ledger is a handle on one bicz database.
type Ledger struct {
	db    *sql.DB
	path  string
	clock canonical.Clock
	actor string
}

// Open opens (creating if needed) the ledger at path, enables foreign keys on
// every connection, and applies all pending forward-only migrations.
//
// The recorded operator actor is the local OS user (docs/CONTRACTS.md §4):
// authority is derived from the invoking user, never supplied by a caller, so
// no harness message or adapter request can forge an operator action.
func Open(path string) (*Ledger, error) {
	return open(context.Background(), path, canonical.System)
}

// OpenWithClock is Open with an injected clock. Timestamps and therefore every
// digest become deterministic; tests use it, production does not.
func OpenWithClock(path string, clock canonical.Clock) (*Ledger, error) {
	return open(context.Background(), path, clock)
}

func open(ctx context.Context, path string, clock canonical.Clock) (*Ledger, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errf(ErrInvalidArgument, "database path is empty")
	}
	if clock == nil {
		clock = canonical.System
	}
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("ledger: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ledger: open %s: %w", path, err)
	}
	actor, err := operatorActor()
	if err != nil {
		db.Close()
		return nil, err
	}
	if err := migrateDB(ctx, db, path); err != nil {
		db.Close()
		return nil, err
	}
	return &Ledger{db: db, path: path, clock: clock, actor: actor}, nil
}

// Close releases the database handle.
func (l *Ledger) Close() error { return l.db.Close() }

// DB exposes the underlying handle for read-only inspection and for the CLI's
// chain verification. Callers must not use it to mutate ledger state.
func (l *Ledger) DB() *sql.DB { return l.db }

// Path is the database path this handle was opened with.
func (l *Ledger) Path() string { return l.path }

// Actor is the recorded operator authority: the local OS user.
func (l *Ledger) Actor() string { return l.actor }

// Clock is the injected time source.
func (l *Ledger) Clock() canonical.Clock { return l.clock }

// dsn builds the driver DSN. The pragmas are per-connection and are the only
// reason this package can rely on foreign-key enforcement and on write-lock
// ordering:
//
//   - foreign_keys(1)  REFERENCES clauses are enforced (SQLite defaults to off).
//   - busy_timeout(10s) a concurrent BEGIN IMMEDIATE waits instead of failing.
//   - journal_mode(WAL) readers do not block the single writer.
//   - _txlock=immediate every database/sql transaction issues BEGIN IMMEDIATE,
//     which is how every mutation takes its write lock before reading.
func dsn(path string) string {
	escaped := strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23").Replace(path)
	return "file:" + escaped +
		"?_pragma=foreign_keys(1)" +
		"&_pragma=busy_timeout(10000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_txlock=immediate"
}

// operatorActor resolves the operator identity from the invoking OS user. It is
// deliberately not a parameter anywhere in this package.
func operatorActor() (string, error) {
	if current, err := user.Current(); err == nil && current.Username != "" {
		return current.Username, nil
	}
	for _, key := range []string{"USER", "LOGNAME"} {
		if value := os.Getenv(key); value != "" {
			return value, nil
		}
	}
	return "", errf(ErrInvalidArgument, "cannot resolve the local OS user for operator authority")
}

// ErrCommitOutcomeUnknown marks a failed COMMIT. Every earlier failure (begin,
// busy timeout, a cancelled or expired context, any error from the mutation
// body) rolls the transaction back, so those are definite "nothing written".
// A commit that returned an error may or may not have reached disk, so callers
// must say so instead of claiming nothing was recorded.
var ErrCommitOutcomeUnknown = errors.New("ledger: commit outcome unknown")

// mutate runs fn inside a single transaction and commits it. A non-nil error
// from fn rolls the whole thing back, so no partial effect is ever visible.
func (l *Ledger) mutate(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("ledger: begin: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("ledger: commit: %w: %w", ErrCommitOutcomeUnknown, err)
	}
	return nil
}

// Migrate applies every pending forward-only migration to db.
//
// Each file body plus its schema_version record is one transaction: a failure
// rolls back the DDL and the version row together, so the database is never left
// half-migrated. An already-applied version is never re-applied, and a database
// whose highest version exceeds the binary's highest known migration is refused
// outright rather than guessed at (docs/SCHEMA.md "Migration policy").
//
// Open already does this, with a pre-migration snapshot; Migrate is for a caller
// that holds its own handle.
func Migrate(db *sql.DB) error {
	return migrateDB(context.Background(), db, "")
}

func migrateDB(ctx context.Context, db *sql.DB, dbPath string) error {
	applied, err := highestApplied(ctx, db)
	if err != nil {
		return err
	}
	migrations, err := schema.Migrations()
	if err != nil {
		return err
	}
	var known int
	if len(migrations) > 0 {
		known = migrations[len(migrations)-1].Version
	}
	if applied > int64(known) {
		return errf(ErrSchemaTooNew,
			"database is at schema version %d but this binary knows at most %d; upgrade bicz",
			applied, known)
	}
	pending := make([]schema.Migration, 0, len(migrations))
	for _, migration := range migrations {
		if int64(migration.Version) > applied {
			pending = append(pending, migration)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	backup := ""
	if dbPath != "" && applied > 0 {
		backup, err = snapshotBeforeMigrate(ctx, db, dbPath)
		if err != nil {
			return err
		}
	}
	for _, migration := range pending {
		if err := applyMigration(ctx, db, migration); err != nil {
			if backup != "" {
				return fmt.Errorf("%w (pre-migration backup: %s)", err, backup)
			}
			return err
		}
	}
	return nil
}

// highestApplied returns the highest applied migration version, or 0 when the
// bookkeeping table does not exist yet (a fresh database). The table is created
// by 001_init.sql itself, so the runner never pre-creates it and can never
// collide with the migration that owns it.
func highestApplied(ctx context.Context, db *sql.DB) (int64, error) {
	var exists int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_version'`).Scan(&exists); err != nil {
		return 0, fmt.Errorf("ledger: inspect schema_version: %w", err)
	}
	if exists == 0 {
		return 0, nil
	}
	var applied int64
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&applied); err != nil {
		return 0, fmt.Errorf("ledger: read schema_version: %w", err)
	}
	return applied, nil
}

// snapshotBeforeMigrate copies the database with VACUUM INTO, which is
// consistent without racing a concurrent writer.
func snapshotBeforeMigrate(ctx context.Context, db *sql.DB, dbPath string) (string, error) {
	destination := fmt.Sprintf("%s.backup-%s", dbPath, time.Now().UTC().Format("20060102T150405Z"))
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, destination); err != nil {
		return "", fmt.Errorf("ledger: pre-migration backup of %s: %w", dbPath, err)
	}
	return destination, nil
}

func applyMigration(ctx context.Context, db *sql.DB, migration schema.Migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("ledger: begin migration %s: %w", migration.Name, err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after a failed commit is a no-op
	// PRAGMA foreign_keys is a no-op inside a transaction, so it is set on the
	// connection by the DSN before this BEGIN, never from the file body.
	if _, err := tx.ExecContext(ctx, migration.SQL); err != nil {
		return fmt.Errorf("ledger: apply %s: %w", migration.Name, err)
	}
	var recorded int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_version WHERE version = ?`, migration.Version).Scan(&recorded); err != nil {
		return fmt.Errorf("ledger: read schema_version after %s: %w", migration.Name, err)
	}
	if recorded == 0 {
		// Migration files are self-contained and record themselves; this insert
		// keeps a future file that forgets to do so from being applied twice.
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_version (version, applied_at) VALUES (?, ?)`,
			migration.Version, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("ledger: record %s: %w", migration.Name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("ledger: commit %s: %w", migration.Name, err)
	}
	return nil
}
