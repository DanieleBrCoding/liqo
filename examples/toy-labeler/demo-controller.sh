#!/usr/bin/env bash
# Avvia (o ferma) il toy-labeler-controller su un provider e ne mostra i log.
# Se lo stream si chiude, si riconnette e ristampa fino a 10 righe recenti.
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
if [ "$ACTION" = "restart" ]; then
  kc "$CLUSTER" -n "$NS" rollout restart deployment/toy-labeler-controller >/dev/null \
    || { err "Riavvio del controller fallito."; exit 1; }
fi

if ! kc "$CLUSTER" -n "$NS" rollout status deployment/toy-labeler-controller --timeout=120s; then
  err "Il controller non e' partito. Se vedi ErrImageNeverPull, l'immagine non e' caricata nel cluster (passo 7 di ./setup-linux.sh)."
  kc "$CLUSTER" -n "$NS" get pods -l app=toy-labeler-controller
  exit 1
fi

follow_controller_logs() {
  local status
  trap 'exit 130' INT
  trap 'exit 143' TERM
  while true; do
    kc "$CLUSTER" -n "$NS" logs -f deployment/toy-labeler-controller -c controller --tail=10
    status=$?
    if [ "$status" -eq 0 ]; then
      warn "Stream dei log chiuso; riconnessione tra 5 secondi."
    else
      err "Stream dei log interrotto (codice $status); riconnessione tra 5 secondi."
    fi
    sleep 5
  done
}

ok "Controller attivo su $CLUSTER. Log in tempo reale (Ctrl+C per uscire, il controller resta attivo):"
follow_controller_logs
