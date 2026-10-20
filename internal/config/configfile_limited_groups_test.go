package config

import (
	"encoding/json"
	"testing"
)

func TestFeishuAppConfigPreservesLimitedGroups(t *testing.T) {
	data := []byte(`{"id":"limited","appId":"cli_test","appSecret":"existing-secret","limitedGroups":[{"enabled":true,"chatId":"oc_limited","ownerUserId":"ou_owner","allowedUserIds":["ou_owner"],"workspace":"/workspace/limited","dailyLimit":25}]}`)
	var app FeishuAppConfig
	if err := json.Unmarshal(data, &app); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(app)
	if err != nil {
		t.Fatal(err)
	}
	var reread FeishuAppConfig
	if err := json.Unmarshal(encoded, &reread); err != nil {
		t.Fatal(err)
	}
	if reread.AppSecret != "existing-secret" || len(reread.LimitedGroups) != 1 || reread.LimitedGroups[0].Validate() != nil {
		t.Fatalf("policy/credential round trip failed: %s", encoded)
	}
}
