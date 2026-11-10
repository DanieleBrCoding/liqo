#!/usr/bin/env bash
# ============================================================
# setup-linux.sh - Setup completo e interattivo per Linux.
#
# Sostituisce (in un unico file) i vecchi script .ps1:
#   setup-liqo-minimal.ps1, post-setup-direct-path.ps1,
#   deploy-latency-pinger.ps1, deploy-toy-labeler-controller.ps1
#
# Cosa fa (ogni passo chiede conferma; se la risorsa esiste gia'
# chiede se reinstallarla/riconfigurarla da zero):
#   1. Installa le utilities: docker, kubectl, kind, liqoctl, go
#   2. Crea 3 cluster kind (consumer, provider1, provider2),
#      installa Liqo e fa il peering consumer <-> provider
#   3. Builda le immagini Liqo dal branch locale e le carica nei cluster
#   4. (opz.) Tunnel diretto provider1 <-> provider2
#   5. Offloading del namespace e deploy del latency-pinger
#   6. Builda e carica l'immagine del toy-labeler-controller nei provider
#
# Uso:  ./setup-linux.sh [--yes] [--direct-tunnel]
#   --yes            risponde "si'" alle domande con default si' e non
#                    reinstalla/riconfigura nulla di gia' esistente
#   --direct-tunnel  abilita il tunnel diretto provider1 <-> provider2
# ============================================================
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
MANIFESTS="$HERE/manifests"

LIQO_VERSION="v1.0.1"
PROM_CRDS_URL="https://github.com/prometheus-operator/prometheus-operator/releases/download/v0.70.0/stripped-down-crds.yaml"
NAMESPACE="hub-spoke-demo"
CONTROLLER_IMAGE="localhost:5000/toy-labeler-controller:latest"
LOCAL_COMPONENTS=(virtual-kubelet liqo-controller-manager)
CLUSTERS=(consumer provider1 provider2)
PROVIDERS=(provider1 provider2)

ASSUME_YES=0
DIRECT_TUNNEL=0
for arg in "$@"; do
  case "$arg" in
    --yes|-y) ASSUME_YES=1 ;;
    --direct-tunnel) DIRECT_TUNNEL=1 ;;
    -h|--help) sed -n '2,25p' "${BASH_SOURCE[0]}"; exit 0 ;;
    *) echo "Argomento sconosciuto: $arg"; exit 1 ;;
  esac
done

# ---------- output ----------
if [ -t 1 ]; then B=$'\e[34m'; G=$'\e[32m'; R=$'\e[31m'; Y=$'\e[33m'; C=$'\e[36m'; N=$'\e[0m'; else B=; G=; R=; Y=; C=; N=; fi
info()  { echo "${B}[INFO]${N}    $*"; }
ok()    { echo "${G}[SUCCESS]${N} $*"; }
warn()  { echo "${Y}[WARN]${N}    $*"; }
err()   { echo "${R}[ERROR]${N}   $*" >&2; }
step()  { echo; echo "${C}========================================${N}"; echo "${C}  $*${N}"; echo "${C}========================================${N}"; }
die()   { err "$*"; exit 1; }

# ask "domanda" default(y|n)  -> 0 se si'
ask() {
  local q="$1" def="${2:-y}" hint ans
  [ "$def" = "y" ] && hint="[S/n]" || hint="[s/N]"
  if [ "$ASSUME_YES" = 1 ]; then
    [ "$def" = "y" ] && { info "$q -> si' (--yes)"; return 0; } || { info "$q -> no (--yes)"; return 1; }
  fi
  while true; do
    read -r -p "${Y}?${N} $q $hint " ans </dev/tty || ans=""
    ans="${ans,,}"
    [ -z "$ans" ] && ans="$def"
    case "$ans" in s|si|y|yes) return 0 ;; n|no) return 1 ;; esac
  done
}

has() { command -v "$1" >/dev/null 2>&1; }

# ---------- sistema ----------
[ "$(uname -s)" = "Linux" ] || die "Questo script e' pensato per Linux."
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "Architettura non supportata: $(uname -m)" ;;
esac
SUDO=""
if [ "$(id -u)" -ne 0 ]; then
  has sudo || die "Serve sudo (o esegui da root) per installare le utilities."
  SUDO="sudo"
fi
has curl || die "curl non trovato: installalo (es. sudo apt install curl)."

KC_DIR="$HERE"
kc() { echo "$KC_DIR/liqo_kubeconf_$1"; }

