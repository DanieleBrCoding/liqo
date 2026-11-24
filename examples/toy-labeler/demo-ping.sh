#!/usr/bin/env bash
# Genera richieste HTTP al Service, non ping agli indirizzi dei probe.
# Uso: ./demo-ping.sh [--loop]
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/demo-lib.sh"

LOOP=0
case "${1:-}" in
  "") ;;
  --loop) LOOP=1 ;;
  *) err "Uso: $0 [--loop]"; exit 1 ;;
esac
need_cluster provider1
kc provider1 -n "$NS" rollout status deployment/toy-labeler-controller --timeout=180s \
  || { err "Controller non pronto su provider1."; exit 1; }
kc provider1 -n "$NS" get service demo-server \
  || { err "Service demo-server non disponibile su provider1."; exit 1; }
while true; do
  printf '\n[%s] HTTP a demo-server.%s.svc.cluster.local:80\n' "$(date +%T)" "$NS"
  if ! kc provider1 -n "$NS" exec deployment/toy-labeler-controller -c controller -- \
    wget -T 5 -q -O - "http://demo-server.$NS.svc.cluster.local/"; then
    err "Richiesta HTTP fallita."
  fi
  [ "$LOOP" = 1 ] || break
  sleep 5
done
