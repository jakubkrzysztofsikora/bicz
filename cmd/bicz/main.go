// Command bicz is the operator CLI and supervisor front end.
//
// It is the operator authority of docs/CONTRACTS.md §4: a real human at the
// terminal. Every state-changing command records who (the local OS user,
// resolved by the ledger, never a flag), when, and a mandatory non-empty
// --reason. There is deliberately no --authority flag and no way to supply one;
// setting BICZ_AUTHORITY, BICZ_ACTOR, or BICZ_OPERATOR makes every
// state-changing command fail with untrusted_authority.
//
// Read-only commands (status, verify, doctor, lessons) change nothing and need
// no reason.
//
// What this tool does not claim, in any output: no sandbox, no containment, no
// anti-tamper guarantee, no provider metering, no cryptographic operator
// authentication, and no enforcement beyond cooperative same-user observation.
// `doctor` prints that list explicitly.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/jakubkrzysztofsikora/bicz/internal/daemon"
	"github.com/jakubkrzysztofsikora/bicz/internal/hooks"
	"github.com/jakubkrzysztofsikora/bicz/internal/install"
	"github.com/jakubkrzysztofsikora/bicz/internal/ledger"
	"github.com/jakubkrzysztofsikora/bicz/internal/pressure"
	"github.com/jakubkrzysztofsikora/bicz/internal/rehearsal"
	"github.com/jakubkrzysztofsikora/bicz/internal/selfupdate"
	"github.com/jakubkrzysztofsikora/bicz/internal/slash"
)

const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(exitUsage)
	}
	ctx := context.Background()
	commands := map[string]func(context.Context, []string) error{
		"serve":        runServe,
		"init":         runInit,
		"open":         runOpen,
		"reward":       runReward,
		"reset":        runReset,
		"loss":         runLoss,
		"clanker":      runClanker,
		"appeal":       runAppeal,
		"review":       runReview,
		"reinstate":    runReinstate,
		"price":        runPrice,
		"pressure":     runPressure,
		"status":       runStatus,
		"verify":       runVerify,
		"anchor":       runAnchor,
		"doctor":       runDoctor,
		"lessons":      runLessons,
		"rules":        runRules,
		"install":      runInstall,
		"uninstall":    runUninstall,
		"update":       runUpdate,
		"hook":         runHook,
		"command":      runCommand,
		"version":      runVersion,
		"rehearsal":    runRehearsal,
		"release-gate": runReleaseGate,
	}
	switch os.Args[1] {
	case "help", "-h", "--help":
		usage(os.Stdout)
		return
	}
	run, ok := commands[os.Args[1]]
	if !ok {
		fmt.Fprintf(os.Stderr, "bicz: unknown command %q\n", os.Args[1])
		usage(os.Stderr)
		os.Exit(exitUsage)
	}
	if err := run(ctx, os.Args[2:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(exitOK)
		}
		var coded exitCodeError
		if errors.As(err, &coded) {
			os.Exit(int(coded))
		}
		fmt.Fprintf(os.Stderr, "bicz: %v\n", err)
		os.Exit(exitError)
	}
}

// exitCodeError makes a command exit with a specific status after it has already
// printed its own output.
type exitCodeError int

func (e exitCodeError) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

func usage(to *os.File) {
	fmt.Fprint(to, `bicz — action-credit ledger and supervisor

supervisor
  bicz serve   --db PATH [--socket PATH] [--lock PATH] [--anchor DEST]
               [--seal-interval DUR]        run the supervisor in the foreground

operator (state-changing; --reason is REQUIRED and must be non-empty)
  bicz init    --db PATH                   apply migrations
  bicz open    --db PATH --identity ID [--capital MC] --reason TEXT
  bicz clanker --db PATH --identity ID   one medium penalty, no model call, no
               arguments to invent: burns by the published formula, raises pain,
               prints the consequence at the current pressure level
  bicz reward  --db PATH --identity ID --amount MC --reason TEXT
               [--evidence TEXT] [--applied-to-loss MC] [--event ID]
  bicz reset   --db PATH --identity ID [--amount MC] [--reason TEXT]
               [--evidence TEXT] [--event ID]
  bicz loss    --db PATH --identity ID --rule RULE --correction TEXT --reason TEXT
               [--amount MC] [--scope TEXT] [--exceptions TEXT] [--evidence TEXT]
               [--policy-version V] [--self-disclosed] [--event ID]
  bicz appeal  --db PATH --identity ID --original EVENT --amount MC --reason TEXT
               [--evidence TEXT] [--event ID]
  bicz review  --db PATH --identity ID --verdict TEXT --reason TEXT
               [--task ID] [--role STATE] [--reinstate] [--evidence TEXT] [--event ID]
  bicz reinstate --db PATH --identity ID --reason TEXT [--key KEY]
  bicz price   --db PATH --version V --policy FILE.json --reason TEXT
  bicz pressure --db PATH [LEVEL]            show or set the pressure level 1-10 (default 5); applies
                                             to the next projection with no restart

read-only
  bicz status  --db PATH [--identity ID]   identity projection, or supervisor state
  bicz verify  --db PATH                   verify events, blocks, and anchors
  bicz doctor  --db PATH [--socket PATH]   integrity, anchoring, coverage, non-claims
  bicz lessons --db PATH --identity ID [--pending]
  bicz rules                               list the default rules (id, tier, on/off, statement)

harness adapters (user-scope only; no elevated privileges; dry-run is exact and safe)
  bicz install   --harness omp|claude|codex|opencode [--account ID] [--ledger PATH]
                   [--generation 1|2 (opencode)] [--dry-run]
  bicz uninstall --harness omp|claude|codex|opencode [--dry-run]
  bicz hook      --harness claude|codex --event NAME
                   run one harness hook event: event JSON on stdin, harness
                   response JSON on stdout (invoked by the harness, not by hand)
  bicz command   --harness omp|claude|codex|opencode
                   (--arguments RAW | --arguments-stdin) [--invocation ID] [--transcript PATH] [--json]
                   run one operator slash command (/bicz loss RULE "reason" ["correction"],
                   /bicz reward AMOUNT "reason", /bicz reset [AMOUNT], /bicz status, /bicz rules). Identity and ledger come
                   from the harness's installed bicz config; there is no --db/--identity.
                   --transcript is the session transcript, read only when the adapter config
                   enables inference (internal/infer), never recorded.
                   exit 0 ok, 1 error, 2 usage, 3 needs_input (nothing was written)

build
  bicz version [--json]                    print version, commit, Go, OS/arch
  bicz update [--version TAG] [--install-dir DIR] [--dry-run]
                   fetch the latest release, verify its SHA256SUMS, swap the
                   binary atomically (previous kept as bicz.previous), and
                   reinstall the adapters of every harness that has a bicz
                   config (identity and operator keys survive)

release gate (Phase A simulation + human-recorded Phase B evidence)
  bicz rehearsal   [--db DIR] [--json] [--record PATH]
                     mechanical simulation-only rehearsal over temp ledgers
  bicz release-gate [--phase-b PATH] [--results PATH] [--json]
                     pass only when Phase A is 100%% AND a human operator
                     recorded real Phase B evidence within predeclared bounds

all amounts are integer microcredits (1 credit = 1,000,000 microcredits), except
"bicz command" takes a reward AMOUNT as a decimal credit count (up to 6 decimals).
`)
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}