# Se l'utente e' nel gruppo docker ma la sessione corrente non lo sa ancora
# (tipico subito dopo l'installazione), rilancia lo script dentro `sg docker`.
docker_ok() { docker info >/dev/null 2>&1; }
ensure_docker_access() {
  has docker || return 0
  docker_ok && return 0
  if [ -z "${SETUP_SG_DONE:-}" ] && id -nG "$USER" | tr ' ' '\n' | grep -qx docker && has sg; then
    info "Rilancio lo script con il gruppo 'docker' attivo..."
    SETUP_SG_DONE=1 exec sg docker -c "$(printf '%q ' "${BASH_SOURCE[0]}" "$@")"
  fi
}

# ============================================================
# 1. UTILITIES
# ============================================================
install_docker() {
  info "Installazione Docker Engine (script ufficiale get.docker.com)..."
  curl -fsSL https://get.docker.com | $SUDO sh || die "Installazione Docker fallita."
  $SUDO systemctl enable --now docker 2>/dev/null || $SUDO service docker start 2>/dev/null || true
  if [ "$(id -u)" -ne 0 ]; then
    $SUDO usermod -aG docker "$USER" && info "Utente '$USER' aggiunto al gruppo docker."
  fi
}
uninstall_docker() {
  warn "Rimozione Docker Engine (immagini/container locali non vengono cancellati da /var/lib/docker)."
  if has apt-get; then $SUDO apt-get remove -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin docker.io 2>/dev/null || true
  elif has dnf; then $SUDO dnf remove -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin 2>/dev/null || true
  elif has yum; then $SUDO yum remove -y docker-ce docker-ce-cli containerd.io 2>/dev/null || true
  elif has pacman; then $SUDO pacman -Rns --noconfirm docker 2>/dev/null || true
  fi
}

install_kubectl() {
  local v; v="$(curl -fsSL https://dl.k8s.io/release/stable.txt)" || die "Impossibile ottenere la versione di kubectl."
  info "Installazione kubectl $v..."
  curl -fsSLo /tmp/kubectl "https://dl.k8s.io/release/$v/bin/linux/$ARCH/kubectl" || die "Download kubectl fallito."
  $SUDO install -m 0755 /tmp/kubectl /usr/local/bin/kubectl && rm -f /tmp/kubectl
}

install_kind() {
  local v="v0.23.0"
  info "Installazione kind $v..."
  curl -fsSLo /tmp/kind "https://kind.sigs.k8s.io/dl/$v/kind-linux-$ARCH" || die "Download kind fallito."
  $SUDO install -m 0755 /tmp/kind /usr/local/bin/kind && rm -f /tmp/kind
}

install_liqoctl() {
  info "Installazione liqoctl $LIQO_VERSION..."
  curl -fsSL "https://github.com/liqotech/liqo/releases/download/$LIQO_VERSION/liqoctl-linux-$ARCH.tar.gz" -o /tmp/liqoctl.tar.gz \
    || die "Download liqoctl fallito."
  tar -xzf /tmp/liqoctl.tar.gz -C /tmp liqoctl && $SUDO install -m 0755 /tmp/liqoctl /usr/local/bin/liqoctl
  rm -f /tmp/liqoctl /tmp/liqoctl.tar.gz
}

install_go() {
  local v; v="$(awk '/^go /{print $2; exit}' "$ROOT/go.mod")"
  info "Installazione Go $v in /usr/local/go..."
  curl -fsSL "https://go.dev/dl/go${v}.linux-${ARCH}.tar.gz" -o /tmp/go.tgz || die "Download Go fallito."
  $SUDO rm -rf /usr/local/go && $SUDO tar -C /usr/local -xzf /tmp/go.tgz && rm -f /tmp/go.tgz
  $SUDO ln -sf /usr/local/go/bin/go /usr/local/bin/go
  $SUDO ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
}

uninstall_bin() { $SUDO rm -f "/usr/local/bin/$1"; }

# util_step nome comando_versione install_fn [uninstall_fn]
util_step() {
  local name="$1" vercmd="$2" inst="$3" uninst="${4:-}"
  if has "$name"; then
    info "$name gia' presente: $(eval "$vercmd" 2>&1 | head -n1)"
    if ask "Vuoi reinstallare $name da zero?" n; then
      if [ -n "$uninst" ]; then "$uninst"; else uninstall_bin "$name"; fi
      "$inst"
    fi
  else
    warn "$name non trovato."
    if ask "Installare $name?" y; then "$inst"; else warn "$name saltato (potrebbe servire piu' avanti)."; fi
  fi
}

