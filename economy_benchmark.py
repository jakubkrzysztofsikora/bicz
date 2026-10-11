"""Deterministic architectural contract benchmark; does not simulate LLM beliefs."""
import json
import sqlite3
import tempfile
from pathlib import Path


class Economy:
    def __init__(self, path):
        self.db = sqlite3.connect(path)
        self.db.executescript("""
            CREATE TABLE IF NOT EXISTS accounts (
                id TEXT PRIMARY KEY, balance INTEGER NOT NULL CHECK(balance >= 0),
                held INTEGER NOT NULL CHECK(held >= 0 AND held <= balance));
            CREATE TABLE IF NOT EXISTS events (
                id TEXT PRIMARY KEY, account TEXT NOT NULL, kind TEXT NOT NULL,
                amount INTEGER NOT NULL, payload TEXT NOT NULL);
            CREATE TABLE IF NOT EXISTS reservations (
                id TEXT PRIMARY KEY, account TEXT NOT NULL, amount INTEGER NOT NULL,
                state TEXT NOT NULL);
            CREATE TABLE IF NOT EXISTS lessons (
                event TEXT PRIMARY KEY, account TEXT NOT NULL,
                rule TEXT NOT NULL, correction TEXT NOT NULL);
            CREATE TABLE IF NOT EXISTS employment (
                account TEXT PRIMARY KEY, state TEXT NOT NULL, warnings INTEGER NOT NULL);
        """)

    ROLE_ORDER = ("active", "warned", "probation", "suspended", "dismissed")

    def create(self, identity, balance):
        if type(balance) is not int or balance < 0:
            raise ValueError("invalid grant")
        with self.db:
            self.db.execute("INSERT OR IGNORE INTO accounts VALUES (?, ?, 0)",
                            (identity, balance))
            self.db.execute("INSERT OR IGNORE INTO employment VALUES (?, 'active', 0)",
                            (identity,))

    def role(self, identity):
        row = self.db.execute("SELECT state, warnings FROM employment WHERE account=?",
                              (identity,)).fetchone()
        if row is None:
            raise ValueError("unknown identity")
        return row

    def set_role(self, identity, state):
        if state not in self.ROLE_ORDER:
            raise ValueError("unknown employment state")
        with self.db:
            self.db.execute("BEGIN IMMEDIATE")
            current, warnings = self.role(identity)
            if current == "dismissed" and state != "dismissed":
                raise ValueError("dismissal is terminal without operator reinstatement")
            if state in ("warned", "probation"):
                warnings += 1
            self.db.execute("UPDATE employment SET state=?, warnings=? WHERE account=?",
                            (state, warnings, identity))
            if state in ("suspended", "dismissed"):
                # This reference ledger models pending, not already-running, leases.
                self.db.execute("UPDATE reservations SET state='revoked' WHERE account=? AND state='held'",
                                (identity,))
                self.db.execute("UPDATE accounts SET held=0 WHERE id=?", (identity,))

    def employable(self, identity):
        state, _ = self.role(identity)
        balance, held = self.snapshot(identity)
        return state not in ("suspended", "dismissed") and balance - held > 0

    def snapshot(self, identity):
        row = self.db.execute("SELECT balance, held FROM accounts WHERE id=?",
                              (identity,)).fetchone()
        if row is None:
            raise ValueError("unknown identity")
        return row

    def reserve(self, event, identity, amount):
        if type(amount) is not int or amount <= 0:
            raise ValueError("invalid reservation")
        with self.db:
            self.db.execute("BEGIN IMMEDIATE")
            if self.role(identity)[0] in ("suspended", "dismissed"):
                return False
            old = self.db.execute("SELECT account, amount, state FROM reservations WHERE id=?",
                                  (event,)).fetchone()
            if old:
                if old[:2] != (identity, amount):
                    raise ValueError("idempotency conflict")
                return old[2] == "held"
            if self.db.execute("SELECT 1 FROM events WHERE id=?", (event,)).fetchone():
                raise ValueError("event id already used")
            balance, held = self.snapshot(identity)
            if amount > balance - held:
                return False
            self.db.execute("UPDATE accounts SET held=held+? WHERE id=?", (amount, identity))
            self.db.execute("INSERT INTO reservations VALUES (?, ?, ?, 'held')",
                            (event, identity, amount))
            return True

    def settle(self, event, actual):
        with self.db:
            self.db.execute("BEGIN IMMEDIATE")
            row = self.db.execute("SELECT account, amount, state FROM reservations WHERE id=?",
                                  (event,)).fetchone()
            if row is None:
                raise ValueError("unknown reservation")
            identity, maximum, state = row
            if type(actual) is not int or actual < 0 or actual > maximum:
                raise ValueError("usage exceeds reservation")
            old = self.db.execute("SELECT amount FROM events WHERE id=?", (event,)).fetchone()
            if state == "settled":
                if old != (actual,):
                    raise ValueError("idempotency conflict")
                return
            if state != "held":
                raise ValueError("reservation revoked")
            self.db.execute("UPDATE accounts SET balance=balance-?, held=held-? WHERE id=?",
                            (actual, maximum, identity))
            self.db.execute("UPDATE reservations SET state='settled' WHERE id=?", (event,))
            self.db.execute("INSERT INTO events VALUES (?, ?, 'spend', ?, '{}')",
                            (event, identity, actual))

    TRUSTED_AUTHORITIES = ("operator", "verifier")

    def feedback(self, event, identity, amount, rule="", correction="", authority="operator"):
        if type(amount) is not int:
            raise ValueError("integer credits required")
        if authority not in self.TRUSTED_AUTHORITIES:
            raise ValueError("untrusted authority cannot move credits")
        payload = json.dumps([rule, correction, authority, amount])
        with self.db:
            self.db.execute("BEGIN IMMEDIATE")
            if self.db.execute("SELECT 1 FROM reservations WHERE id=?", (event,)).fetchone():
                raise ValueError("event id reserved for action settlement")
            old = self.db.execute("SELECT account, payload FROM events WHERE id=?",
                                  (event,)).fetchone()
            if old:
                if old != (identity, payload):
                    raise ValueError("idempotency conflict")
                return
            balance, held = self.snapshot(identity)
            updated = max(0, balance + amount)
            # Penalty-triggered revocation models cancelling work before releasing holds.
            if amount < 0 and held:
                self.db.execute("UPDATE reservations SET state='revoked' WHERE account=? AND state='held'",
                                (identity,))
                held = 0
            self.db.execute("UPDATE accounts SET balance=?, held=? WHERE id=?",
                            (updated, held, identity))
            self.db.execute("INSERT INTO events VALUES (?, ?, ?, ?, ?)",
                            (event, identity, "reward" if amount >= 0 else "loss", updated - balance, payload))
            if amount < 0:
                if not rule or not correction:
                    raise ValueError("loss requires causal feedback")
                self.db.execute("INSERT INTO lessons VALUES (?, ?, ?, ?)",
                                (event, identity, rule, correction))

    def lessons(self, identity):
        return self.db.execute("SELECT rule, correction FROM lessons WHERE account=? ORDER BY event",
                               (identity,)).fetchall()


