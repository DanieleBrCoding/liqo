#!/usr/bin/env bash
# Lancia l'intera demo in una sessione tmux a 4 pannelli:
#   1. provider1: controller in esecuzione (log)
#   2. consumer : ascolto ICMP sui gateway (hub)
#   3. provider2: ascolto ICMP sui gateway (hub vs diretto)
#   4. provider1: ping verso i due percorsi, in loop
# Uso: ./demo-run-all.sh [--stop]
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/demo-lib.sh"

SESSION="liqo-demo"

if [ "${1:-}" = "--stop" ]; then
  tmux kill-session -t "$SESSION" 2>/dev/null && ok "Sessione '$SESSION' chiusa." || info "Nessuna sessione attiva."
  exit 0
fi

if ! command -v tmux >/dev/null 2>&1; then
  warn "tmux non trovato."
  read -r -p "Installare tmux con apt? [S/n] " ans </dev/tty
  case "${ans,,}" in
    n|no) err "Senza tmux apri 4 terminali e lancia a mano: demo-controller.sh, demo-listen.sh consumer, demo-listen.sh provider2, demo-ping.sh --loop"; exit 1 ;;
  esac
  sudo apt-get install -y tmux || { err "Installazione tmux fallita."; exit 1; }
fi

for c in consumer provider1 provider2; do need_cluster "$c"; done

tmux kill-session -t "$SESSION" 2>/dev/null
cd "$HERE" || exit 1
export FORCE_COLOR=1

P_CTRL="$(tmux new-session -d -s "$SESSION" -n demo -P -F '#{pane_id}' "./demo-controller.sh provider1")"
tmux set-option -t "$SESSION" remain-on-exit on
tmux set-option -t "$SESSION" pane-border-status top
P_CONS="$(tmux split-window -h -t "$P_CTRL" -P -F '#{pane_id}' "./demo-listen.sh consumer")"
P_PROV2="$(tmux split-window -v -t "$P_CTRL" -P -F '#{pane_id}' "./demo-listen.sh provider2")"
P_PING="$(tmux split-window -v -t "$P_CONS" -P -F '#{pane_id}' "./demo-ping.sh --loop")"
tmux select-layout -t "$SESSION:demo" tiled

tmux select-pane -t "$P_CTRL" -T "provider1: CONTROLLER (log)"
tmux select-pane -t "$P_CONS" -T "consumer: ascolto (hub)"
tmux select-pane -t "$P_PROV2" -T "provider2: ascolto (hub vs diretto)"
tmux select-pane -t "$P_PING" -T "provider1: PING"

info "Sessione tmux '$SESSION' avviata. Per uscire dal vista: Ctrl+B poi D. Per chiudere tutto: ./demo-run-all.sh --stop"
tmux attach -t "$SESSION"