step_utilities() {
  step "STEP 1: Utilities (docker, kubectl, kind, liqoctl, go)"
  util_step docker  "docker --version"                     install_docker uninstall_docker
  util_step kubectl "kubectl version --client"             install_kubectl
  util_step kind    "kind version"                         install_kind
  util_step liqoctl "liqoctl version --client"             install_liqoctl
  util_step go      "go version"                           install_go
}

check_prereqs() {
  local missing=0 c
  for c in docker kubectl kind liqoctl go; do
    has "$c" || { err "$c non trovato."; missing=1; }
  done
  [ "$missing" = 0 ] || die "Prerequisiti mancanti: riesegui lo script e accetta l'installazione."
  docker_ok || die "Docker non raggiungibile (daemon spento o utente senza permessi: esegui 'newgrp docker' o rifai il login)."
}

# ============================================================
# 2. CLUSTER + LIQO + PEERING
# ============================================================
cluster_exists() { kind get clusters 2>/dev/null | grep -qx "$1"; }

liqo_installed() { kubectl --kubeconfig "$(kc "$1")" get deployment liqo-controller-manager -n liqo >/dev/null 2>&1; }

peered() { # consumer ha un nodo virtuale per $1
  kubectl --kubeconfig "$(kc consumer)" get nodes -l liqo.io/type=virtual-node -o name 2>/dev/null | grep -q "$1"
}

tune_inotify() {
  # kind con 3 cluster esaurisce facilmente gli inotify di default
  local inst watches
  inst="$(cat /proc/sys/fs/inotify/max_user_instances 2>/dev/null || echo 0)"
  watches="$(cat /proc/sys/fs/inotify/max_user_watches 2>/dev/null || echo 0)"
  if [ "$inst" -lt 512 ] || [ "$watches" -lt 524288 ]; then
    if ask "I limiti inotify ($inst/$watches) sono bassi per 3 cluster kind. Alzarli (non persistente)?" y; then
      $SUDO sysctl -w fs.inotify.max_user_instances=512 fs.inotify.max_user_watches=524288 >/dev/null
    fi
  fi
}

create_clusters() {
  local c
  tune_inotify
  for c in "${CLUSTERS[@]}"; do
    cluster_exists "$c" && { info "Eliminazione cluster '$c'..."; kind delete cluster --name "$c" >/dev/null 2>&1; }
    info "Creazione cluster '$c'..."
    kind create cluster --name "$c" --kubeconfig "$(kc "$c")" --config "$MANIFESTS/cluster-$c.yaml" --wait 5m \
      || die "Creazione cluster '$c' fallita."
    ok "Cluster '$c' creato."
  done

  for c in "${CLUSTERS[@]}"; do
    info "Prometheus Operator CRD su '$c'..."
    kubectl apply --server-side -f "$PROM_CRDS_URL" --kubeconfig "$(kc "$c")" >/dev/null \
      || die "Installazione Prometheus CRD su '$c' fallita."
  done

  for c in "${CLUSTERS[@]}"; do
    info "Installazione Liqo $LIQO_VERSION su '$c'..."
    liqoctl install kind --cluster-id "$c" --cluster-labels="cl.liqo.io/name=$c" \
      --kubeconfig "$(kc "$c")" --version "$LIQO_VERSION" --enable-metrics \
      || die "Installazione Liqo su '$c' fallita."
    ok "Liqo installato su '$c'."
  done

  for c in "${PROVIDERS[@]}"; do
    info "Peering consumer <-> $c..."
    KUBECONFIG="$(kc consumer)" liqoctl peer --remote-kubeconfig "$(kc "$c")" --gw-server-service-type NodePort \
      || die "Peering consumer <-> $c fallito."
    ok "Peering consumer <-> $c completato."
  done
}

step_clusters() {
  step "STEP 2: Cluster kind + Liqo + peering"
  local existing=() c
  for c in "${CLUSTERS[@]}"; do cluster_exists "$c" && existing+=("$c"); done

  if [ "${#existing[@]}" -eq 0 ]; then
    warn "Nessun cluster trovato."
    if ask "Creare i 3 cluster (consumer, provider1, provider2), installare Liqo e fare il peering?" y; then
      create_clusters; CLUSTERS_FRESH=1
    else
      warn "Creazione cluster saltata."
    fi
    return
  fi

  local configured=1
  for c in "${CLUSTERS[@]}"; do
    cluster_exists "$c" && [ -f "$(kc "$c")" ] && liqo_installed "$c" || configured=0
  done
  for c in "${PROVIDERS[@]}"; do peered "$c" || configured=0; done

  if [ "$configured" = 1 ]; then
    ok "I cluster esistono gia' e sono configurati (Liqo installato + peering attivo)."
    ask "Vuoi riconfigurarli da zero (cancella e ricrea tutto)?" n && { create_clusters; CLUSTERS_FRESH=1; }
  else
    warn "Cluster presenti (${existing[*]}) ma setup incompleto o non verificabile."
    if ask "Cancellare e ricreare i 3 cluster da zero?" y; then
      create_clusters; CLUSTERS_FRESH=1
    else
      # Rigenera solo i kubeconfig mancanti dai cluster esistenti
      for c in "${existing[@]}"; do
        [ -f "$(kc "$c")" ] || kind get kubeconfig --name "$c" > "$(kc "$c")"
      done
    fi
  fi
}

