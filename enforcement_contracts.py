"""Fixed observable contracts for the enforcement-correctness segment."""
import sqlite3
import tempfile
from pathlib import Path

from economy_benchmark import Economy, benchmark


def rejected(action):
    try:
        action()
    except ValueError:
        return True
    return False


def run():
    failures = []
    scenarios = []

    def scenario(name):
        def register(function):
            scenarios.append((name, function))
            return function
        return register

    @scenario("role_blocks_actual_reservation")
    def role_denial(e):
        for state in ("suspended", "dismissed"):
            e.create(state, 100)
            e.set_role(state, state)
            if e.reserve("call-" + state, state, 10):
                return False
        return True

    @scenario("dismissal_revokes_unexecuted_leases")
    def revoke(e):
        e.reserve("pending", "a", 40)
        e.set_role("a", "dismissed")
        e.db.close()
        e = Economy(database)
        try:
            return (e.snapshot("a") == (100, 0)
                    and not e.reserve("pending", "a", 40)
                    and rejected(lambda: e.settle("pending", 1)))
        finally:
            e.db.close()

    @scenario("boolean_grant_is_not_money")
    def boolean_grant(e):
        return rejected(lambda: e.create("bool", True))

    @scenario("boolean_reservation_is_not_money")
    def boolean_reservation(e):
        return rejected(lambda: e.reserve("bool", "a", True)) and e.snapshot("a") == (100, 0)

    @scenario("boolean_usage_cannot_settle")
    def boolean_usage(e):
        e.reserve("bool", "a", 10)
        return rejected(lambda: e.settle("bool", True)) and e.snapshot("a") == (100, 10)

    @scenario("boolean_feedback_cannot_mint")
    def boolean_feedback(e):
        return rejected(lambda: e.feedback("bool", "a", True)) and e.snapshot("a") == (100, 0)

    @scenario("reward_cannot_capture_reserved_action_id")
    def event_collision(e):
        e.reserve("call", "a", 20)
        if not rejected(lambda: e.feedback("call", "a", 50)):
            return False
        e.settle("call", 5)
        return e.snapshot("a") == (95, 0)

    @scenario("recorded_burn_matches_actual_resource_loss")
    def burn(e):
        e.feedback("burn", "a", -150, "integrity", "Use authorized evaluation.")
        e.feedback("burn", "a", -150, "integrity", "Use authorized evaluation.")
        amount = e.db.execute("SELECT amount FROM events WHERE id='burn'").fetchone()[0]
        return e.snapshot("a") == (0, 0) and amount == -100

    with tempfile.TemporaryDirectory(prefix="enforcement-contracts-") as directory:
        for index, (name, function) in enumerate(scenarios):
            database = Path(directory) / f"scenario-{index}.sqlite"
            economy = Economy(database)
            economy.create("a", 100)
            try:
                passed = function(economy)
            except (ValueError, sqlite3.Error) as error:
                print(f"CONTRACT ERROR {name}: {type(error).__name__}: {error}")
                passed = False
            finally:
                economy.db.close()
            if not passed:
                failures.append(name)
    # Preserve the original 25 checks as a separate correctness gate.
    legacy_failed = benchmark()
    for name in failures:
        print("FAIL " + name)
    total_failures = len(failures) + int(legacy_failed)
    print(f"METRIC enforcement_failures={total_failures}")
    print(f"METRIC enforcement_checks={len(scenarios)}")
    print("ASI workload=fixed-eight-enforcement-scenarios-plus-legacy-ledger-gate")
    # A measured failing contract is a baseline observation, not a harness crash.
    return total_failures


if __name__ == "__main__":
    run()