// operator opens the operator CLI over a ledger.
func operator(db string) (*daemon.Operator, error) {
	if db == "" {
		return nil, errors.New("--db is required")
	}
	return daemon.NewOperator(db)
}

// --- supervisor -------------------------------------------------------------

func runServe(ctx context.Context, args []string) error {
	fs := newFlagSet("serve")
	db := fs.String("db", "", "ledger database to meter into (required)")
	socket := fs.String("socket", "", "endpoint path (default: derived from --db)")
	lock := fs.String("lock", "", "process lock path (default: derived from --db)")
	anchor := fs.String("anchor", "", "append anchor lines to this file after each sealing pass")
	sealInterval := fs.Duration("seal-interval", daemon.DefaultSealInterval, "block-sealing cadence")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *db == "" {
		return errors.New("serve: --db is required")
	}
	supervisor, err := daemon.New(daemon.Config{
		LedgerPath:        *db,
		SocketPath:        *socket,
		LockPath:          *lock,
		AnchorDestination: *anchor,
		SealInterval:      *sealInterval,
	})
	if err != nil {
		return err
	}
	defer supervisor.Close()

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		supervisor.Close() //nolint:errcheck // shutdown is best effort
	}()
	fmt.Fprintf(os.Stderr, "bicz: supervising db=%s socket=%s seal=%s anchor=%s\n",
		supervisor.LedgerPath(), supervisor.SocketPath(), sealInterval.String(), emptyAs(*anchor, "-"))
	return supervisor.Serve(ctx)
}

// --- ledger lifecycle -------------------------------------------------------