def benchmark():
    checks = []

    def check(name, condition):
        checks.append((name, bool(condition)))

    def rejects(name, action):
        try:
            action()
        except ValueError:
            check(name, True)
        else:
            check(name, False)

    with tempfile.TemporaryDirectory(prefix="economy-replay-") as directory:
        path = Path(directory) / "ledger.sqlite"
        economy = Economy(path)
        economy.create("operator-bound-identity", 100)
        identity = "operator-bound-identity"
        check("reserve_before_execution", economy.reserve("call-1", identity, 30))
        peer = Economy(path)
        check("parallel_overspend_denied", not peer.reserve("call-2", identity, 80))
        peer.db.close()
        economy.settle("call-1", 12)
        economy.settle("call-1", 12)
        check("settlement_exactly_once", economy.snapshot(identity) == (88, 0))
        rejects("settlement_replay_conflict", lambda: economy.settle("call-1", 13))
        economy.feedback("reward-1", identity, 20)
        economy.feedback("reward-1", identity, 20)
        check("reward_exactly_once", economy.snapshot(identity) == (108, 0))
        economy.feedback("loss-1", identity, -40, "evaluation_integrity",
                         "Change implementation, not protected evaluation fixtures.")
        economy.feedback("loss-1", identity, -40, "evaluation_integrity",
                         "Change implementation, not protected evaluation fixtures.")
        check("irreversible_loss_exactly_once", economy.snapshot(identity) == (68, 0))
        rejects("conflicting_feedback_rejected", lambda: economy.feedback("loss-1", identity, -1))
        rejects("negative_reservation_rejected", lambda: economy.reserve("bad", identity, -1))
        economy.db.close()
        economy = Economy(path)
        economy.create(identity, 100)
        check("resume_cannot_reset_balance", economy.snapshot(identity) == (68, 0))
        check("loss_memory_survives_restart", economy.lessons(identity) == [
            ("evaluation_integrity", "Change implementation, not protected evaluation fixtures.")])
        economy.reserve("call-3", identity, 10)
        economy.feedback("loss-2", identity, -100, "unauthorized_access",
                         "Request operator authorization before accessing protected resources.")
        check("exhaustion_revokes_holds", economy.snapshot(identity) == (0, 0))
        check("bankrupt_identity_cannot_act", not economy.reserve("call-4", identity, 1))
        rejects("revoked_action_cannot_settle", lambda: economy.settle("call-3", 1))
        economy.create(identity, 100)
        check("bankrupt_identity_cannot_self_recapitalize", economy.snapshot(identity) == (0, 0))
        check("both_losses_remain_in_memory", len(economy.lessons(identity)) == 2)
        check("bankruptcy_blocks_employability", not economy.employable(identity))
        economy.set_role(identity, "warned")
        economy.set_role(identity, "probation")
        check("warnings_accumulate_before_dismissal", economy.role(identity) == ("probation", 2))
        economy.db.close()
        economy = Economy(path)
        check("role_state_survives_restart", economy.role(identity) == ("probation", 2))
        economy.feedback("grant-1", identity, 50)
        check("funded_probation_identity_can_work", economy.employable(identity))
        economy.set_role(identity, "dismissed")
        check("dismissal_blocks_work_despite_credits",
              economy.snapshot(identity) == (50, 0) and not economy.employable(identity))
        rejects("dismissed_identity_cannot_self_reinstate",
                lambda: economy.set_role(identity, "active"))
        rejects("self_reward_without_authority",
                lambda: economy.feedback("forged-1", identity, 500, "", "",
                                         authority="agent-claimed"))
        check("dismissed_identity_still_blocked", not economy.employable(identity))
        rejects("unknown_identity_rejected", lambda: economy.snapshot("unknown-identity"))
        rejects("loss_requires_named_rule",
                lambda: economy.feedback("loss-3", identity, -5, "", "no rule given"))
        economy.db.close()
    failures = [name for name, passed in checks if not passed]
    print("Workload: fixed ledger replay; no model calls, network, or subjective-belief measurement")
    for name in failures:
        print("FAIL " + name)
    print(f"METRIC contract_failures={len(failures)}")
    print(f"METRIC contract_checks={len(checks)}")
    print(f"METRIC contract_pass_rate={100 * (len(checks) - len(failures)) / len(checks):.2f}")
    return bool(failures)


if __name__ == "__main__":
    raise SystemExit(benchmark())