# Garantisce i kubeconfig per gli step successivi
require_clusters() {
  local c
  for c in "${CLUSTERS[@]}"; do
    if [ ! -f "$(kc "$c")" ]; then
      cluster_exists "$c" || { warn "Cluster '$c' assente: step saltato."; return 1; }
      kind get kubeconfig --name "$c" > "$(kc "$c")"
    fi
  done
}

# ============================================================
# 3. IMMAGINI LIQO DAL BRANCH LOCALE
# ============================================================
step_local_images() {
  step "STEP 3: Build immagini Liqo dal branch locale"
  require_clusters || return 0
  local def=n; [ "${CLUSTERS_FRESH:-0}" = 1 ] && def=y
  ask "Buildare ${LOCAL_COMPONENTS[*]} dal branch locale, caricarle nei cluster e patcharle?" "$def" || { info "Saltato."; return 0; }

  local tag="$LIQO_VERSION-local-$(date +%Y%m%d%H%M%S)" comp image c
  pushd "$ROOT" >/dev/null || die "Root repo non trovata."
  mkdir -p "bin/$ARCH"
  for comp in "${LOCAL_COMPONENTS[@]}"; do
    image="ghcr.io/liqotech/${comp}:${tag}"
    info "Compilazione $comp (linux/$ARCH)..."
    CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -ldflags="-s -w" -o "bin/$ARCH/${comp}_linux_${ARCH}" "./cmd/$comp" \
      || { popd >/dev/null; die "Compilazione $comp fallita."; }
    info "Build immagine $image..."
    docker build --build-arg COMPONENT="$comp" --build-arg TARGETARCH="$ARCH" -t "$image" -f build/liqo/Dockerfile . \
      || { popd >/dev/null; die "Build immagine $comp fallita."; }
    for c in "${CLUSTERS[@]}"; do
      kind load docker-image "$image" --name "$c" >/dev/null 2>&1 || warn "kind load $image su '$c' fallito."
    done
    ok "$comp buildato e caricato."
  done
  popd >/dev/null

  for c in "${CLUSTERS[@]}"; do
    info "Patch liqo-controller-manager su '$c'..."
    kubectl --kubeconfig "$(kc "$c")" set image deployment/liqo-controller-manager \
      controller-manager="ghcr.io/liqotech/liqo-controller-manager:${tag}" -n liqo || die "Patch fallita su '$c'."
  done

  local p dep
  for p in "${PROVIDERS[@]}"; do
    dep="$(kubectl --kubeconfig "$(kc consumer)" get deployment -n "liqo-tenant-$p" -o name 2>/dev/null | grep "vk-$p" | head -n1)"
    if [ -n "$dep" ]; then
      info "Patch $dep (liqo-tenant-$p)..."
      kubectl --kubeconfig "$(kc consumer)" set image "$dep" "virtual-kubelet=ghcr.io/liqotech/virtual-kubelet:${tag}" \
        -n "liqo-tenant-$p" || die "Patch virtual-kubelet $p fallita."
    else
      warn "Deployment vk-$p non trovato sul consumer."
    fi
  done

  info "Attendo i rollout..."
  for c in "${CLUSTERS[@]}"; do
    kubectl --kubeconfig "$(kc "$c")" rollout status deployment/liqo-controller-manager -n liqo --timeout=180s >/dev/null
  done
  for p in "${PROVIDERS[@]}"; do
    dep="$(kubectl --kubeconfig "$(kc consumer)" get deployment -n "liqo-tenant-$p" -o name 2>/dev/null | grep "vk-$p" | head -n1)"
    [ -n "$dep" ] && kubectl --kubeconfig "$(kc consumer)" rollout status "$dep" -n "liqo-tenant-$p" --timeout=180s >/dev/null
  done
  ok "Immagini locali in uso (tag $tag)."
}

