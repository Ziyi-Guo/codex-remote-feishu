// Package limitedgroups stores private quota metadata for restricted gateways.
package limitedgroups

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrQuota  = errors.New("limited-group daily quota exhausted")
	ErrLedger = errors.New("limited-group ledger unavailable")
)

const maxLedgerBytes = 32 << 20

// Usage contains owner-visible accounting metadata, never question/answer text.
type Usage struct {
	GatewayID      string    `json:"gateway_id"`
	UserID         string    `json:"user_id"`
	Date           string    `json:"date"`
	Timezone       string    `json:"timezone"`
	Used           int       `json:"used"`
	Limit          int       `json:"limit"`
	LastAcceptedAt time.Time `json:"last_accepted_at"`
}

type acceptance struct {
	GatewayID  string    `json:"gateway_id"`
	UserID     string    `json:"user_id"`
	MessageID  string    `json:"message_id"`
	AcceptedAt time.Time `json:"accepted_at"`
}

type ledgerState struct {
	Version  int          `json:"version"`
	Usage    []Usage      `json:"usage"`
	Accepted []acceptance `json:"accepted"`
}

var ledgerPathLocks sync.Map

// Ledger serializes gateway generations sharing a path in one daemon process.
// It does not provide a cross-process filesystem lock.
type Ledger struct {
	mu     *sync.Mutex
	path   string
	exists bool
	state  ledgerState
}

// OpenLedger validates existing state without creating or replacing anything.
// The parent directory must already exist. Missing state is created on accept.
func OpenLedger(path string) (*Ledger, error) {
	if strings.TrimSpace(path) == "" {
		return nil, ledgerError("empty path")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, ledgerError("path: %v", err)
	}
	if err := writableDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return nil, ledgerError("parent: %v", err)
	}
	path = filepath.Join(parent, filepath.Base(path))
	lock, _ := ledgerPathLocks.LoadOrStore(path, &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	state, exists, err := readState(path)
	if err != nil {
		return nil, err
	}
	return &Ledger{path: path, exists: exists, state: state, mu: mu}, nil
}

// Accept reserves one request before execution. Exact redeliveries do not spend
// quota, even on another day or after exhaustion. Dedupe is retained 30 days.
// The configured limit applies immediately; lowering it cannot refund usage.
func (l *Ledger) Accept(gatewayID, userID, messageID string, limit int, now time.Time, location *time.Location) (int, bool, error) {
	if !validID(gatewayID) || !validID(userID) || !validID(messageID) || limit < 1 || limit > 10000 || location == nil || !validID(location.String()) || now.IsZero() || now.Year() < 1 || now.Year() > 9999 {
		return 0, false, ledgerError("invalid accept arguments")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	state, exists, err := readState(l.path)
	if err != nil {
		return 0, false, err
	}
	if l.exists && !exists {
		return 0, false, ledgerError("state disappeared")
	}
	date := now.In(location).Format("2006-01-02")
	used, index := 0, -1
	for i, usage := range state.Usage {
		if usage.GatewayID == gatewayID && usage.UserID == userID && usage.Date == date {
			used, index = usage.Used, i
			break
		}
	}
	remaining := max(0, limit-used)
	for _, item := range state.Accepted {
		if item.GatewayID == gatewayID && item.UserID == userID && item.MessageID == messageID && !item.AcceptedAt.Before(now.Add(-30*24*time.Hour)) {
			l.state, l.exists = state, exists
			return remaining, true, nil
		}
	}
	if remaining == 0 {
		return 0, false, ErrQuota
	}
	if index < 0 {
		state.Usage = append(state.Usage, Usage{GatewayID: gatewayID, UserID: userID, Date: date})
		index = len(state.Usage) - 1
	}
	usage := &state.Usage[index]
	usage.Used++
	usage.Limit = limit
	usage.Timezone = location.String()
	usage.LastAcceptedAt = now.UTC()
	state.Accepted = append(state.Accepted, acceptance{gatewayID, userID, messageID, now.UTC()})
	state = retainedState(state, now)
	if err := persistState(l.path, state); err != nil {
		return 0, false, err
	}
	l.state, l.exists = state, true
	return remaining - 1, false, nil
}

// Snapshot returns a detached, deterministic view of the last verified state.
func (l *Ledger) Snapshot() []Usage {
	l.mu.Lock()
	defer l.mu.Unlock()
	result := append([]Usage{}, l.state.Usage...)
	sortUsage(result)
	return result
}

func validID(value string) bool {
	return len(value) <= 512 && utf8.ValidString(value) && strings.TrimSpace(value) == value && value != "" && strings.IndexFunc(value, unicode.IsControl) < 0
}

func ledgerError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrLedger, fmt.Sprintf(format, args...))
}

