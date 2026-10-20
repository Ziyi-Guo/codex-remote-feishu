package limitedgroups

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLedgerGatewayGenerationsShareQuota(t *testing.T) {
	first, path, now, zone := ledgerAt(t)
	ledgers := []*Ledger{first}
	for i := 0; i < 3; i++ {
		l, err := OpenLedger(path)
		if err != nil {
			t.Fatal(err)
		}
		ledgers = append(ledgers, l)
	}
	var accepted atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, duplicate, err := ledgers[i%len(ledgers)].Accept("gateway", "user", fmt.Sprint(i), 25, now, zone)
			if err == nil && !duplicate {
				accepted.Add(1)
			} else if err != nil && !errors.Is(err, ErrQuota) {
				t.Errorf("accept: %v", err)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	if accepted.Load() != 25 {
		t.Fatalf("gateway hot reload bypassed quota: accepted %d", accepted.Load())
	}
}

func ledgerAt(t *testing.T) (*Ledger, string, time.Time, *time.Location) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "quota.json")
	l, err := OpenLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	return l, path, time.Date(2026, 10, 20, 15, 59, 0, 0, time.UTC), time.FixedZone("Asia/Shanghai", 8*3600)
}

func TestLedgerQuotaDuplicateRestartAndNaturalDay(t *testing.T) {
	l, path, now, zone := ledgerAt(t)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("open created state: %v", err)
	}
	for i := 0; i < 2; i++ {
		left, duplicate, err := l.Accept("gateway", "user", fmt.Sprint(i), 2, now, zone)
		if err != nil || duplicate || left != 1-i {
			t.Fatalf("accept %d: %d %v %v", i, left, duplicate, err)
		}
	}
	if _, _, err := l.Accept("gateway", "user", "over", 2, now, zone); !errors.Is(err, ErrQuota) {
		t.Fatalf("quota: %v", err)
	}
	reopened, err := OpenLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	left, duplicate, err := reopened.Accept("gateway", "user", "0", 2, now, zone)
	if err != nil || !duplicate || left != 0 {
		t.Fatalf("duplicate at quota: %d %v %v", left, duplicate, err)
	}
	tomorrow := now.Add(2 * time.Minute)
	left, duplicate, err = reopened.Accept("gateway", "user", "0", 2, tomorrow, zone)
	if err != nil || !duplicate || left != 2 {
		t.Fatalf("cross-day duplicate: %d %v %v", left, duplicate, err)
	}
	left, duplicate, err = reopened.Accept("gateway", "user", "next", 2, tomorrow, zone)
	if err != nil || duplicate || left != 1 {
		t.Fatalf("new day: %d %v %v", left, duplicate, err)
	}
	for _, pair := range [][2]string{{"gateway", "other"}, {"other", "user"}} {
		if left, _, err := reopened.Accept(pair[0], pair[1], "0", 2, now, zone); err != nil || left != 1 {
			t.Fatalf("isolation: %d %v", left, err)
		}
	}
	usage := reopened.Snapshot()
	if len(usage) != 4 {
		t.Fatalf("usage rows: %+v", usage)
	}
	if usage[0].Date != "2026-10-20" || usage[0].Timezone != "Asia/Shanghai" || usage[0].Used != 1 || usage[0].LastAcceptedAt.IsZero() {
		t.Fatalf("snapshot: %+v", usage)
	}
	usage[0].Used = 999
	if reopened.Snapshot()[0].Used == 999 {
		t.Fatal("snapshot aliases ledger")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("file permissions: %v %v", info, err)
	}
}

func TestLedgerInvalidInputFailsClosed(t *testing.T) {
	l, path, now, zone := ledgerAt(t)
	tests := []struct {
		gateway, user, message string
		limit                  int
		zone                   *time.Location
	}{
		{"", "u", "m", 1, zone}, {"g", " ", "m", 1, zone}, {"g", "u", "", 1, zone},
		{"g", "u", "m\n", 1, zone}, {"g", "u", "m", 0, zone}, {"g", "u", "m", 10001, zone}, {"g", "u", "m", 1, nil}, {"g", "u", "m", 1, time.FixedZone("", 0)},
	}
	for _, tt := range tests {
		if _, _, err := l.Accept(tt.gateway, tt.user, tt.message, tt.limit, now, tt.zone); !errors.Is(err, ErrLedger) {
			t.Fatalf("invalid input: %+v: %v", tt, err)
		}
	}
	if len(l.Snapshot()) != 0 {
		t.Fatal("invalid accept changed usage")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("invalid accept wrote state: %v", err)
	}
	if _, err := OpenLedger(filepath.Join(t.TempDir(), "missing", "quota")); !errors.Is(err, ErrLedger) {
		t.Fatalf("missing directory: %v", err)
	}
}

