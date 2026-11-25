package controller

import "testing"

// The backup gateway's auth proxy derives the same key in sh:
// printf '%s:%s' "$key" "$app" | sha256sum
func TestAppSecret(t *testing.T) {
	if got := (&backupTarget{GatewayKey: "gk"}).appSecret("notes"); got != "efe2d6c2964358676bd8fb3ca69af3256d04aa3fdfb761030246929a2844d53b" {
		t.Errorf("app secret %s", got)
	}
}
