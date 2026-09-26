package feishu

import "testing"

func TestMatchScopeRequirementTreatsChatAsChatReadonlySatisfier(t *testing.T) {
	scope, ok := MatchScopeRequirement("im:chat:readonly", "tenant", []AppScopeStatus{
		{ScopeName: "im:chat", ScopeType: "tenant", GrantStatus: 1},
	})
	if !ok || scope != "im:chat" {
		t.Fatalf("MatchScopeRequirement = %q, %v; want im:chat satisfier", scope, ok)
	}
}

func TestMatchScopeRequirementRequiresGrantedMatchingIdentity(t *testing.T) {
	for _, typ := range []string{"user", "", "unknown"} {
		t.Run(typ, func(t *testing.T) {
			if _, ok := MatchScopeRequirement("im:message", "tenant", []AppScopeStatus{{ScopeName: "im:message", ScopeType: typ, GrantStatus: 1}}); ok {
				t.Fatalf("%q grant must not satisfy tenant requirement", typ)
			}
		})
	}
	if _, ok := MatchScopeRequirement("im:message", "", []AppScopeStatus{{ScopeName: "im:message", ScopeType: "tenant", GrantStatus: 1}}); ok {
		t.Fatal("unknown requirement identity must not match")
	}
	if _, ok := MatchScopeRequirement("im:message", "tenant", []AppScopeStatus{{ScopeName: "im:message", ScopeType: "tenant", GrantStatus: 2}}); ok {
		t.Fatal("ungranted scope must not match")
	}
}

func TestMatchScopeRequirementOperationAlternativesAreDirectional(t *testing.T) {
	for _, test := range []struct {
		required, granted string
		want              bool
	}{
		{"im:chat:readonly", "im:chat:read", true},
		{"im:chat:read", "im:chat", true},
		{"space:document:retrieve", "drive:drive:readonly", true},
		{"space:folder:create", "drive:drive", true},
		{"base:app:read", "bitable:app:readonly", true},
		{"base:record:update", "bitable:app", true},
		{"drive:drive", "space:folder:create", false},
		{"bitable:app", "base:record:update", false},
		{"application:application:self_manage", "admin:app.info:readonly", false},
	} {
		_, ok := MatchScopeRequirement(test.required, "tenant", []AppScopeStatus{{ScopeName: test.granted, ScopeType: "tenant", GrantStatus: 1}})
		if ok != test.want {
			t.Errorf("%s satisfied by %s = %v, want %v", test.required, test.granted, ok, test.want)
		}
	}
}
