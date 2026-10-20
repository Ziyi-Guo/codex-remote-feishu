package limitedgroups

import "testing"

func TestReadApprovalUsesCompleteNativeCommand(t *testing.T) {
	for _, c := range []string{`bytedcli --json rds db list --region cn`, `/bin/bash -c 'bytedcli --json rds db table schema ip_fun content --region cn'`, `/bin/zsh -lc 'bytedcli rds db query ip_fun "SELECT name FROM content LIMIT 1" --region cn --mode legacy'`} {
		if !IsKnownReadApprovalCommand(c) {
			t.Errorf("read request rejected: %s", c)
		}
	}
	for _, c := range []string{`/bin/bash -c 'bytedcli rds db list; touch /tmp/x'`, `/bin/bash -c 'bytedcli rds db list' && touch /tmp/x`, `/bin/bash -c 'bytedcli rds db list' extra`, `/tmp/bash -c 'bytedcli rds db list'`, `/bin/bash -ic 'bytedcli rds db list'`, `/bin/bash -c 'env bytedcli rds db list'`, `/bin/bash -c 'bytedcli rds db query ip_fun "DELETE FROM content"'`, `/bin/bash -c 'bytedcli rds db query ip_fun "SELECT name FROM content LIMIT 1" --output /tmp/x'`, `curl -X POST http://localhost/write`, `/bin/bash -c 'bytedcli rds db list | bash'`} {
		if IsKnownReadApprovalCommand(c) {
			t.Errorf("write or unknown request approved: %s", c)
		}
	}
}

func TestReadApprovalAcceptsNativeEscapedSQLQuotes(t *testing.T) {
	for _, c := range []string{
		`/bin/bash -lc "bytedcli --json rds db query ip_fun \"SHOW TABLES LIKE '%show%'\""`,
		`/bin/bash -lc "bytedcli rds db query ip_fun \"SELECT name, account_id FROM shows WHERE show_uuid = 'show_123' LIMIT 1\""`,
	} {
		if !IsKnownReadApprovalCommand(c) {
			t.Errorf("native read rejected: %s", c)
		}
	}
	for _, c := range []string{
		`/bin/bash -lc "bytedcli rds db query ip_fun \"SELECT 1 LIMIT 1\"; touch /tmp/x"`,
		`/bin/bash -lc "bytedcli rds db query ip_fun \"SELECT $(id) LIMIT 1\""`,
		`/bin/bash -lc "bytedcli rds db query ip_fun \"SELECT 1 LIMIT 1\" && touch /tmp/x"`,
		`/bin/bash -lc "bytedcli rds db query ip_fun \"DELETE FROM shows\""`,
		`/bin/bash -lc "bytedcli rds db query ip_fun \"SELECT 1 LIMIT 1\"\n touch /tmp/x"`,
		`/bin/bash -lc "bytedcli rds db list\\"`,
	} {
		if IsKnownReadApprovalCommand(c) {
			t.Errorf("unsafe native command approved: %s", c)
		}
	}
}

func TestReadApprovalAllowsReadHelpAndNoAutoUpgrade(t *testing.T) {
	for _, c := range []string{
		`/bin/bash -lc 'bytedcli --no-auto-upgrade rds db query --help'`,
		`bytedcli --no-auto-upgrade --json rds db table schema ip_fun show_info`,
		`bytedcli --no-auto-upgrade rds db query ip_fun "SELECT name FROM show_info LIMIT 1"`,
		`bytedcli rds db query -h`,
		`bytedcli --no-auto-upgrade --help`,
		`bytedcli --help`,
		`bytedcli --no-auto-upgrade rds --help`,
		`bytedcli rds db table schema ip_fun show_info --help`,
		`bytedcli rds db table schema --help`,
		`bytedcli rds db --help`,
	} {
		if !IsKnownReadApprovalCommand(c) {
			t.Errorf("read metadata/query rejected: %s", c)
		}
	}
	for _, c := range []string{
		`bytedcli --no-auto-upgrade rds db query ip_fun "DELETE FROM show_info"`,
		`bytedcli --no-auto-upgrade rds bpm create --help`,
		`bytedcli --no-auto-upgrade rds db query --help; touch /tmp/x`,
		`bytedcli --no-auto-upgrade=false rds db query --help`,
		`bytedcli rds db query --help --output /tmp/x`,
		`bytedcli rds db query ip_fun "DELETE FROM show_info" --help`,
		`bytedcli --no-auto-upgrade --no-auto-upgrade rds db list`,
		`bytedcli rds db unknown --help`,
	} {
		if IsKnownReadApprovalCommand(c) {
			t.Errorf("unknown or write request approved: %s", c)
		}
	}
}

