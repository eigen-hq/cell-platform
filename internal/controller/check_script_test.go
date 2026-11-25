package controller

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The target check passes only when every rclone call succeeds and the
// object reads back and shows in the listing.
func TestCheckScript(t *testing.T) {
	bin := t.TempDir()
	fake := `#!/bin/sh
case $1 in
rcat) cat >/dev/null ;;
cat) printf '%s\n' "${CAT_OUT-written by the cell to check its backup target; safe to delete}"; exit "${CAT_RC:-0}" ;;
lsf) printf '%s\n' "${LSF_OUT-.cell-check}"; exit "${LSF_RC:-0}" ;;
deletefile) ;;
*) exit 9 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "rclone"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		env  []string
		pass bool
	}{
		"all fine":          {nil, true},
		"read fails":        {[]string{"CAT_RC=1"}, false},
		"read differs":      {[]string{"CAT_OUT=other"}, false},
		"listing fails":     {[]string{"LSF_RC=1"}, false},
		"object not listed": {[]string{"LSF_OUT=else"}, false},
	} {
		cmd := exec.Command("sh", "-c", checkScript)
		cmd.Env = append([]string{"PATH=" + bin + ":/usr/bin:/bin", "ROOT=bucket/cell"}, tc.env...)
		out, err := cmd.CombinedOutput()
		if (err == nil) != tc.pass {
			t.Errorf("%s: err %v, output %s", name, err, out)
		}
	}
}
