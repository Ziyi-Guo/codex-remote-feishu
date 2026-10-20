package limitedgroups

import (
	"path/filepath"
	"testing"
)

func TestPolicyOnlyAdmitsConfiguredGroupAndRealPeople(t *testing.T) {
	p := Policy{Enabled: true, ChatID: "oc_trial", OwnerUserID: "ou_owner", AllowedUserIDs: []string{"ou_owner", "ou_pan"}, Workspace: t.TempDir(), DailyLimit: 25}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if !p.Allows("oc_trial", "ou_pan") || !p.Allows("oc_trial", "ou_owner") {
		t.Fatal("configured people denied")
	}
	if p.Allows("oc_other", "ou_pan") || p.Allows("oc_trial", "ou_other") || p.Allows("oc_trial", "") {
		t.Fatal("unconfigured principal admitted")
	}
	p.Enabled = false
	if p.Allows("oc_trial", "ou_pan") {
		t.Fatal("disabled group admitted")
	}
}

func TestPolicyRejectsInvalidAuthorityAndLimits(t *testing.T) {
	base := Policy{Enabled: true, ChatID: "oc_trial", OwnerUserID: "ou_owner", AllowedUserIDs: []string{"ou_owner", "ou_pan"}, Workspace: t.TempDir(), DailyLimit: 25}
	for name, change := range map[string]func(*Policy){
		"empty group":        func(p *Policy) { p.ChatID = "" },
		"wildcard group":     func(p *Policy) { p.ChatID = "*" },
		"empty owner":        func(p *Policy) { p.OwnerUserID = "" },
		"owner absent":       func(p *Policy) { p.AllowedUserIDs = []string{"ou_pan"} },
		"wildcard user":      func(p *Policy) { p.AllowedUserIDs = []string{"ou_owner", "*"} },
		"root workspace":     func(p *Policy) { p.Workspace = filepath.VolumeName(p.Workspace) + string(filepath.Separator) },
		"relative workspace": func(p *Policy) { p.Workspace = "project" },
		"no limit":           func(p *Policy) { p.DailyLimit = 0 },
		"override invalid":   func(p *Policy) { p.UserDailyLimits = map[string]int{"ou_pan": 0} },
		"override outsider":  func(p *Policy) { p.UserDailyLimits = map[string]int{"ou_other": 50} },
		"unknown timezone":   func(p *Policy) { p.Timezone = "Unknown/Zone" },
	} {
		t.Run(name, func(t *testing.T) {
			p := base
			change(&p)
			if p.Validate() == nil || p.Allows("oc_trial", "ou_pan") {
				t.Fatal("unsafe policy accepted")
			}
		})
	}
	base.UserDailyLimits = map[string]int{"ou_pan": 50}
	if base.Validate() != nil || base.Limit("ou_pan") != 50 || base.Limit("ou_owner") != 25 || base.LocationName() != "Asia/Shanghai" {
		t.Fatal("configured quota not preserved")
	}
}

func TestDisabledPolicyStillReservesExactGroup(t *testing.T) {
	p := Policy{ChatID: "oc_trial"}
	if p.Validate() != nil || p.Allows("oc_trial", "ou_owner") {
		t.Fatal("disabled standby must remain closed")
	}
	p.ChatID = "*"
	if p.Validate() == nil {
		t.Fatal("standby wildcard accepted")
	}
}
