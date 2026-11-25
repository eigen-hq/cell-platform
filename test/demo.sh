#!/usr/bin/env bash
# The demo (docs/status.md) against the local cell. Every step is
# an end-to-end test that works through the platform API like a user; the
# output keeps what each step did. Step 1 (fresh VM, one command) is
# make vm-test; locally it is make cell-up, which make demo runs first.
set -euo pipefail
cd "$(dirname "$0")/.."

step() {
  printf '\n\033[1m==> %s\033[0m\n' "$1"
  go test -tags e2e -count=1 -timeout 30m -run "^$2\$" -v ./test/e2e/ |
    grep -E --line-buffered '^ +[a-z_]+_test\.go:[0-9]+: |^ *--- |^(ok|FAIL)'
}

step "2. Three apps, installed and usable with single sign-on" TestReferenceApps
step "3. A broken app: the ops agent finds the cause, the repair waits for approval" TestAgentRepairsApp
step "4. A failed update rolls back without data loss" TestFailedUpdateRollsBack
step "5. The ops agent packages an unknown app, the operator installs it" TestAgentPackagesApp
step "6. One MCP endpoint for the tools of the installed apps" TestMCPGateway

ca=.cell/cell-ca.crt
kubectl -n cert-manager get secret cell-ca -o jsonpath='{.data.ca\.crt}' | base64 -d >"$ca"
printf '\n\033[1m==> Try it yourself\033[0m\n'
kubectl get apps -o custom-columns='APP:.metadata.name,PHASE:.status.phase,URL:.status.url'
cat <<EOF

Log in as e2e-user (password e2e-Passw0rd-not-secret), created by step 2.
The browser needs the cell's CA: $ca.
The ops agent ran with a scripted model in these steps; with a key
(CELL_LLM_API_KEY at make cell-up) the agent in the cell works on its own.
EOF
