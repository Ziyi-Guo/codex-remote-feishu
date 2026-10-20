package limitedgroups

import "testing"

func TestKnownReadCommandConfirmedRDSForms(t *testing.T) {
	for _, command := range []string{
		`bytedcli rds db list`,
		`bytedcli --json --site boe rds db list --region cn --page 1 --page-size 50`,
		`bytedcli -j rds db search "ip_gen" --region=cn`,
		`bytedcli rds db overview ip_gen --qps --no-topo`,
		`bytedcli rds db table list ip_gen --mode dbw --page 1`,
		`bytedcli rds db table schema ip_gen tasks --region boe`,
		`bytedcli rds db query ip_gen "SELECT id, status FROM tasks WHERE id = 'task_123' LIMIT 10" --region cn`,
		`bytedcli rds db query ip_gen 'select count(*), coalesce(status, 0) from tasks group by status order by status desc limit 20'`,
		`bytedcli rds db query ip_gen "SELECT upper(name) FROM tasks WHERE status IN ('ready', 'done') LIMIT 2 OFFSET 4"`,
		`bytedcli rds db query ip_gen "SELECT 1 LIMIT 1"`,
		`bytedcli rds db query ip_gen "SHOW TABLES"`,
		`bytedcli rds db query ip_gen "SHOW CREATE TABLE tasks"`,
		`bytedcli rds db query ip_gen "DESCRIBE tasks"`,
	} {
		t.Run(command, func(t *testing.T) {
			if !IsKnownReadCommand(command) {
				t.Fatal("confirmed literal read rejected")
			}
		})
	}
}

func TestKnownReadCommandRejectsShellAndUnconfirmedRouting(t *testing.T) {
	for _, command := range []string{
		"", `rds db list`, `/opt/homebrew/bin/bytedcli rds db list`, `env bytedcli rds db list`, `sh -c 'bytedcli rds db list'`,
		`X=1 bytedcli rds db list`, `bytedcli rds db list; echo x`, `bytedcli rds db list && echo x`,
		`bytedcli rds db list | cat`, `bytedcli rds db list > /tmp/out`, `bytedcli rds db list </tmp/in`,
		"bytedcli rds db list\nbytedcli rds db list", "bytedcli rds db list\r", "bytedcli rds db list\x00",
		"bytedcli rds db search `id`", `bytedcli rds db search $(id)`, `bytedcli rds db search "$HOME"`,
		`bytedcli rds db search '${HOME}'`, `bytedcli rds db list # comment`, `bytedcli rds db search *`,
		`bytedcli rds db search ?`, `bytedcli rds db search {a,b}`, `bytedcli rds db search [ab]`,
		`bytedcli rds db search ~/x`, `bytedcli rds db search <(id)`, `bytedcli rds db search foo\ bar`,
		`bytedcli rds db search 'unterminated`, `bytedcli rds db search "unterminated`,
		`bytedcli --yes rds db list`, `bytedcli --nomask rds db list`, `bytedcli --debug rds db list`,
		`bytedcli --site http://evil rds db list`, `bytedcli --site unconfirmed rds db list`,
		`bytedcli rds db list --base-url https://evil`, `bytedcli rds db list --yes`,
		`bytedcli rds db list --site boe`, `bytedcli rds db list --json`, `bytedcli rds db list --unknown value`,
		`bytedcli rds db list --region --yes`, `bytedcli rds db list --region https://evil`, `bytedcli rds db list --region attacker.internal`, `bytedcli rds db list --vregion attacker.internal`,
		`bytedcli rds db list --mode unsafe`, `bytedcli rds db list --page-size 0`,
		`bytedcli rds db list rds bpm create`, `bytedcli rds db list --`, `bytedcli rds db search --yes`,
		`bytedcli rds db query ip_gen 'SELECT 1 LIMIT 1' --confirm-risky-shard-query`,
		`bytedcli rds db query ip_gen 'SELECT 1 LIMIT 1' --sql 'DELETE FROM tasks'`,
		`bytedcli rds bpm list`, `bytedcli rds db get ip_gen`, `bytedcli log search`, `bytedcli log list`,
		`bytedcli log clusters`, `bytedcli log search-psm-log --psm demo --output-file /tmp/x`,
	} {
		t.Run(command, func(t *testing.T) {
			if IsKnownReadCommand(command) {
				t.Fatal("unsafe or unknown command auto-approved")
			}
		})
	}
}

func TestKnownReadCommandRejectsUnknownOrEffectfulSQL(t *testing.T) {
	for _, sql := range []string{
		"SELECT * FROM tasks", "SELECT * FROM tasks LIMIT 1001", "SELECT * FROM tasks LIMIT -1", "SELECT * FROM tasks LIMIT 0",
		"SELECT * FROM tasks LIMIT 1; DELETE FROM tasks", "SELECT * FROM tasks LIMIT 1;", "SELECT * FROM tasks LIMIT 1 -- x",
		"SELECT /* read */ id FROM tasks LIMIT 1", "SELECT /*!50000 INTO OUTFILE '/tmp/x' */ id FROM tasks LIMIT 1",
		"SELECT id FROM tasks INTO OUTFILE '/tmp/x' LIMIT 1", "SELECT id FROM tasks INTO DUMPFILE '/tmp/x' LIMIT 1",
		"SELECT id FROM tasks FOR UPDATE LIMIT 1", "SELECT id FROM tasks LOCK IN SHARE MODE LIMIT 1",
		"SELECT sleep(10) LIMIT 1", "SELECT benchmark(1000, 1) LIMIT 1", "SELECT load_file('/tmp/x') LIMIT 1",
		"SELECT get_lock('lock', 1) LIMIT 1", "SELECT release_lock('lock') LIMIT 1", "SELECT side_effect() LIMIT 1",
		"SELECT db.count(id) FROM tasks LIMIT 1", "SELECT count (side_effect()) FROM tasks LIMIT 1",
		"SELECT @x:=1 LIMIT 1", "SELECT nextval('sequence') LIMIT 1", "SELECT dblink_exec('db','DELETE FROM tasks') LIMIT 1",
		"SELECT id FROM tasks LIMIT 1 UNION SELECT side_effect()", "SELECT (SELECT id FROM tasks LIMIT 1) LIMIT 1",
		"SELECT id FROM tasks WHERE id IN (SELECT id FROM other) LIMIT 1", "WITH q AS (DELETE FROM tasks) SELECT 1 LIMIT 1",
		"CALL side_effect()", "UPDATE tasks SET status=1", "DELETE FROM tasks", "SELECT id FROM tasks LIMIT 1 PROCEDURE ANALYSE()",
		"SHOW CREATE PROCEDURE side_effect", "SHOW TABLES; DROP TABLE tasks", "DESCRIBE SELECT side_effect()", "EXPLAIN ANALYZE DELETE FROM tasks",
		"SELECT id FROM tasks LIMIT 1 nonsense", "SELECT id FROM tasks LIMIT 1 OFFSET -1", "SELECT id FROM tasks LIMIT 1 OFFSET 100001",
		"SELECT id FROM tasks WHERE id = 'unfinished LIMIT 1", "SELECT id FROM tasks LIMIT 1\\G",
	} {
		command := "bytedcli rds db query ip_gen \"" + sql + "\""
		t.Run(sql, func(t *testing.T) {
			if IsKnownReadCommand(command) {
				t.Fatal("SQL bypass auto-approved")
			}
		})
	}
}