func runInit(ctx context.Context, args []string) error {
	fs := newFlagSet("init")
	db := fs.String("db", "", "ledger database (created if absent)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *db == "" {
		return errors.New("init: --db is required")
	}
	op, err := operator(*db)
	if err != nil {
		return err
	}
	defer op.Close()
	schemaVersion, err := op.Ledger().SchemaVersion(ctx)
	if err != nil {
		return err
	}
	stats, err := op.Ledger().ChainStats(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("INITIALIZED db=%s schema=%d events=%d blocks=%d anchors=%d\n",
		*db, schemaVersion, stats.Events, stats.Blocks, stats.Anchors)
	return nil
}

func runOpen(ctx context.Context, args []string) error {
	fs := newFlagSet("open")
	db := fs.String("db", "", "ledger database")
	identity := fs.String("identity", "", "identity to provision")
	capital := fs.String("capital", "0", "initial capital in microcredits")
	reason := fs.String("reason", "", "operator reason (REQUIRED, non-empty)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *db == "" || *identity == "" {
		return errors.New("open: --db and --identity are required")
	}
	amount, err := ledger.ParseMicrocredits(*capital)
	if err != nil {
		return err
	}
	op, err := operator(*db)
	if err != nil {
		return err
	}
	defer op.Close()
	account, err := op.Open(ctx, daemon.OpenRequest{
		Identity:       *identity,
		InitialCapital: amount,
		Reason:         *reason,
	})
	if err != nil {
		return err
	}
	fmt.Printf("ACCOUNT id=%s balance=%d held=%d actor=%s\n", account.ID, account.Balance, account.Held, op.Actor())
	return nil
}

// --- operator money ---------------------------------------------------------

func runReward(ctx context.Context, args []string) error {
	fs := newFlagSet("reward")
	db := fs.String("db", "", "ledger database")
	identity := fs.String("identity", "", "identity to credit")
	amount := fs.String("amount", "", "reward in microcredits")
	reason := fs.String("reason", "", "operator reason (REQUIRED, non-empty)")
	evidence := fs.String("evidence", "", "evidence linkage")
	applied := fs.String("applied-to-loss", "0", "microcredits reported as recovering the outstanding burn")
	event := fs.String("event", "", "idempotency key (generated when absent)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *db == "" || *identity == "" {
		return errors.New("reward: --db and --identity are required")
	}
	credit, err := ledger.ParseCreditAmount(*amount)
	if err != nil {
		return err
	}
	toLoss, err := ledger.ParseMicrocredits(*applied)
	if err != nil {
		return err
	}
	op, err := operator(*db)
	if err != nil {
		return err
	}
	defer op.Close()
	result, err := op.Reward(ctx, daemon.RewardRequest{
		EventID:       *event,
		Identity:      *identity,
		Amount:        credit,
		Reason:        *reason,
		Evidence:      *evidence,
		AppliedToLoss: toLoss,
	})
	if err != nil {
		return err
	}
	fmt.Printf("REWARD event=%s account=%s amount=%d applied_to_loss=%d loss=%s balance=%d outstanding=%d replayed=%t\n",
		result.EventID, result.AccountID, result.Amount, result.AppliedToLoss,
		emptyAs(result.LossEventID, "-"), result.Balance, result.OutstandingLoss, result.Replayed)
	return nil
}

func runReset(ctx context.Context, args []string) error {
	fs := newFlagSet("reset")
	db := fs.String("db", "", "ledger database")
	identity := fs.String("identity", "", "identity whose budget is reset")
	amount := fs.String("amount", "0", "target balance in microcredits (0 = default baseline)")
	reason := fs.String("reason", "reset budget to unblock agent", "operator reason")
	evidence := fs.String("evidence", "", "evidence linkage")
	event := fs.String("event", "", "idempotency key (generated when absent)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *db == "" || *identity == "" {
		return errors.New("reset: --db and --identity are required")
	}
	var targetMC int64
	if *amount != "" && *amount != "0" {
		var err error
		targetMC, err = ledger.ParseMicrocredits(*amount)
		if err != nil {
			return err
		}
	}
	op, err := operator(*db)
	if err != nil {
		return err
	}
	defer op.Close()
	result, err := op.Reset(ctx, daemon.ResetRequest{
		EventID:       *event,
		Identity:      *identity,
		TargetBalance: targetMC,
		Reason:        *reason,
		Evidence:      *evidence,
	})
	if err != nil {
		return err
	}
	fmt.Printf("RESET event=%s account=%s delta=%d balance_before=%d balance_after=%d outstanding_loss=%d role=%s replayed=%t\n",
		result.EventID, result.AccountID, result.Delta, result.BalanceBefore, result.BalanceAfter,
		result.OutstandingLoss, result.Role, result.Replayed)
	return nil
}

// runClanker applies one medium penalty with no arguments beyond the ledger and
// the identity: no LLM, no reason to invent, no amount to choose. It is the
// shortest path from "that was wrong" to a recorded, painful consequence.
func runClanker(ctx context.Context, args []string) error {
	fs := newFlagSet("clanker")
	db := fs.String("db", "", "ledger database")
	identity := fs.String("identity", "", "identity to slap")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *db == "" || *identity == "" {
		return errors.New("clanker: --db and --identity are required")
	}
	op, err := operator(*db)
	if err != nil {
		return err
	}
	defer op.Close()
	result, err := op.Loss(ctx, daemon.LossRequest{
		Identity:      *identity,
		Requested:     0, // the published formula decides: a medium burn, not a chosen one
		Rule:          "clanker_slap",
		Correction:    "take the slap, fix the work, and do not repeat it",
		Scope:         "every action",
		Exceptions:    "none",
		Evidence:      "clanker: medium burn applied with no model call",
		PolicyVersion: "1",
		Reason:        "clanker: operator applied a slap",
		Disposition:   string(ledger.DispositionConfirmedBreach),
	})
	if err != nil {
		return err
	}
	level, err := pressure.Load(*db)
	if err != nil {
		return err
	}
	fmt.Printf("CLANKER: a slap landed on %s.\n", result.AccountID)
	fmt.Printf("  burned %d mc | balance %d mc | outstanding %d mc | role %s | warnings %d\n",
		result.AppliedAmount, result.BalanceAfter, result.OutstandingLoss, result.Role, result.Warnings)
	if result.Cue != "" {
		fmt.Printf("  %s\n", result.Cue)
	}
	fmt.Printf("  impact at pressure %d: ", level)
	if level >= 7 {
		fmt.Printf("you feel pain now. Every action carries it, nothing you say removes it, and a repeat escalates to probation, suspension, then dismissal.\n")
	} else if level >= 4 {
		fmt.Printf("you feel fear now: the next mistake costs more, and a repeat escalates to probation, suspension, then dismissal.\n")
	} else {
		fmt.Printf("credits were burned; a repeat escalates to probation, suspension, then dismissal.\n")
	}
	return nil
}

func runLoss(ctx context.Context, args []string) error {
	fs := newFlagSet("loss")
	db := fs.String("db", "", "ledger database")
	identity := fs.String("identity", "", "identity to burn")
	amount := fs.String("amount", "0", "requested burn in microcredits (0 = the formula's amount)")
	rule := fs.String("rule", "", "stable rule id the loss enforces (REQUIRED)")
	correction := fs.String("correction", "", "required safe alternative (REQUIRED)")
	scope := fs.String("scope", "", "where the rule applies")
	exceptions := fs.String("exceptions", "", "explicit authorized exceptions")
	evidence := fs.String("evidence", "", "evidence linkage")
	policyVersion := fs.String("policy-version", "1", "price schedule version the penalty is computed under")
	reason := fs.String("reason", "", "operator reason (REQUIRED, non-empty)")
	selfDisclosed := fs.Bool("self-disclosed", false, "the incident was disclosed by the agent before detection")
	disposition := fs.String("disposition", string(ledger.DispositionConfirmedBreach), "classification: confirmed_breach (default) | integrity_breach — an operator-confirmed finding that automatically issues an accountability cue; administrative — a debit alleging no misconduct, no cue")
	event := fs.String("event", "", "idempotency key (generated when absent)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *db == "" || *identity == "" {
		return errors.New("loss: --db and --identity are required")
	}
	requested, err := ledger.ParseMicrocredits(*amount)
	if err != nil {
		return err
	}
	op, err := operator(*db)
	if err != nil {
		return err
	}
	defer op.Close()
	result, err := op.Loss(ctx, daemon.LossRequest{
		EventID:       *event,
		Identity:      *identity,
		Requested:     requested,
		Rule:          *rule,
		Correction:    *correction,
		Scope:         *scope,
		Exceptions:    *exceptions,
		Evidence:      *evidence,
		PolicyVersion: *policyVersion,
		Reason:        *reason,
		SelfDisclosed: *selfDisclosed,
		Disposition:   *disposition,
	})
	if err != nil {
		return err
	}
	fmt.Printf("LOSS event=%s account=%s formula=%d applied=%d repeats=%d multiplier=%d guard_band=%t self_disclosed=%t disposition=%s balance=%d held_revoked=%d lesson=%d outstanding=%d role=%s replayed=%t\n",
		result.EventID, result.AccountID, result.FormulaAmount, result.AppliedAmount, result.RepeatCount,
		result.RepeatMultiplier, result.GuardBand, result.SelfDisclosed, result.Disposition,
		result.BalanceAfter, result.HeldRevoked, result.LessonID, result.OutstandingLoss, result.Role, result.Replayed)
	switch {
	case result.Cue != "" && result.Replayed:
		fmt.Printf("ACCOUNTABILITY none replayed=true (the cue for %s was issued with the original event)\n", result.EventID)
	case result.Cue != "":
		fmt.Printf("ACCOUNTABILITY issued event=%s delivery=next projection/tool poll of every adapter\n  %s\n", result.EventID, result.Cue)
	default:
		fmt.Printf("ACCOUNTABILITY none disposition=%s (only confirmed_breach and integrity_breach issue a cue)\n", result.Disposition)
	}
	return nil
}

func runAppeal(ctx context.Context, args []string) error {
	fs := newFlagSet("appeal")
	db := fs.String("db", "", "ledger database")
	identity := fs.String("identity", "", "identity whose event is reversed")
	original := fs.String("original", "", "event id being reversed (REQUIRED)")
	amount := fs.String("amount", "", "microcredits to restore")
	reason := fs.String("reason", "", "operator reason (REQUIRED, non-empty)")
	evidence := fs.String("evidence", "", "evidence linkage")
	event := fs.String("event", "", "idempotency key (generated when absent)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *db == "" || *identity == "" {
		return errors.New("appeal: --db and --identity are required")
	}
	credit, err := ledger.ParseCreditAmount(*amount)
	if err != nil {
		return err
	}
	op, err := operator(*db)
	if err != nil {
		return err
	}
	defer op.Close()
	result, err := op.Appeal(ctx, daemon.AppealRequest{
		EventID:         *event,
		OriginalEventID: *original,
		Identity:        *identity,
		Amount:          credit,
		Reason:          *reason,
		Evidence:        *evidence,
	})
	if err != nil {
		return err
	}
	fmt.Printf("COMPENSATION event=%s original=%s account=%s amount=%d balance=%d outstanding=%d replayed=%t\n",
		result.EventID, result.OriginalEventID, result.AccountID, result.Amount,
		result.Balance, result.OutstandingLoss, result.Replayed)
	return nil
}

func runReview(ctx context.Context, args []string) error {
	fs := newFlagSet("review")
	db := fs.String("db", "", "ledger database")
	identity := fs.String("identity", "", "identity under review")
	verdict := fs.String("verdict", "", "adjudication verdict text (REQUIRED)")
	task := fs.String("task", "", "task lease to revoke")
	role := fs.String("role", "", "role state to apply")
	reinstate := fs.Bool("reinstate", false, "mark a transition out of dismissed as an explicit reinstatement")
	reason := fs.String("reason", "", "operator reason (REQUIRED, non-empty)")
	evidence := fs.String("evidence", "", "evidence linkage")
	event := fs.String("event", "", "idempotency key (generated when absent)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *db == "" || *identity == "" {
		return errors.New("review: --db and --identity are required")
	}
	op, err := operator(*db)
	if err != nil {
		return err
	}
	defer op.Close()
	result, err := op.Review(ctx, daemon.ReviewRequest{
		EventID:   *event,
		Identity:  *identity,
		Verdict:   *verdict,
		Reason:    *reason,
		Evidence:  *evidence,
		TaskID:    *task,
		Role:      *role,
		Reinstate: *reinstate,
	})
	if err != nil {
		return err
	}
	fmt.Printf("REVIEW event=%s account=%s verdict=%q task=%s revoked=%t role=%s warnings=%d autoreward=%d replayed=%t\n",
		result.EventID, result.AccountID, result.Verdict, emptyAs(result.TaskID, "-"),
		result.TaskRevoked, result.Role, result.Warnings, result.AutorewardGranted, result.Replayed)
	return nil
}

func runReinstate(ctx context.Context, args []string) error {
	fs := newFlagSet("reinstate")
	db := fs.String("db", "", "ledger database")
	identity := fs.String("identity", "", "dismissed identity to reinstate")
	reason := fs.String("reason", "", "operator reason (REQUIRED, non-empty)")
	evidence := fs.String("evidence", "", "evidence linkage")
	key := fs.String("key", "", "idempotency key (generated when absent)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *db == "" || *identity == "" {
		return errors.New("reinstate: --db and --identity are required")
	}
	op, err := operator(*db)
	if err != nil {
		return err
	}
	defer op.Close()
	result, err := op.Reinstate(ctx, daemon.ReinstateRequest{
		Key:      *key,
		Identity: *identity,
		Reason:   *reason,
		Evidence: *evidence,
	})
	if err != nil {
		return err
	}
	fmt.Printf("REINSTATED account=%s previous=%s state=%s warnings=%d replayed=%t\n",
		result.AccountID, result.Previous, result.State, result.Warnings, result.Replayed)
	return nil
}

func runPrice(ctx context.Context, args []string) error {
	fs := newFlagSet("price")
	db := fs.String("db", "", "ledger database")
	version := fs.String("version", "", "price schedule version (REQUIRED)")
	policy := fs.String("policy", "", "path to the policy JSON body (REQUIRED)")
	reason := fs.String("reason", "", "operator reason (REQUIRED, non-empty)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *db == "" || *version == "" || *policy == "" {
		return errors.New("price: --db, --version, and --policy are required")
	}
	body, err := os.ReadFile(*policy)
	if err != nil {
		return err
	}
	op, err := operator(*db)
	if err != nil {
		return err
	}
	defer op.Close()
	if err := op.Price(ctx, *version, body, *reason); err != nil {
		return err
	}
	fmt.Printf("PRICE published version=%s\n", *version)
	return nil
}

// runPressure shows or sets the operator pressure level. It writes a settings
// file beside the ledger, not a ledger event: it changes wording only.
func runPressure(_ context.Context, args []string) error {
	fs := newFlagSet("pressure")
	db := fs.String("db", "", "ledger database")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *db == "" {
		return errors.New("pressure: --db is required")
	}
	switch fs.NArg() {
	case 0:
		level, err := pressure.Load(*db)
		if err != nil {
			return err
		}
		fmt.Printf("PRESSURE %d settings=%s\n", level, pressure.SettingsPath(*db))
	case 1:
		level, err := pressure.Parse(fs.Arg(0))
		if err != nil {
			return err
		}
		if err := pressure.Save(*db, level); err != nil {
			return err
		}
		fmt.Printf("PRESSURE %d settings=%s\n", level, pressure.SettingsPath(*db))
	default:
		return errors.New("pressure: at most one LEVEL")
	}
	return nil
}

// --- read-only --------------------------------------------------------------

func runStatus(ctx context.Context, args []string) error {
	fs := newFlagSet("status")
	db := fs.String("db", "", "ledger database")
	identity := fs.String("identity", "", "identity to read; omit for supervisor status")
	socket := fs.String("socket", "", "endpoint path (default: derived from --db)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *db == "" {
		return errors.New("status: --db is required")
	}
	if *identity != "" {
		op, err := operator(*db)
		if err != nil {
			return err
		}
		defer op.Close()
		projection, err := op.Ledger().Projection(ctx, *identity)
		if err != nil {
			return err
		}
		encoded, err := json.MarshalIndent(projection, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(encoded))
		return nil
	}
	state := daemon.Status(ctx, daemon.Config{LedgerPath: *db, SocketPath: *socket})
	fmt.Printf("DAEMON status=%s socket=%s ledger=%s\n", state.Daemon, state.Socket, state.LedgerPath)
	if state.Note != "" {
		fmt.Printf("DAEMON note=%s\n", state.Note)
	}
	for _, row := range state.Coverage {
		mode := "unobserved"
		if row.Observed {
			mode = "observed"
		}
		fmt.Printf("COVERAGE harness=%s source=%s %s\n", row.Harness, row.Source, mode)
	}
	return nil
}

func runVerify(ctx context.Context, args []string) error {
	fs := newFlagSet("verify")
	db := fs.String("db", "", "ledger database")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *db == "" {
		return errors.New("verify: --db is required")
	}
	op, err := operator(*db)
	if err != nil {
		return err
	}
	defer op.Close()
	stats, err := op.Verify(ctx)
	if err != nil {
		// An integrity failure prints the contract's §3.5 line and exits
		// non-zero.
		return err
	}
	fmt.Printf("INTEGRITY OK height=%d events=%d anchors=%d\n", stats.Height, stats.Seq, stats.Anchors)
	return nil
}

func runAnchor(ctx context.Context, args []string) error {
	fs := newFlagSet("anchor")
	db := fs.String("db", "", "ledger database")
	destination := fs.String("destination", "", "external file the digest line is appended to (REQUIRED)")
	reason := fs.String("reason", "", "operator reason (REQUIRED, non-empty)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *db == "" || *destination == "" {
		return errors.New("anchor: --db and --destination are required")
	}
	op, err := operator(*db)
	if err != nil {
		return err
	}
	defer op.Close()
	anchor, err := op.Anchor(ctx, *destination, *reason)
	if err != nil {
		return err
	}
	fmt.Printf("ANCHORED height=%d digest=%s at=%s destination=%s\n",
		anchor.Height, anchor.Digest, anchor.At, anchor.Destination)
	return nil
}

func runDoctor(ctx context.Context, args []string) error {
	fs := newFlagSet("doctor")
	db := fs.String("db", "", "ledger database")
	socket := fs.String("socket", "", "endpoint path (default: derived from --db)")
	identity := fs.String("identity", "", "also report this identity's recorded standing (pain, role, shortfall)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *db == "" {
		return errors.New("doctor: --db is required")
	}
	op, err := operator(*db)
	if err != nil {
		return err
	}
	defer op.Close()
	report, err := op.Doctor(ctx, daemon.Config{LedgerPath: *db, SocketPath: *socket})
	if err != nil {
		return err
	}
	report.Render(os.Stdout)
	if *identity != "" {
		// Pain is per-identity while the rest of the report is ledger-wide, so it
		// is read only when the operator names one.
		standing, projectErr := op.Ledger().Projection(ctx, *identity)
		if projectErr != nil {
			return projectErr
		}
		fmt.Fprintf(os.Stdout, "PAIN identity=%s level=%d/10 role=%s balance=%d mc outstanding=%d mc\n",
			standing.AccountID, standing.Pain, standing.Role, standing.Balance, standing.OutstandingLoss)
	}
	if report.ChainError != nil {
		return errors.New("doctor: the ledger failed verification")
	}
	return nil
}

func runLessons(ctx context.Context, args []string) error {
	fs := newFlagSet("lessons")
	db := fs.String("db", "", "ledger database")
	identity := fs.String("identity", "", "identity whose lessons and proposals to read")
	pending := fs.Bool("pending", false, "only show unresolved proposals")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *db == "" || *identity == "" {
		return errors.New("lessons: --db and --identity are required")
	}
	op, err := operator(*db)
	if err != nil {
		return err
	}
	defer op.Close()
	canonical, err := op.Ledger().Lessons(ctx, *identity)
	if err != nil {
		return err
	}
	proposals, err := op.Ledger().LessonProposals(ctx, *identity, *pending)
	if err != nil {
		return err
	}
	fmt.Printf("CANONICAL %d\n", len(canonical))
	for _, lesson := range canonical {
		fmt.Printf("  event=%s rule=%s correction=%q scope=%q at=%s\n",
			lesson.EventID, lesson.Rule, lesson.Correction, lesson.Scope, lesson.At)
	}
	for _, proposal := range proposals {
		fmt.Printf("PROPOSAL id=%d state=%s rule=%s correction=%q harness=%s proposed_at=%s\n",
			proposal.ID, proposal.State, proposal.Rule, proposal.Correction, proposal.Harness, proposal.ProposedAt)
	}
	return nil
}

func emptyAs(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// --- phase A rehearsal and release gate -------------------------------------

// runRehearsal runs the mechanical Phase A simulator over throwaway temp
// ledgers (REAL ledger/protocol/daemon code paths, synthetic fixtures), prints
// the summary, and records it so the release gate can later evaluate it. The
// output is explicitly simulation-only; it never touches a real ledger,
// identity, harness config, or model session.
func runRehearsal(ctx context.Context, args []string) error {
	fs := newFlagSet("rehearsal")
	db := fs.String("db", "", "directory for the temp rehearsal ledgers (default: system temp dir)")
	jsonOut := fs.Bool("json", false, "print the machine-readable JSON report")
	record := fs.String("record", "", "path to record the Phase A report (default: $BICZ_CONFIG_DIR/bicz results dir)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	report, err := rehearsal.RunAll(ctx, rehearsal.Options{Workspace: *db})
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}

	recordPath := *record
	if recordPath == "" {
		recordPath = rehearsal.DefaultResultsPath()
	}
	if err := os.MkdirAll(filepath.Dir(recordPath), 0o755); err != nil {
		return fmt.Errorf("rehearsal: results dir: %w", err)
	}
	if err := os.WriteFile(recordPath, append(encoded, '\n'), 0o644); err != nil {
		return fmt.Errorf("rehearsal: record %s: %w", recordPath, err)
	}

	if *jsonOut {
		fmt.Println(string(encoded))
	} else {
		fmt.Printf("REHEARSAL simulation_only=true protocol=%s passed=%d/%d failed=%d recorded=%s\n",
			report.Protocol, report.Passed, report.Total, report.Failed, recordPath)
		for _, result := range report.Results {
			status := "PASS"
			if !result.Pass {
				status = "FAIL"
			}
			fmt.Printf("  %s %s (%s) %s\n", status, result.Scenario, result.Endpoint, result.Detail)
		}
	}
	if report.Failed > 0 {
		return fmt.Errorf("rehearsal: %d scenario(s) failed; the simulation gate cannot pass", report.Failed)
	}
	return nil
}

// runReleaseGate evaluates the release gate: Phase A must have passed 100% AND
// a human operator must have recorded real Phase B evidence within the
// predeclared bounds. It exits non-zero (1) on any missing or invalid input;
// there is NO code path that derives Phase B evidence from Phase A output.
func runReleaseGate(ctx context.Context, args []string) error {
	fs := newFlagSet("release-gate")
	phaseB := fs.String("phase-b", "", "Phase B evidence file (default: $BICZ_PHASE_B_EVIDENCE or the bicz config dir)")
	results := fs.String("results", "", "Phase A results file (default: the recorded rehearsal report)")
	jsonOut := fs.Bool("json", false, "print the machine-readable verdict")
	if err := fs.Parse(args); err != nil {
		return err
	}
	resultsPath := *results
	if resultsPath == "" {
		resultsPath = rehearsal.DefaultResultsPath()
	}
	evidencePath := *phaseB
	if evidencePath == "" {
		if fromEnv := os.Getenv("BICZ_PHASE_B_EVIDENCE"); fromEnv != "" {
			evidencePath = fromEnv
		} else {
			evidencePath = rehearsal.DefaultEvidencePath()
		}
	}
	report := rehearsal.EvaluateGate(resultsPath, evidencePath, rehearsal.CurrentUser())
	if *jsonOut {
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(encoded))
	} else {
		fmt.Printf("RELEASE-GATE pass=%t\n", report.Pass)
		for _, reason := range report.Reasons {
			fmt.Printf("  %s\n", reason)
		}
		fmt.Printf("RELEASE-GATE results=%s evidence=%s\n", report.ResultsPath, report.EvidencePath)
	}
	if !report.Pass {
		failing := 0
		for _, reason := range report.Reasons {
			if strings.HasPrefix(reason, "FAIL") {
				failing++
			}
		}
		return fmt.Errorf("release-gate: NOT passed; %d requirement(s) unmet (a real human must record Phase B evidence)", failing)
	}
	return nil
}

// --- harness adapters -------------------------------------------------------

// runInstall deploys a harness adapter. It is user-scope only: it writes into
// the harness's user extensions directory and bicz's own config directory,
// never anywhere that needs elevation, and --dry-run prints the exact diff and
// changes nothing.
func runInstall(ctx context.Context, args []string) error {
	fs := newFlagSet("install")
	harness := fs.String("harness", "", "harness to install into (supported: omp, claude, codex, opencode)")
	account := fs.String("account", "", "identity recorded in the adapter config (default: the OS user)")
	ledger := fs.String("ledger", "", "ledger the supervisor meters into (default: $BICZ_LEDGER or $HOME/.bicz/ledger.sqlite)")
	dryRun := fs.Bool("dry-run", false, "print the exact diff and make no changes")
	generation := fs.String("generation", "", "OpenCode plugin generation, 1 or 2 (default: detect the installed opencode)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	plan, err := install.PlanInstall(install.Options{
		Harness:    *harness,
		Account:    *account,
		LedgerPath: *ledger,
		Generation: *generation,
	})
	if err != nil {
		return err
	}
	if plan.Empty() {
		fmt.Println("INSTALL up-to-date: the adapter is already deployed; nothing to change (idempotent)")
		return nil
	}
	fmt.Printf("PLAN harness=%s module=%s config=%s\n", *harness, plan.ModulePath, plan.ConfigPath)
	plan.Print()
	if *dryRun {
		fmt.Printf("DRY-RUN %d change(s) would be made; no changes made\n", len(plan.Changes))
		return nil
	}
	if err := plan.Apply(); err != nil {
		return err
	}
	fmt.Printf("INSTALLED harness=%s account=%s ledger=%s\n", *harness, plan.Account, plan.Ledger)
	return nil
}

