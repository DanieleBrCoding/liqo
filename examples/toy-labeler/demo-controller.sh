#!/usr/bin/env bash
# Avvia (o ferma) il toy-labeler-controller su un provider e ne mostra i log.
# Uso: ./demo-controller.sh [provider1|provider2] [--restart|--stop]
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/demo-lib.sh"

CLUSTER="provider1"
ACTION="start"
for a in "$@"; do
  case "$a" in
    provider1|provider2) CLUSTER="$a" ;;
    --restart) ACTION="restart" ;;
    --stop) ACTION="stop" ;;
    *) err "Argomento sconosciuto: $a"; exit 1 ;;
  esac
done

need_cluster "$CLUSTER"

if [ "$ACTION" = "stop" ]; then
  kc "$CLUSTER" -n "$NS" delete deployment toy-labeler-controller --ignore-not-found
  ok "Controller fermato su $CLUSTER."
  exit 0
fi

kc "$CLUSTER" get namespace "$NS" >/dev/null 2>&1 \
  || { err "Namespace '$NS' assente su $CLUSTER: esegui il passo 5 di ./setup-linux.sh (offloading)."; exit 1; }

info "Avvio del controller su $CLUSTER..."
kc "$CLUSTER" apply -f "$CONTROLLER_MANIFEST" >/dev/null || { err "Apply del manifest fallito."; exit 1; }
[ "$ACTION" = "restart" ] && kc "$CLUSTER" -n "$NS" rollout restart deployment/toy-labeler-controller >/dev/null

if ! kc "$CLUSTER" -n "$NS" rollout status deployment/toy-labeler-controller --timeout=120s; then
  err "Il controller non e' partito. Se vedi ErrImageNeverPull, l'immagine non e' caricata nel cluster (passo 6 di ./setup-linux.sh)."
  kc "$CLUSTER" -n "$NS" get pods -l app=toy-labeler-controller
  exit 1
fi

ok "Controller attivo su $CLUSTER. Log in tempo reale (Ctrl+C per uscire, il controller resta attivo):"
kc "$CLUSTER" -n "$NS" logs -f deployment/toy-labeler-controller
