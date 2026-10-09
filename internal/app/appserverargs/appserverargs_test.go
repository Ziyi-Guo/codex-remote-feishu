package appserverargs

import "testing"

func TestFindSkipsRootFeatureOptions(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want Match
	}{
		{[]string{"--enable", "code_mode", "app-server"}, Match{ModeCodex, 2}},
		{[]string{"--disable", "shell_tool", "app-server"}, Match{ModeCodex, 2}},
		{[]string{"--enable=code_mode", "--disable=shell_tool", "app-server"}, Match{ModeCodex, 2}},
		{[]string{"--enable", "app-server", "--disable", "claude-app-server", "opencode-acp"}, Match{ModeOpenCode, 4}},
		{[]string{"--enable", "daemon", "claude-app-server"}, Match{ModeClaude, 2}},
		{[]string{"-c", "app-server", "--disable", "opencode-acp", "app-server"}, Match{ModeCodex, 4}},
	} {
		got, ok := Find(tc.args)
		if !ok || got != tc.want {
			t.Errorf("Find(%v) = %v, %v; want %v, true", tc.args, got, ok, tc.want)
		}
	}
}

func TestFindRejectsIncompleteRootFeatureOptions(t *testing.T) {
	for _, args := range [][]string{
		{"--enable"}, {"--disable"},
		{"--enable", "app-server"}, {"--disable", "claude-app-server"},
		{"--enable=", "app-server"}, {"--disable=", "app-server"},
		{"--unknown", "app-server"},
	} {
		if got, ok := Find(args); ok {
			t.Errorf("Find(%v) = %v, true; want no mode", args, got)
		}
	}
}