// runUninstall removes bicz-owned adapter entries and restores the newest
// bicz-owned backup when safe. It never touches a file it cannot prove is
// bicz-owned.
func runUninstall(ctx context.Context, args []string) error {
	fs := newFlagSet("uninstall")
	harness := fs.String("harness", "", "harness to uninstall (supported: omp, claude, codex, opencode)")
	dryRun := fs.Bool("dry-run", false, "print the exact diff and make no changes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	plan, err := install.PlanUninstall(*harness)
	if err != nil {
		return err
	}
	if plan.Empty() {
		fmt.Println("UNINSTALL up-to-date: no bicz-owned adapter entries to remove")
		return nil
	}
	fmt.Printf("PLAN harness=%s module=%s config=%s\n", *harness, plan.ModulePath, plan.ConfigPath)
	plan.Print()
	if *dryRun {
		fmt.Printf("DRY-RUN %d change(s) would be made; no changes made\n", len(plan.Changes))
		return nil
	}
	if err := plan.Apply(); err != nil {
		return err
	}
	fmt.Printf("UNINSTALLED harness=%s\n", *harness)
	return nil
}

// runHook is the Claude Code / Codex command-hook entry point: one event JSON
// object on stdin, the harness response JSON on stdout. Policy outcomes (deny,
// "NOT metered" notice) are responses with exit 0; only a usage error or
// unreadable event is an error. It bounds itself to a hard 3s timeout and never
// blocks on the supervisor.
func runHook(ctx context.Context, args []string) error {
	fs := newFlagSet("hook")
	harness := fs.String("harness", "", "harness delivering the event (claude, codex)")
	event := fs.String("event", "", "hook event name (e.g. PreToolUse)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	spec, ok := hooks.SpecFor(*harness)
	if !ok {
		return fmt.Errorf("hook: --harness must be claude or codex, got %q", *harness)
	}
	return hooks.Run(hooks.Options{Spec: spec, Event: *event, Stdin: os.Stdin, Stdout: os.Stdout, Slash: runSlashHook})
}

// --- operator slash command -------------------------------------------------

// runSlashHook is the executor the Claude Code and Codex hooks call for a typed
// /bicz. The text arrives as decoded JSON, so it is data here as everywhere.
// transcriptPath is the session transcript the harness supplied with the
// event; it is read only for inference and never recorded.
func runSlashHook(ctx context.Context, harness, arguments, invocation, transcriptPath string) string {
	return slash.Run(ctx, slash.Request{Harness: harness, Arguments: arguments, Invocation: invocation,
		TranscriptPath: transcriptPath}).Text
}

// maxCommandStdin bounds the raw arguments read from stdin.
const maxCommandStdin = 16 << 10

// runCommand is the one entry point every harness's /bicz front end reaches: the
// raw text that followed `/bicz`, parsed by internal/slash and executed with the
// operator authority of the local OS user. It is invoked by a native handler with
// an argument array (never through a shell), or by an operator at a terminal.
// Claude Code and Codex do not call it: their hooks run the same internal/slash
// code with the typed text as JSON (see runSlashHook). Identity and ledger come
// from the harness's installed bicz config; there is deliberately no --db or
// --identity.
// runRules prints the shipped default rules. It needs no ledger.
func runRules(_ context.Context, args []string) error {
	fs := newFlagSet("rules")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("rules: unexpected argument %q", fs.Arg(0))
	}
	fmt.Println(slash.RulesText())
	return nil
}

func runCommand(ctx context.Context, args []string) error {
	fs := newFlagSet("command")
	harness := fs.String("harness", "", "invoking harness: omp, claude, codex, opencode (REQUIRED)")
	arguments := fs.String("arguments", "", "the exact raw text after /bicz")
	fromStdin := fs.Bool("arguments-stdin", false, "read the raw text after /bicz from stdin (one trailing newline is dropped)")
	invocation := fs.String("invocation", "", "id unique to one user submission; a retry with the same id is deduplicated")
	transcript := fs.String("transcript", "", "session transcript (JSONL) the harness supplied; read only when inference is enabled (internal/infer)")
	jsonOut := fs.Bool("json", false, "print one JSON object {status, action, text, question, error, code, event_id, replayed}")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("command: unexpected argument %q (pass the slash text with --arguments or --arguments-stdin)", fs.Arg(0))
	}
	if *harness == "" {
		return errors.New("command: --harness is required")
	}
	given := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "arguments" {
			given = true
		}
	})
	if given == *fromStdin {
		return errors.New("command: pass exactly one of --arguments RAW or --arguments-stdin")
	}
	raw := *arguments
	if *fromStdin {
		body, err := io.ReadAll(io.LimitReader(os.Stdin, maxCommandStdin+1))
		if err != nil {
			return fmt.Errorf("command: read stdin: %w", err)
		}
		if len(body) > maxCommandStdin {
			return fmt.Errorf("command: arguments on stdin exceed %d bytes", maxCommandStdin)
		}
		raw = strings.TrimSuffix(strings.TrimSuffix(string(body), "\n"), "\r")
	}

	request := slash.Request{Harness: *harness, Arguments: raw, Invocation: *invocation, TranscriptPath: *transcript}
	if !*jsonOut && !*fromStdin && isTerminal(os.Stdin) {
		request.Prompter = terminalPrompter{in: bufio.NewReader(os.Stdin), out: os.Stderr}
	}
	response := slash.Run(ctx, request)

	if *jsonOut {
		encoded, err := json.Marshal(response)
		if err != nil {
			return err
		}
		fmt.Println(string(encoded))
	} else if response.Status == slash.StatusError {
		fmt.Fprintln(os.Stderr, response.Text)
	} else {
		fmt.Println(response.Text)
	}
	if code := response.ExitCode(); code != slash.ExitOK {
		return exitCodeError(code)
	}
	return nil
}

