# Terminal output of the installer, sourced by lib.sh and node.sh (the join
# script carries a copy). ui_start sends all command output to a log and
# keeps the terminal for progress lines; without it (make cell-up), output
# goes to stdout as is.
#
#   step "Identity"   ends the step before (done, its time) and starts this one
#   progress "text"   what the current step does right now, after its time
#   note "text"       a line under the current step
#   step_end          ends the last step
#   die "text"        marks the step failed, prints the error and the log's end

# Errors go to stderr without the log: a die inside $(...) must not vanish.
if [ -n "${CELL_UI_LOG:-}" ]; then ui_fd=3 ui_efd=3; else ui_fd=1 ui_efd=2; fi
if [ -t "$ui_fd" ] && [ -z "${NO_COLOR:-}" ] && [ "${TERM:-dumb}" != dumb ]; then
  ui_tty=1 ui_dim=$'\033[2m' ui_bold=$'\033[1m' ui_ok=$'\033[32m' ui_err=$'\033[31m' ui_off=$'\033[0m' ui_cl=$'\r\033[K'
else
  ui_tty="" ui_dim="" ui_bold="" ui_ok="" ui_err="" ui_off="" ui_cl=""
fi
# A failing command names itself in the log (errexit alone says nothing).
if [ -n "${CELL_UI_LOG:-}" ]; then
  set -E
  trap 'ui_failed $?' ERR
fi
# The current step, shared by install.sh and the scripts it runs:
# "<start> <label>", or "!" once die reported a failure.
if [ -z "${CELL_STEP_FILE:-}" ]; then
  CELL_STEP_FILE=$(mktemp)
  export CELL_STEP_FILE
fi

ui() { printf "$@" >&"$ui_fd"; }

ui_failed() {
  printf 'failed (exit %s): %s\n' "$1" "$BASH_COMMAND"
  # The first failure is the cause; the scripts around it fail after it.
  [ -s "$CELL_STEP_FILE.cmd" ] || printf '%s' "$BASH_COMMAND" >"$CELL_STEP_FILE.cmd"
}

# duration prints seconds as 9s or 4m 07s.
duration() {
  if [ "$1" -lt 60 ]; then printf '%ss' "$1"; else printf '%sm %02ds' $(($1 / 60)) $(($1 % 60)); fi
}

# On a terminal a ticker redraws the current step's line every second
# (spinner, time, progress); whoever writes that line holds this lock.
ui_locked() {
  if [ -n "${CELL_UI_TICK:-}" ]; then { flock 9; "$@"; } 9>>"$CELL_STEP_FILE.lock"; else "$@"; fi
}

step_end() { ui_locked ui_step_end; }

ui_step_end() {
  local start label
  read -r start label <"$CELL_STEP_FILE" 2>/dev/null || return 0
  [ -n "$label" ] || return 0
  # Without a terminal the step's start line stands for it.
  [ -z "$ui_tty" ] || ui '%s  %s✓%s %-40s %s%s%s\n' "$ui_cl" "$ui_ok" "$ui_off" "$label" "$ui_dim" "$(duration $(($(date +%s) - start)))" "$ui_off"
  ui_notes
  : >"$CELL_STEP_FILE"
  : >"$CELL_STEP_FILE.progress"
}

step() {
  step_end
  ui_locked ui_step "$@"
}

ui_step() {
  printf '%s %s\n' "$(date +%s)" "$*" >"$CELL_STEP_FILE"
  [ -z "${CELL_UI_LOG:-}" ] || printf '\n==> %s\n' "$*"
  if [ -n "$ui_tty" ]; then ui '%s  %s·%s %s' "$ui_cl" "$ui_dim" "$ui_off" "$*"; else ui '  - %s\n' "$*"; fi
}

progress() {
  [ -z "${CELL_UI_LOG:-}" ] || printf -- '--> %s\n' "$*"
  [ -z "$ui_tty" ] || printf '%s' "$*" >"$CELL_STEP_FILE.progress"
}

# On a terminal a note waits below the step's line until the step ends.
note() {
  [ -z "${CELL_UI_LOG:-}" ] || printf '%s\n' "$*"
  if [ -n "$ui_tty" ]; then printf '%s\n' "$*" >>"$CELL_STEP_FILE.notes"; else ui '    %s\n' "$*"; fi
}

ui_notes() {
  [ -s "$CELL_STEP_FILE.notes" ] || return 0
  sed "s/^/    $ui_dim/; s/\$/$ui_off/" "$CELL_STEP_FILE.notes" >&"$ui_fd"
  : >"$CELL_STEP_FILE.notes"
}

# log stays for the local cell's scripts: a step there.
log() { step "$@"; }

die() {
  ui_locked ui_die "$@"
  [ -z "${CELL_UI_LOG:-}" ] || printf 'error: %s\n' "$*"
  exit 1
}