func TestLedgerCorruptionAndUnwritableStateFailClosed(t *testing.T) {
	for _, data := range []string{"", "null", "{}", "{", `{"version":99,"usage":[],"accepted":[]}`, `{"version":1,"usage":[],"accepted":[],"extra":true}`} {
		t.Run(fmt.Sprintf("bad-%q", data), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "quota")
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := OpenLedger(path); !errors.Is(err, ErrLedger) {
				t.Fatalf("corrupt state: %v", err)
			}
			got, _ := os.ReadFile(path)
			if string(got) != data {
				t.Fatal("corrupt state replaced")
			}
		})
	}
	l, path, now, zone := ledgerAt(t)
	if _, _, err := l.Accept("g", "u", "m", 2, now, zone); err != nil {
		t.Fatal(err)
	}
	before := l.Snapshot()
	if err := os.Chmod(filepath.Dir(path), 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(filepath.Dir(path), 0700)
	if _, _, err := l.Accept("g", "u", "retry", 2, now, zone); !errors.Is(err, ErrLedger) {
		t.Fatalf("unwritable state: %v", err)
	}
	if !reflect.DeepEqual(before, l.Snapshot()) {
		t.Fatal("failed persistence advanced count")
	}
	if err := os.Chmod(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if left, duplicate, err := l.Accept("g", "u", "retry", 2, now, zone); err != nil || duplicate || left != 0 {
		t.Fatalf("retry after persistence failure: %d %v %v", left, duplicate, err)
	}
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.Accept("g", "u", "m", 2, now, zone); !errors.Is(err, ErrLedger) {
		t.Fatalf("runtime corruption bypass: %v", err)
	}
	if !reflect.DeepEqual(l.Snapshot()[0].Used, 2) {
		t.Fatal("runtime corruption changed cache")
	}
}

func TestLedgerConcurrentAccepts(t *testing.T) {
	l, path, now, zone := ledgerAt(t)
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 60; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, duplicate, err := l.Accept("g", "u", fmt.Sprint(i), 20, now, zone)
			if err == nil && !duplicate {
				accepted.Add(1)
			} else if !errors.Is(err, ErrQuota) {
				t.Errorf("accept: duplicate=%v err=%v", duplicate, err)
			}
		}(i)
	}
	wg.Wait()
	if accepted.Load() != 20 {
		t.Fatalf("accepted %d", accepted.Load())
	}
	reopened, err := OpenLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Snapshot(); len(got) != 1 || got[0].Used != 20 {
		t.Fatalf("persisted usage: %+v", got)
	}
}

func TestLedgerRetention(t *testing.T) {
	l, path, now, zone := ledgerAt(t)
	if _, _, err := l.Accept("g", "u", "ancient", 2, now.AddDate(-2, 0, 0), zone); err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.Accept("g", "u", "old-message", 2, now.AddDate(0, 0, -31), zone); err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.Accept("g", "u", "current", 2, now, zone); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.Snapshot()) != 2 {
		t.Fatalf("usage retention: %+v", reopened.Snapshot())
	}
	if left, duplicate, err := reopened.Accept("g", "u", "old-message", 2, now, zone); err != nil || duplicate || left != 0 {
		t.Fatalf("dedupe retention: %d %v %v", left, duplicate, err)
	}
}

func TestLedgerMissingCommittedStateFailsClosed(t *testing.T) {
	l, path, now, zone := ledgerAt(t)
	if _, _, err := l.Accept("g", "u", "m", 2, now, zone); err != nil {
		t.Fatal(err)
	}
	before := l.Snapshot()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.Accept("g", "u", "next", 2, now, zone); !errors.Is(err, ErrLedger) {
		t.Fatalf("disappeared state: %v", err)
	}
	if !reflect.DeepEqual(before, l.Snapshot()) {
		t.Fatal("disappeared state reset cache")
	}
}

func TestLedgerConcurrentDuplicatesSpendOnce(t *testing.T) {
	l, _, now, zone := ledgerAt(t)
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			left, duplicate, err := l.Accept("g", "u", "same", 1, now, zone)
			if err != nil || left != 0 {
				t.Errorf("duplicate accept: %d %v", left, err)
			}
			if !duplicate {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 || l.Snapshot()[0].Used != 1 {
		t.Fatalf("duplicate spent %d times", accepted.Load())
	}
}

func TestLedgerRejectsInvalidPersistedUsage(t *testing.T) {
	l, path, now, zone := ledgerAt(t)
	if _, _, err := l.Accept("g", "u", "m", 1, now, zone); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), `"used": 1`, `"used": 2`, 1))
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenLedger(path); !errors.Is(err, ErrLedger) {
		t.Fatalf("invalid count accepted: %v", err)
	}
}

func TestLedgerRejectsReadOnlyOrSymlinkState(t *testing.T) {
	l, path, now, zone := ledgerAt(t)
	if _, _, err := l.Accept("g", "u", "m", 1, now, zone); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0400); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenLedger(path); !errors.Is(err, ErrLedger) {
		t.Fatalf("read-only state: %v", err)
	}
	link := filepath.Join(filepath.Dir(path), "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenLedger(link); !errors.Is(err, ErrLedger) {
		t.Fatalf("symlink state: %v", err)
	}
}
