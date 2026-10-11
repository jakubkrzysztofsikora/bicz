package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A hook process is stateless, but a settlement must charge exactly what the
// PreToolUse reserved. The price schedule is mutable, so PostToolUse must never
// re-quote it: PreToolUse freezes the quote (amount, policy version, price
// state, reservation id) into one small file per call, keyed by session +
// tool_use_id, and PostToolUse settles from that file alone.
//
// The record is written BEFORE authorize (status pending) so a crash or a lost
// response between the daemon's reserve and the hook's exit still leaves the
// frozen amount behind. It then moves through:
//
//	pending    intent written, authorize answer not known
//	reserved   the daemon allowed it; a hold exists for Amount
//	unreserved nothing was reserved (unobserved path, unpriced, unmanaged-and-
//	           never-pending); PostToolUse has nothing to settle
//	settled    the daemon confirmed the settlement; kept so a duplicate
//	           PostToolUse replays the same receipt instead of finding nothing
//
// Only confirmed-finished records are pruned (settled and unreserved ones older
// than stateRetention). pending and reserved records are never pruned: they may
// stand for a live hold (docs/CONTRACTS.md §1 I8).

const (
	statusPending    = "pending"
	statusReserved   = "reserved"
	statusUnreserved = "unreserved"
	statusSettled    = "settled"

	stateVersion   = 1
	stateRetention = 24 * time.Hour
)

// callState is the frozen quote and lifecycle of one tool call.
type callState struct {
	Version       int    `json:"version"`
	Harness       string `json:"harness"`
	SessionID     string `json:"session_id"`
	ToolUseID     string `json:"tool_use_id"`
	Tool          string `json:"tool"`
	ArgsDigest    string `json:"args_digest"`
	Status        string `json:"status"`
	Amount        int64  `json:"amount_mc"`
	PolicyVersion string `json:"policy_version"`
	PriceState    string `json:"price_state"`
	QuoteReason   string `json:"quote_reason"`
	ReservationID string `json:"reservation_id"`
	At            string `json:"at"`
	SettledAt     string `json:"settled_at,omitempty"`
}

// StateDir is where hook call state lives: $BICZ_STATE_DIR, else
// $XDG_STATE_HOME/bicz, else $HOME/.local/state/bicz.
func StateDir() (string, error) {
	if explicit := os.Getenv("BICZ_STATE_DIR"); explicit != "" {
		return explicit, nil
	}
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "bicz"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errors.New("no state directory: set BICZ_STATE_DIR (cannot resolve $HOME)")
	}
	return filepath.Join(home, ".local", "state", "bicz"), nil
}

// store is the per-harness directory of call records.
type store struct{ dir string }

func openStore(spec *Spec, override string) (store, error) {
	base := override
	if base == "" {
		var err error
		if base, err = StateDir(); err != nil {
			return store{}, err
		}
	}
	return store{dir: filepath.Join(base, "hooks", spec.ID)}, nil
}

// path names one call's record. The file name is a digest, so a hostile
// session_id or tool_use_id can never escape the directory.
func (s store) path(sessionID, toolUseID string) string {
	sum := sha256.Sum256([]byte(sessionID + "\x00" + toolUseID))
	return filepath.Join(s.dir, hex.EncodeToString(sum[:])+".json")
}

// load reads a call's record. A missing record is (nil, nil); an unreadable,
// malformed, or foreign one is an error, never a guess.
func (s store) load(sessionID, toolUseID string) (*callState, error) {
	path := s.path(sessionID, toolUseID)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read hook state %s: %w", path, err)
	}
	var st callState
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("hook state %s is malformed: %w", path, err)
	}
	if st.Version != stateVersion || st.SessionID != sessionID || st.ToolUseID != toolUseID {
		return nil, fmt.Errorf("hook state %s does not belong to this call (version %d)", path, st.Version)
	}
	switch st.Status {
	case statusPending, statusReserved, statusUnreserved, statusSettled:
	default:
		return nil, fmt.Errorf("hook state %s has unknown status %q", path, st.Status)
	}
	if st.Amount < 0 {
		return nil, fmt.Errorf("hook state %s has a negative amount", path)
	}
	return &st, nil
}

// save writes the record atomically (temp file + rename, owner-only).
func (s store) save(st *callState) error {
	encoded, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return s.writeAtomic(s.path(st.SessionID, st.ToolUseID), encoded)
}

func (s store) writeAtomic(path string, encoded []byte) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("create hook state dir %s: %w", s.dir, err)
	}
	temp, err := os.CreateTemp(s.dir, ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("hook state temp file: %w", err)
	}
	tempName := temp.Name()
	defer func() {
		if tempName != "" {
			_ = os.Remove(tempName)
		}
	}()
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(append(encoded, '\n')); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("write hook state: %w", err)
	}
	tempName = ""
	return nil
}

// penaltySeenRetention is how long an idle session's delivered-penalty mark is
// kept. Losing it only means the session is told its active penalties once more.
const penaltySeenRetention = 7 * 24 * time.Hour

// seenRecord is the per-session mark of the newest confirmed penalty already
// emitted to the harness. It records delivery to the harness, nothing about
// whether or how the model took it in.
type seenRecord struct {
	Version   int    `json:"version"`
	SessionID string `json:"session_id"`
	Seq       int64  `json:"penalty_seq"`
}

func (s store) seenPath(sessionID string) string {
	sum := sha256.Sum256([]byte("penalty-seen\x00" + sessionID))
	return filepath.Join(s.dir, hex.EncodeToString(sum[:])+".seen")
}

// loadSeen returns the newest penalty position already delivered to this
// session. A missing or unreadable mark is 0: the session has been told nothing,
// so every active penalty is (re)delivered rather than silently skipped.
func (s store) loadSeen(sessionID string) int64 {
	raw, err := os.ReadFile(s.seenPath(sessionID))
	if err != nil {
		return 0
	}
	var rec seenRecord
	if json.Unmarshal(raw, &rec) != nil || rec.Version != stateVersion || rec.SessionID != sessionID || rec.Seq < 0 {
		return 0
	}
	return rec.Seq
}

// markSeen advances the delivered mark; it never moves it backwards.
func (s store) markSeen(sessionID string, seq int64) {
	if seq <= s.loadSeen(sessionID) {
		return
	}
	encoded, err := json.Marshal(seenRecord{Version: stateVersion, SessionID: sessionID, Seq: seq})
	if err != nil {
		return
	}
	_ = s.writeAtomic(s.seenPath(sessionID), encoded)
}

func (s store) remove(sessionID, toolUseID string) {
	_ = os.Remove(s.path(sessionID, toolUseID))
}

// prune deletes finished records (settled, unreserved) older than the
// retention window. It never touches pending or reserved records, and a record
// it cannot parse is left alone for an operator to inspect.
func (s store) prune(now time.Time) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasSuffix(entry.Name(), ".seen") {
			if info, err := entry.Info(); err == nil && now.Sub(info.ModTime()) >= penaltySeenRetention {
				_ = os.Remove(filepath.Join(s.dir, entry.Name()))
			}
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(s.dir, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var st callState
		if json.Unmarshal(raw, &st) != nil {
			continue
		}
		if st.Status != statusSettled && st.Status != statusUnreserved {
			continue
		}
		stamp := st.SettledAt
		if stamp == "" {
			stamp = st.At
		}
		at, err := time.Parse(time.RFC3339Nano, stamp)
		if err != nil || now.Sub(at) < stateRetention {
			continue
		}
		_ = os.Remove(path)
	}
}

func nowStamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }
