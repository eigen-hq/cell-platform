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
# ui_foreground: this script is the terminal's foreground job. Piped into
# sudo, it may run in the background of sudo's terminal, where the first
# touch of the terminal (stty in the ticker) stops it for good.
ui_foreground() {
  local pg tpg
  read -r pg tpg <<<"$(ps -o pgid=,tpgid= -p $$ 2>/dev/null)" || return 1
  [ -n "$tpg" ] && [ "$pg" = "$tpg" ]
}
if [ -t "$ui_fd" ] && [ -z "${NO_COLOR:-}" ] && [ "${TERM:-dumb}" != dumb ] && ui_foreground; then
  ui_tty=1 ui_dim=$'\033[2m' ui_bold=$'\033[1m' ui_ok=$'\033[32m' ui_err=$'\033[31m' ui_off=$'\033[0m' ui_cl=$'\r\033[K'
else
  ui_tty="" ui_dim="" ui_bold="" ui_ok="" ui_err="" ui_off="" ui_cl=""
fi
# A failing command names itself in the log (errexit alone says nothing).
# Scripts run under ui_start catch PIPE too (a trap does not pass to a new
# shell; HUP, ignored, does).
if [ -n "${CELL_UI_LOG:-}" ]; then
  set -E
  trap 'ui_failed $?' ERR
  trap : PIPE
fi
# The current step, shared by install.sh and the scripts it runs:
# "<start> <label>", or "!" once die reported a failure.
# Its siblings (.notes, .cmd, .progress, .lock) live with it in a private
# directory.
if [ -z "${CELL_STEP_FILE:-}" ]; then
  CELL_STEP_DIR=$(mktemp -d)
  CELL_STEP_FILE=$CELL_STEP_DIR/step
  : >"$CELL_STEP_FILE"
  export CELL_STEP_DIR CELL_STEP_FILE
fi

# A terminal that went away (SSH dropped) fails no step: the run goes on
# into the log.
ui() { printf "$@" >&"$ui_fd" 2>/dev/null || true; }

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
  sed "s/^/    $ui_dim/; s/\$/$ui_off/" "$CELL_STEP_FILE.notes" >&"$ui_fd" 2>/dev/null || true
  : >"$CELL_STEP_FILE.notes"
}

# log stays for the local cell's scripts: a step there.
log() { step "$@"; }

# fetch <url> <sha256> prints the file at url once it matches its pin in
# versions.env (E-65). Otherwise it prints nothing and fails: a pipe after
# it gets no part of a file that was changed or cut short.
fetch() {
  local f sum
  f=$(mktemp)
  if ! curl -fsSL --retry 3 -o "$f" "$1"; then
    rm -f "$f"
    die "could not download $1"
  fi
  sum=$( (sha256sum "$f" 2>/dev/null || shasum -a 256 "$f") | awk '{print $1}')
  if [ "$sum" != "$2" ]; then
    rm -f "$f"
    die "$1 is not the pinned file (sha256 $sum, pinned $2): refusing it"
  fi
  if ! cat "$f"; then
    rm -f "$f"
    die "could not pass on $1 (disk full?)"
  fi
  rm -f "$f"
}

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
  tail -n 15 "$CELL_UI_LOG" | sed 's/^/    /' >&"$ui_fd" 2>/dev/null || true
  ui '\n  Full log: %s\n' "$CELL_UI_LOG"
}

# ui_start <log>: from here on, command output goes to the log (appended,
# root only) and the terminal shows only the lines above. A command that
# fails without die still ends in a failed step with the log's end.
ui_start() {
  [ -z "${CELL_UI_LOG:-}" ] || return 0
  mkdir -p "$(dirname "$1")"
  # Root's log, never someone's link; an older one gets its mode fixed.
  [ ! -L "$1" ] || die "$1 is a symbolic link: remove it and run again"
  (umask 077 && touch "$1")
  chmod 600 "$1"
  export CELL_UI_LOG=$1
  # A closed SSH session or output pipe does not stop the run halfway; the
  # log shows it. HUP is ignored (the commands running then get it too),
  # PIPE only caught, so a write of this shell fails instead of killing it
  # and the commands it runs keep the default.
  trap '' HUP
  trap : PIPE
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
  if [ -n "${CELL_STEP_DIR:-}" ]; then rm -rf "$CELL_STEP_DIR"; else rm -f "$CELL_STEP_FILE" "$CELL_STEP_FILE".*; fi
  exit "$rc"
}
