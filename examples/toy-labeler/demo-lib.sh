#!/usr/bin/env bash
# Funzioni comuni degli script demo-*.sh (da includere con "source").

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NS="hub-spoke-demo"
DIRECT_SVC="pinger-direct-svc"
INDIRECT_SVC="pinger-indirect-svc"
CONTROLLER_MANIFEST="$HERE/manifests/toy-labeler-controller-deployment.yaml"

if [ -t 1 ] || [ -n "${FORCE_COLOR:-}" ]; then
  B=$'\e[34m'; G=$'\e[32m'; R=$'\e[31m'; Y=$'\e[33m'; C=$'\e[36m'; M=$'\e[35m'; N=$'\e[0m'
else
  B=; G=; R=; Y=; C=; M=; N=
fi
info() { echo "${B}[INFO]${N}  $*"; }
ok()   { echo "${G}[OK]${N}    $*"; }
warn() { echo "${Y}[WARN]${N}  $*"; }
err()  { echo "${R}[ERROR]${N} $*" >&2; }

kcfg() { echo "$HERE/liqo_kubeconf_$1"; }
kc()   { local c="$1"; shift; kubectl --kubeconfig "$(kcfg "$c")" "$@"; }

need_cluster() {
  [ -f "$(kcfg "$1")" ] || { err "Kubeconfig di '$1' non trovato: esegui prima ./setup-linux.sh"; exit 1; }
  kc "$1" get nodes >/dev/null 2>&1 || {
    err "Cluster '$1' non raggiungibile. Container fermi? Prova: docker start $1-control-plane $1-worker"
    exit 1
  }
}

# Stampa "namespace pod remote" per ogni pod gateway Liqo in esecuzione nel cluster $1.
# Il nome del pod e' gw-<cluster-remoto>-<hash-deployment>-<hash-pod>.
gateway_pods() {
  kc "$1" get pods -A --no-headers \
    -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name,PHASE:.status.phase 2>/dev/null \
    | awk '$2 ~ /^gw-/ && $3 == "Running" {print $1, $2}' \
    | while read -r ns pod; do
        echo "$ns $pod $(echo "$pod" | sed -E 's/^gw-//; s/-[a-z0-9]+-[a-z0-9]+$//')"
      done
}

SNIFF_CLUSTER=""
SNIFF_TARGETS=()

# Chiude i tcpdump rimasti nei pod e tutti i processi figli dello script
cleanup_sniff() {
  trap - INT TERM EXIT
  local t
  for t in "${SNIFF_TARGETS[@]:-}"; do
    [ -z "$t" ] && continue
    kc "$SNIFF_CLUSTER" exec -n "${t%% *}" "${t##* }" -- pkill tcpdump >/dev/null 2>&1 || true
  done
  kill 0 2>/dev/null || true
}

# Avvia tcpdump (solo ICMP echo-request in ingresso) su un gateway e prefissa ogni riga con un'etichetta.
# sniff_gateway <cluster> <namespace> <pod> <etichetta> <colore>
sniff_gateway() {
  local cluster="$1" ns="$2" pod="$3" tag="$4" color="$5"
  SNIFF_CLUSTER="$cluster"
  SNIFF_TARGETS+=("$ns $pod")
  (
    kc "$cluster" exec -n "$ns" "$pod" -- \
      tcpdump -nn -l -i any -Q in 'icmp and icmp[icmptype]==icmp-echo' 2>/dev/null \
      | while IFS= read -r line; do
          printf '%s %s[%s]%s %s\n' "$(date +%T)" "$color" "$tag" "$N" "$line"
        done
  ) &
}