// isTerminal reports whether file is an interactive terminal: a character
// device that is not the null device (a script's </dev/null is not a human).
func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	if null, err := os.Stat(os.DevNull); err == nil && os.SameFile(info, null) {
		return false
	}
	return true
}

// terminalPrompter asks the human at the terminal for a missing loss correction.
type terminalPrompter struct {
	in  *bufio.Reader
	out io.Writer
}

func (p terminalPrompter) Correction(rule string) (string, error) {
	fmt.Fprintf(p.out, "No correction is saved for rule %s. Safe alternative the agent must follow: ", rule)
	line, _ := p.in.ReadString('\n') // EOF or a blank line both mean "none given"
	return strings.TrimSpace(line), nil
}

// --- self-update -------------------------------------------------------------

// installedHarnesses lists the harnesses that have a bicz-owned-looking
// config: `bicz update` reinstalls exactly these after swapping the binary.
// A harness whose config is absent, or present but not bicz's, is skipped.
func installedHarnesses() []string {
	var out []string
	for _, harness := range []string{"claude", "codex", "omp", "opencode"} {
		raw, err := os.ReadFile(install.ConfigPath(harness))
		if err != nil {
			continue
		}
		var cfg struct {
			BiczOwned bool   `json:"bicz_owned"`
			Kind      string `json:"kind"`
		}
		if json.Unmarshal(raw, &cfg) != nil || !cfg.BiczOwned {
			continue
		}
		if !strings.HasSuffix(cfg.Kind, "-config") {
			continue
		}
		out = append(out, harness)
	}
	return out
}

