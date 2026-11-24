//go:build e2e && dr

package e2e

import (
	"testing"
	"time"
)

// Through the API: an app is restored into a fresh cell
// from its backup, with complete data. Destroys and recreates the local
// cell; the backup bucket next to it survives, like a bucket at a
// provider. Run: make dr-test.
func TestRestoreIntoFreshCell(t *testing.T) {
	tc := newTestCell(t)
	admin := tc.adminAPI(t)
	admin.installed(t, "forgejo", 15*time.Minute)
	m := marker{t: t, app: "forgejo"}
	m.write("survives-the-cell")
	backup := admin.backup(t, "forgejo")

	t.Log("destroying the cell")
	run(t, "make", "cell-down")
	run(t, "make", "cell-up")

	tc = newTestCell(t) // new cell: new CA, new identity, new credentials
	admin = tc.adminAPI(t)
	admin.installed(t, "forgejo", 15*time.Minute)
	m = marker{t: t, app: "forgejo"}
	if db, _ := m.read(); db == "survives-the-cell" {
		t.Fatal("fresh cell already has the data")
	}
	// The fresh cell knows the backup only from the bucket: it is named.
	admin.restore(t, "forgejo", backup)
	m.expect("survives-the-cell")

	// The restored app works with the new cell's identity.
	const user, password = "e2e-user", "e2e-Passw0rd-not-secret"
	admin.account(t, user, password)
	br := tc.browser(t)
	base := "https://" + tc.cfg.Host("git")
	readBody(t, tc.login(t, br, base+"/user/oauth2/authentik", user, password))
	for _, ra := range referenceApps {
		if ra.name == "forgejo" {
			if got := ra.whoAmI(t, br, base); got != user {
				t.Fatalf("after restore: logged in as %q", got)
			}
		}
	}
}