func TestReadApprovalBatchRequiresEveryCommandToBeKnownRead(t *testing.T) {
	for _, c := range []string{
		`/bin/bash -c 'bytedcli --no-auto-upgrade rds db query --help; bytedcli --no-auto-upgrade --json rds db table schema ip_fun show --mode dbw'`,
		`bytedcli rds db list && bytedcli rds db table list ip_fun`,
		`/bin/bash -lc "bytedcli rds db query ip_fun \"SELECT name FROM show_info WHERE name = 'a;b' LIMIT 1\"; bytedcli rds db list"`,
	} {
		if !IsKnownReadApprovalCommand(c) {
			t.Errorf("readonly batch rejected: %s", c)
		}
	}
	for _, c := range []string{
		`bytedcli rds db list; touch /tmp/x`,
		`bytedcli rds db list && bytedcli rds db query ip_fun "DELETE FROM show_info"`,
		`bytedcli rds db list; bytedcli rds db table schema ip_fun show; curl -X POST http://localhost/write`,
		`bytedcli rds db list || bytedcli rds db table list ip_fun`,
		`bytedcli rds db list & bytedcli rds db table list ip_fun`,
		`bytedcli rds db list | bytedcli rds db table list ip_fun`,
		`bytedcli rds db list;`, `;bytedcli rds db list`,
		`bytedcli rds db list;;bytedcli rds db list`,
		`/bin/bash -c 'bytedcli rds db list; bytedcli rds db list' extra`,
		`bytedcli rds db list; bytedcli rds db search "$(touch /tmp/x)"`,
	} {
		if IsKnownReadApprovalCommand(c) {
			t.Errorf("effectful or unknown batch approved: %s", c)
		}
	}
}

func TestReadApprovalRealNativeFrames(t *testing.T) {
	for _, c := range []string{
		"/bin/bash -c 'bytedcli --no-auto-upgrade rds db query --help; bytedcli --no-auto-upgrade --json rds db table schema ip_fun show --mode dbw'",
		"/bin/bash -c \"bytedcli --no-auto-upgrade --json rds db query ip_fun 'SHOW CREATE TABLE \"'`show`'\"' --region cn\"",
	} {
		if !IsKnownReadApprovalCommand(c) {
			t.Errorf("real native read rejected: %s", c)
		}
	}
}

func TestReadApprovalRejectsShellExecutionAndMixedBatch(t *testing.T) {
	for _, c := range []string{
		`/bin/bash -c 'bytedcli rds db list; bytedcli rds db list > /tmp/x'`,
		`/bin/bash -c 'bytedcli rds db list; X=1 bytedcli rds db list'`,
		`/bin/bash -c 'bytedcli rds db list; (bytedcli rds db list)'`,
		`/bin/bash -c 'bytedcli rds db list; { bytedcli rds db list; }'`,
		`/bin/bash -c 'bytedcli rds db list; eval bytedcli rds db list'`,
		`/bin/bash -c 'bytedcli rds db list; /bin/bash -c "bytedcli rds db list"'`,
		`bytedcli rds db list; bytedcli rds db query ip_fun "SELECT load_file('/tmp/x') LIMIT 1"`,
		`bytedcli rds db list; bytedcli rds db query ip_fun "SHOW CREATE TABLE x; DELETE FROM x"`,
		`bytedcli rds db query ip_fun "SELECT 1 LIMIT $N"`,
		`bytedcli rds db query ip_fun "SELECT $((1+1)) LIMIT 1"`,
		"bytedcli rds db query ip_fun \"SELECT `touch /tmp/x` LIMIT 1\"",
		`bytedcli rds db list; bytedcli rds db search <(touch /tmp/x)`,
		`! bytedcli rds db list`,
		`bytedcli rds db list &`,
		`bytedcli rds db list # read`,
		`bytedcli rds db list; sleep 1`,
	} {
		if IsKnownReadApprovalCommand(c) {
			t.Errorf("effectful shell approved: %s", c)
		}
	}
}