# ============================================================
# 4. TUNNEL DIRETTO provider1 <-> provider2
# ============================================================
step_direct_tunnel() {
  step "STEP 4: Tunnel diretto provider1 <-> provider2"
  require_clusters || return 0
  local def=n; [ "$DIRECT_TUNNEL" = 1 ] && def=y
  ask "Creare il tunnel diretto provider1 <-> provider2?" "$def" || { info "Saltato."; return 0; }
  liqoctl network connect --kubeconfig "$(kc provider1)" --remote-kubeconfig "$(kc provider2)" --gw-server-service-type NodePort \
    || { warn "liqoctl network connect fallito (se il tunnel esiste gia' e' normale)."; return 0; }
  ok "Tunnel diretto richiesto."
}

# ============================================================
# 5. NAMESPACE + LATENCY PINGER
# ============================================================
step_workload() {
  step "STEP 5: Offloading namespace '$NAMESPACE' + latency-pinger"
  require_clusters || return 0
  local consumer; consumer="$(kc consumer)"

  if kubectl --kubeconfig "$consumer" get deployment latency-pinger -n "$NAMESPACE" >/dev/null 2>&1; then
    ok "latency-pinger gia' deployato."
    if ask "Vuoi ridepolyarlo da zero (cancella namespace '$NAMESPACE' e ricrea)?" n; then
      kubectl --kubeconfig "$consumer" delete namespace "$NAMESPACE" --wait=true --timeout=180s || warn "Delete namespace non completata."
    else
      return 0
    fi
  else
    ask "Creare il namespace '$NAMESPACE', offloadarlo sui provider e deployare il latency-pinger?" y || { info "Saltato."; return 0; }
  fi

  kubectl --kubeconfig "$consumer" create namespace "$NAMESPACE" 2>/dev/null || true
  liqoctl offload namespace "$NAMESPACE" --kubeconfig "$consumer" --namespace-mapping-strategy EnforceSameName \
    || die "Offloading del namespace fallito."
  info "Attendo propagazione del namespace (10s)..."; sleep 10

  kubectl --kubeconfig "$consumer" -n "$NAMESPACE" apply -f "$MANIFESTS/latency-pinger-deployment.yaml" \
    && kubectl --kubeconfig "$consumer" -n "$NAMESPACE" apply -f "$MANIFESTS/latency-pinger-services.yaml" \
    || die "Deploy del latency-pinger fallito."
  kubectl --kubeconfig "$consumer" -n "$NAMESPACE" rollout status deployment/latency-pinger --timeout=120s \
    || warn "latency-pinger non ancora pronto."
  ok "latency-pinger deployato."
}

# ============================================================
# 6. IMMAGINE TOY-LABELER-CONTROLLER
# ============================================================
step_controller_image() {
  step "STEP 6: Immagine toy-labeler-controller"
  require_clusters || return 0
  local present=1 p
  docker image inspect "$CONTROLLER_IMAGE" >/dev/null 2>&1 || present=0
  if [ "$present" = 1 ]; then
    ok "Immagine $CONTROLLER_IMAGE gia' presente."
    ask "Vuoi ribuildarla da zero?" n && present=0
  else
    ask "Buildare $CONTROLLER_IMAGE e caricarla nei provider?" y || { info "Saltato."; return 0; }
  fi
  if [ "$present" = 0 ]; then
    docker build --no-cache -t "$CONTROLLER_IMAGE" -f "$ROOT/build/toy-labeler-controller/Dockerfile" "$ROOT" \
      || die "Build del controller fallita."
  fi
  for p in "${PROVIDERS[@]}"; do
    kind load docker-image "$CONTROLLER_IMAGE" --name "$p" >/dev/null 2>&1 || warn "kind load su '$p' fallito."
  done
  ok "Immagine controller caricata su ${PROVIDERS[*]}."
}

# ============================================================
# main
# ============================================================
CLUSTERS_FRESH=0
ensure_docker_access "$@"
step_utilities
ensure_docker_access "$@"
check_prereqs
step_clusters
step_local_images
step_direct_tunnel
step_workload
step_controller_image

step "SETUP COMPLETATO"
cat <<EOF
Kubeconfig:
  consumer : $(kc consumer)
  provider1: $(kc provider1)
  provider2: $(kc provider2)

Per avviare il controller su un provider (es. provider1):
  kubectl --kubeconfig $(kc provider1) apply -f $MANIFESTS/toy-labeler-controller-deployment.yaml
  kubectl --kubeconfig $(kc provider1) -n $NAMESPACE logs -f deployment/toy-labeler-controller

Cleanup dei cluster:
  kind delete cluster --name consumer; kind delete cluster --name provider1; kind delete cluster --name provider2
EOF