func writableDirectory(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return ledgerError("state directory: %v", err)
	}
	if !info.IsDir() || info.Mode().Perm()&0222 == 0 {
		return ledgerError("state directory is not writable")
	}
	return nil
}

func readState(path string) (ledgerState, bool, error) {
	empty := ledgerState{Version: 1, Usage: []Usage{}, Accepted: []acceptance{}}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return empty, false, nil
	}
	if err != nil {
		return empty, false, ledgerError("stat: %v", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0222 == 0 || info.Size() > maxLedgerBytes {
		return empty, true, ledgerError("invalid or unwritable state file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return empty, true, ledgerError("read: %v", err)
	}
	var state ledgerState
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return empty, true, ledgerError("decode: %v", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return empty, true, ledgerError("trailing state data")
	}
	if err := validateState(state); err != nil {
		return empty, true, err
	}
	return state, true, nil
}

func validateState(state ledgerState) error {
	if state.Version != 1 || state.Usage == nil || state.Accepted == nil {
		return ledgerError("invalid state version or structure")
	}
	usageKeys := map[[3]string]bool{}
	for _, usage := range state.Usage {
		key := [3]string{usage.GatewayID, usage.UserID, usage.Date}
		date, err := time.Parse("2006-01-02", usage.Date)
		if !validID(usage.GatewayID) || !validID(usage.UserID) || !validID(usage.Timezone) || err != nil || date.Format("2006-01-02") != usage.Date || usage.Used < 1 || usage.Used > 10000 || usage.Used > usage.Limit || usage.Limit < 1 || usage.Limit > 10000 || usage.LastAcceptedAt.IsZero() || usageKeys[key] {
			return ledgerError("invalid usage record")
		}
		usageKeys[key] = true
	}
	acceptedKeys := map[[3]string]bool{}
	for _, item := range state.Accepted {
		key := [3]string{item.GatewayID, item.UserID, item.MessageID}
		if !validID(item.GatewayID) || !validID(item.UserID) || !validID(item.MessageID) || item.AcceptedAt.IsZero() || acceptedKeys[key] {
			return ledgerError("invalid acceptance record")
		}
		acceptedKeys[key] = true
	}
	return nil
}

func retainedState(state ledgerState, now time.Time) ledgerState {
	result := ledgerState{Version: 1, Usage: []Usage{}, Accepted: []acceptance{}}
	// Date keys are local natural days. A one-day cushion avoids early removal
	// across timezones and DST while bounding usage history to about one year.
	cutoff := now.UTC().AddDate(0, 0, -366).Format("2006-01-02")
	for _, usage := range state.Usage {
		if usage.Date >= cutoff {
			result.Usage = append(result.Usage, usage)
		}
	}
	for _, item := range state.Accepted {
		if !item.AcceptedAt.Before(now.Add(-30 * 24 * time.Hour)) {
			result.Accepted = append(result.Accepted, item)
		}
	}
	sortUsage(result.Usage)
	sort.Slice(result.Accepted, func(i, j int) bool {
		a, b := result.Accepted[i], result.Accepted[j]
		if a.GatewayID != b.GatewayID {
			return a.GatewayID < b.GatewayID
		}
		if a.UserID != b.UserID {
			return a.UserID < b.UserID
		}
		return a.MessageID < b.MessageID
	})
	return result
}

func sortUsage(usage []Usage) {
	sort.Slice(usage, func(i, j int) bool {
		a, b := usage[i], usage[j]
		if a.GatewayID != b.GatewayID {
			return a.GatewayID < b.GatewayID
		}
		if a.UserID != b.UserID {
			return a.UserID < b.UserID
		}
		return a.Date < b.Date
	})
}

func persistState(path string, state ledgerState) error {
	dir := filepath.Dir(path)
	if err := writableDirectory(dir); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return ledgerError("encode: %v", err)
	}
	if len(data)+1 > maxLedgerBytes {
		return ledgerError("state capacity exceeded")
	}
	file, err := os.CreateTemp(dir, ".limited-group-*")
	if err != nil {
		return ledgerError("create state: %v", err)
	}
	defer os.Remove(file.Name())
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return ledgerError("secure state: %v", err)
	}
	// CreateTemp creates mode 0600. Sync before rename preserves the old state
	// on write failure and makes the complete replacement visible atomically.
	if _, err := file.Write(append(data, '\n')); err != nil {
		file.Close()
		return ledgerError("write state: %v", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return ledgerError("sync state: %v", err)
	}
	if err := file.Close(); err != nil {
		return ledgerError("close state: %v", err)
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return ledgerError("replace state: %v", err)
	}
	return nil
}
