//go:build e2e && dr

package e2e

import (
	"testing"
	"time"
)

// M2 acceptance: an app is restored into a fresh cell from its backup, with
// complete data. Destroys and recreates the local cell; the backup bucket
// next to it survives, like a bucket at a provider. Run: make dr-test.
func TestRestoreIntoFreshCell(t *testing.T) {
	tc := newTestCell(t)
	install(t, tc, loadManifest(t, "forgejo"), 15*time.Minute)
	m := marker{t: t, app: "forgejo"}
	m.write("survives-the-cell")
	b := backupNow(t, "forgejo")

	t.Log("destroying the cell")
	run(t, "make", "cell-down")
	run(t, "make", "cell-up")

	tc = newTestCell(t) // new cell: new CA, new identity, new credentials
	install(t, tc, loadManifest(t, "forgejo"), 15*time.Minute)
	m = marker{t: t, app: "forgejo"}
	if db, _ := m.read(); db == "survives-the-cell" {
		t.Fatal("fresh cell already has the data")
	}
	restoreNow(t, "forgejo", b.Name)
	install(t, tc, loadManifest(t, "forgejo"), 15*time.Minute)
	m.expect("survives-the-cell")

	// The restored app works with the new cell's identity.
	const user, password = "e2e-user", "e2e-Passw0rd-not-secret"
	tc.user(t, user, password)
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
