#!/usr/bin/env bash
# Esegue ping dal pod del controller (provider1) verso gli IP dei due percorsi:
# diretto (pinger-direct-svc) e indiretto (pinger-indirect-svc).
# Uso: ./demo-ping.sh [--count N] [--loop] [--cluster provider1|provider2]
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/demo-lib.sh"

COUNT=5
LOOP=0
CLUSTER="provider1"
while [ $# -gt 0 ]; do
  case "$1" in
    --count|-c) COUNT="$2"; shift ;;
    --loop) LOOP=1 ;;
    --cluster) CLUSTER="$2"; shift ;;
    *) err "Argomento sconosciuto: $1"; exit 1 ;;
  esac
  shift
done

need_cluster "$CLUSTER"
info "Attendo che il controller sia pronto su $CLUSTER..."
kc "$CLUSTER" -n "$NS" rollout status deployment/toy-labeler-controller --timeout=180s >/dev/null 2>&1 \
  || { err "Controller non pronto su $CLUSTER: avvialo con ./demo-controller.sh $CLUSTER"; exit 1; }

endpoint_ips() {
  kc "$CLUSTER" -n "$NS" get endpointslices -l "kubernetes.io/service-name=$1" \
    -o jsonpath='{range .items[*].endpoints[*]}{.addresses[0]}{"\n"}{end}' 2>/dev/null
}

ping_path() { # etichetta servizio colore
  local label="$1" svc="$2" color="$3" ip out
  local ips; ips="$(endpoint_ips "$svc")"
  if [ -z "$ips" ]; then
    warn "$label: nessun endpoint per $svc su $CLUSTER (EndpointSlice non ancora creati?)"
    return
  fi
  for ip in $ips; do
    echo "${color}--- ping $label -> $ip ($svc) ---${N}"
    out="$(kc "$CLUSTER" -n "$NS" exec deployment/toy-labeler-controller -- ping -c "$COUNT" -W 2 "$ip" 2>&1)"
    echo "$out" | tail -n 2
  done
}

while true; do
  echo "${C}===== $(date +%T) ping da $CLUSTER =====${N}"
  ping_path "DIRETTO" "$DIRECT_SVC" "$G"
  ping_path "INDIRETTO" "$INDIRECT_SVC" "$Y"
  [ "$LOOP" = 1 ] || break
  sleep 5
done
