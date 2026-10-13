#!/bin/sh
# ARTEX supervisor launch script (Linux / macOS / Docker ENTRYPOINT)
#
# Usage:
#   ./start.sh                       run in foreground (Ctrl-C stops)
#   nohup ./start.sh >artex.log 2>&1 &   stay resident in background
#   ./start.sh -addr :9000           extra args are passed through to artex unchanged
#
# It does one thing: run artex, then decide whether to relaunch it based on the exit code.
#
#   0      clean user stop       -> exit the loop
#   75     program asked restart -> rerun immediately (the page's "one-click update" or "rollback" was used)
#   other  crash                 -> rerun with backoff (1->2->4... up to 60s)
#
# Deliberately no download, SHA256 verify, or swap logic here: that logic would need two copies (sh and bat),
# yet it is exactly the part that must never break — once a non-booting binary is swapped in, this script would
# faithfully relaunch it forever and only manual rescue on the machine could fix it. So verify/swap stays in Go (the selfupdate package),
# done by artex itself at startup; this script stays dumb.
set -u

cd "$(dirname "$0")" || exit 1

BIN=./artex
[ -x "$BIN" ] || { echo "[artex] executable not found: $BIN" >&2; exit 1; }

RESTART_CODE=75
MAX_DELAY=60

child=0
stopping=0

# Forward stop signals to the artex process itself.
#
# Required under Docker: docker stop sends SIGTERM only to PID 1 (i.e. this script),
# never to the child. Without forwarding, artex gets no signal, cannot shut down gracefully,
# and is SIGKILLed after 10s — running tasks die mid-flight.
forward() {
	stopping=1
	if [ "$child" -ne 0 ]; then
		kill -TERM "$child" 2>/dev/null || true
	fi
}
trap forward INT TERM

delay=1
while :; do
	"$BIN" "$@" &
	child=$!

	# A signal interrupts wait and makes it return >128. The child is still shutting down gracefully
	# at that point, so wait once more to get its real exit code.
	wait "$child"
	code=$?
	if [ "$code" -gt 128 ]; then
		wait "$child"
		code=$?
	fi
	child=0

	if [ "$stopping" -eq 1 ]; then
		echo "[artex] stopped"
		exit 0
	fi

	case "$code" in
		0)
			echo "[artex] exited normally"
			exit 0
			;;
		"$RESTART_CODE")
			# Update/rollback staged: on rerun artex completes the swap at startup (see selfupdate.Bootstrap).
			echo "[artex] restart requested (applying new version)..."
			delay=1
			;;
		*)
			echo "[artex] abnormal exit (code=$code), restarting in ${delay}s" >&2
			sleep "$delay"
			delay=$((delay * 2))
			[ "$delay" -gt "$MAX_DELAY" ] && delay=$MAX_DELAY
			;;
	esac
done