ui_die() {
  local start label ui_fd=$ui_efd
  read -r start label <"$CELL_STEP_FILE" 2>/dev/null || label=""
  [ "$start" != "!" ] || return 0
  [ -z "$label" ] || ui '%s  %s✗%s %s\n' "$ui_cl" "$ui_err" "$ui_off" "$label"
  ui_notes
  ui '\n  %serror:%s %s\n' "$ui_err$ui_bold" "$ui_off" "$*"
  ui_log_tail
  echo "!" >"$CELL_STEP_FILE"
}

ui_log_tail() {
  [ -n "${CELL_UI_LOG:-}" ] && [ -s "$CELL_UI_LOG" ] || return 0
  ui '\n  %sLast lines of the log:%s\n' "$ui_dim" "$ui_off"
  tail -n 15 "$CELL_UI_LOG" | sed 's/^/    /' >&"$ui_fd"
  ui '\n  Full log: %s\n' "$CELL_UI_LOG"
}

# ui_start <log>: from here on, command output goes to the log (appended,
# root only) and the terminal shows only the lines above. A command that
# fails without die still ends in a failed step with the log's end.
ui_start() {
  [ -z "${CELL_UI_LOG:-}" ] || return 0
  mkdir -p "$(dirname "$1")"
  (umask 077 && touch "$1")
  export CELL_UI_LOG=$1
  exec 3>&1 >>"$CELL_UI_LOG" 2>&1
  ui_fd=3 ui_efd=3
  printf '\n===== %s %s\n' "$(date -u +%FT%TZ)" "$0"
  set -E
  trap 'ui_failed $?' ERR
  trap ui_exit EXIT
  if [ -n "$ui_tty" ] && command -v flock >/dev/null; then
    export CELL_UI_TICK=1
    (
      trap - ERR EXIT
      set +eE
      ui_tick "$$"
    ) </dev/null &
    ui_ticker=$!
  fi
}

# ui_tick <pid> redraws the current step's line every second while <pid>
# runs.
ui_tick() {
  local frames=(⠋ ⠙ ⠹ ⠸ ⠼ ⠴ ⠦ ⠧ ⠇ ⠏) i=0 cols start label
  cols=$(stty size </dev/tty 2>/dev/null | awk '{print $2}')
  [ "${cols:-0}" -gt 0 ] || cols=80
  while kill -0 "$1" 2>/dev/null; do
    {
      flock 9
      if read -r start label <"$CELL_STEP_FILE" 2>/dev/null && [ -n "$label" ] && [ "$start" != "!" ]; then
        ui_tick_line "$cols" "${frames[i % 10]}" "$label" "$(duration $(($(date +%s) - start)))" "$(cat "$CELL_STEP_FILE.progress" 2>/dev/null)"
      fi
    } 9>>"$CELL_STEP_FILE.lock"
    i=$((i + 1))
    sleep 1
  done
}

# ui_tick_line <cols> <frame> <label> <time> <detail> prints
# "  <frame> <label, padded to 40> <time>  <detail>" in at most cols - 1
# characters: a wrapped line could not be redrawn. The detail goes first,
# then the label's end.
ui_tick_line() {
  local cols=$1 frame=$2 label=$3 time=$4 detail=$5 width room
  width=$((${#label} > 40 ? ${#label} : 40))
  room=$((cols - 1 - 4 - width - 1 - ${#time} - 2))
  if [ "$room" -lt 8 ]; then
    detail=""
  elif [ "${#detail}" -gt "$room" ]; then
    detail="${detail:0:room-3}..."
  fi
  room=$((cols - 1 - 4 - 1 - ${#time}))
  [ "$room" -ge 1 ] || room=1
  [ "${#label}" -le "$room" ] || label=${label:0:room}
  ui '%s  %s%s%s %-*s %s%s%s%s' "$ui_cl" "$ui_dim" "$frame" "$ui_off" "$((room < 40 ? room : 40))" "$label" "$ui_dim" "$time" "${detail:+  $detail}" "$ui_off"
}

ui_exit() {
  local rc=$? start label
  if [ -n "${ui_ticker:-}" ]; then
    kill "$ui_ticker" 2>/dev/null || true
    wait "$ui_ticker" 2>/dev/null || true
  fi
  if [ "$rc" -ne 0 ]; then
    read -r start label <"$CELL_STEP_FILE" 2>/dev/null || label=""
    if [ "$start" != "!" ]; then
      [ -z "$label" ] || ui '%s  %s✗%s %s\n' "$ui_cl" "$ui_err" "$ui_off" "$label"
      ui_notes
      ui '\n  %serror:%s this command failed (exit %s):\n    %s\n' "$ui_err$ui_bold" "$ui_off" "$rc" "$(cut -c1-160 "$CELL_STEP_FILE.cmd" 2>/dev/null)"
      ui_log_tail
    fi
  fi
  rm -f "$CELL_STEP_FILE" "$CELL_STEP_FILE.notes" "$CELL_STEP_FILE.cmd" "$CELL_STEP_FILE.progress" "$CELL_STEP_FILE.lock"
  exit "$rc"
}