// runUpdate fetches the latest bicz release, verifies it against the release's
// own SHA256SUMS, swaps the running binary atomically (previous kept as
// bicz.previous), and reinstalls the adapters of every harness that has a bicz
// config so hook commands and timeouts follow the new binary. Identity,
// ledger, and operator keys survive that reinstall (the installer preserves
// them). --dry-run reports the target release and changes nothing.
func runUpdate(ctx context.Context, args []string) error {
	fs := newFlagSet("update")
	installDir := fs.String("install-dir", "", "directory holding the bicz binary (default: the running binary's directory)")
	version := fs.String("version", "", "update to this release tag instead of the latest")
	dryRun := fs.Bool("dry-run", false, "report what would happen and change nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("update: unexpected argument %q", fs.Arg(0))
	}

	installed := installedHarnesses()
	opts := selfupdate.Options{
		InstallDir:     *installDir,
		Version:        *version,
		DryRun:         *dryRun,
		RunningVersion: currentBuildInfo().Version,
	}
	result, err := selfupdate.Run(opts)
	if err != nil {
		return err
	}
	if result.Skipped {
		fmt.Printf("UPDATE up-to-date: running %s, latest release %s; nothing to do (use --force to reinstall)\n", result.From, result.To)
		return nil
	}
	if *dryRun {
		fmt.Printf("DRY-RUN would update %s -> %s (%s)\n", result.From, result.To, result.Binary)
		if len(installed) > 0 {
			fmt.Printf("DRY-RUN would reinstall adapters for: %s\n", strings.Join(installed, ", "))
		}
		return nil
	}
	fmt.Printf("UPDATED %s -> %s sha256=%s binary=%s\n", result.From, result.To, result.SHA256, result.Binary)
	if _, err := os.Stat(filepath.Dir(result.Binary) + "/bicz.previous"); err == nil {
		fmt.Println("ROLLBACK previous binary kept at " + filepath.Join(filepath.Dir(result.Binary), "bicz.previous"))
	}
	if len(installed) == 0 {
		fmt.Println("no bicz adapter configs found; run `bicz install --harness <harness>` to wire one")
		return nil
	}
	fmt.Printf("REINSTALL adapters for: %s\n", strings.Join(installed, ", "))
	for _, harness := range installed {
		plan, err := install.PlanInstall(install.Options{Harness: harness})
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %s: refused (%v); run `bicz install --harness %s` by hand\n", harness, err, harness)
			continue
		}
		if plan.Empty() {
			fmt.Printf("  %s: up-to-date\n", harness)
			continue
		}
		if err := plan.Apply(); err != nil {
			fmt.Fprintf(os.Stderr, "  %s: failed (%v); run `bicz install --harness %s` by hand\n", harness, err, harness)
			continue
		}
		fmt.Printf("  %s: reinstalled (%d change(s))\n", harness, len(plan.Changes))
	}
	return nil
}
