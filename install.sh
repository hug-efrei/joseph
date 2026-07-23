#!/usr/bin/env bash
# Installation native de Joseph dans un LXC Debian/Ubuntu dédié (sans Docker).
#
# Usage (à lancer en root, dans le LXC) :
#   BOOKORBIT_URL=http://192.168.1.24:3000 \
#   BOOKORBIT_OPDS_USER=joseph \
#   BOOKORBIT_OPDS_PASSWORD=xxxxx \
#   ./install.sh
#
# Sans variables d'environnement, le script les demande de façon interactive.
# Relancer ce script met à jour une installation existante (git pull + rebuild
# + restart) au lieu de repartir de zéro.

set -euo pipefail
export LC_ALL=C DEBIAN_FRONTEND=noninteractive

JOSEPH_REPO="${JOSEPH_REPO:-https://github.com/hug-efrei/joseph.git}"
JOSEPH_REF="${JOSEPH_REF:-main}"
JOSEPH_DIR="/opt/joseph"
JOSEPH_USER="joseph"
JOSEPH_CACHE_DIR="/var/lib/joseph/cache"
JOSEPH_PORT="${JOSEPH_PORT:-8080}"
ENV_FILE="$JOSEPH_DIR/.env"
SERVICE_FILE="/etc/systemd/system/joseph.service"

if [[ $EUID -ne 0 ]]; then
  echo "Ce script doit être exécuté en root (dans le LXC)." >&2
  exit 1
fi

prompt_if_empty() {
  local var_name="$1" prompt_text="$2" secret="${3:-}"
  local current="${!var_name:-}"
  if [[ -n "$current" ]]; then
    return
  fi
  if [[ "$secret" == "secret" ]]; then
    read -rsp "$prompt_text: " value
    echo
  else
    read -rp "$prompt_text: " value
  fi
  printf -v "$var_name" '%s' "$value"
}

echo "=== Joseph — installation LXC native ==="

prompt_if_empty BOOKORBIT_URL "URL de BookOrbit (ex: http://192.168.1.24:3000)"
prompt_if_empty BOOKORBIT_OPDS_USER "Utilisateur OPDS BookOrbit (Réglages > OPDS)"
prompt_if_empty BOOKORBIT_OPDS_PASSWORD "Mot de passe OPDS" secret

echo "--- Dépendances système ---"
apt-get update -qq
apt-get install -y -qq git curl ca-certificates jq >/dev/null

if ! command -v go >/dev/null 2>&1; then
  echo "--- Installation de Go ---"
  ARCH="$(dpkg --print-architecture)"
  case "$ARCH" in
    amd64) GO_ARCH="amd64" ;;
    arm64) GO_ARCH="arm64" ;;
    *) echo "Architecture non supportée par ce script : $ARCH" >&2; exit 1 ;;
  esac

  # On récupère la dernière version stable et son SHA256 officiel depuis
  # l'API JSON de go.dev (pas de fichier .sha256 séparé par tarball, et pas
  # de version codée en dur qui finirait par devenir obsolète).
  RELEASE_JSON="$(curl -fsSL 'https://go.dev/dl/?mode=json')"
  GO_ASSET="$(echo "$RELEASE_JSON" | jq -r --arg arch "$GO_ARCH" \
    '[.[] | select(.stable==true)][0].files[] | select(.os=="linux" and .arch==$arch and .kind=="archive") | .filename')"
  EXPECTED_SHA256="$(echo "$RELEASE_JSON" | jq -r --arg arch "$GO_ARCH" \
    '[.[] | select(.stable==true)][0].files[] | select(.os=="linux" and .arch==$arch and .kind=="archive") | .sha256')"

  if [[ -z "$GO_ASSET" || -z "$EXPECTED_SHA256" ]]; then
    echo "Erreur : impossible de déterminer la dernière release Go pour linux/$GO_ARCH" >&2
    exit 1
  fi

  # Répertoire temporaire privé (pas de chemin fixe dans /tmp : évite qu'un
  # symlink pré-existant ne détourne le téléchargement/l'extraction).
  GO_TMP_DIR="$(mktemp -d)"
  trap 'rm -rf "$GO_TMP_DIR"' EXIT
  GO_TARBALL="$GO_TMP_DIR/go.tar.gz"

  curl -fsSL "https://go.dev/dl/${GO_ASSET}" -o "$GO_TARBALL"
  # Intégrité de la chaîne d'approvisionnement : on vérifie le tarball contre
  # le SHA256 officiel publié par go.dev avant de l'extraire en root.
  ACTUAL_SHA256="$(sha256sum "$GO_TARBALL" | awk '{print $1}')"
  if [[ "$EXPECTED_SHA256" != "$ACTUAL_SHA256" ]]; then
    echo "Erreur : SHA256 du tarball Go invalide (attendu $EXPECTED_SHA256, obtenu $ACTUAL_SHA256)" >&2
    exit 1
  fi

  rm -rf /usr/local/go
  tar -C /usr/local -xzf "$GO_TARBALL"
  rm -rf "$GO_TMP_DIR"
  trap - EXIT
  ln -sf /usr/local/go/bin/go /usr/local/bin/go
  ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
fi

if ! id "$JOSEPH_USER" >/dev/null 2>&1; then
  echo "--- Création de l'utilisateur système $JOSEPH_USER ---"
  useradd --system --home-dir "$JOSEPH_DIR" --shell /usr/sbin/nologin "$JOSEPH_USER"
fi

if systemctl is-active --quiet joseph 2>/dev/null; then
  echo "--- Service existant détecté, arrêt avant mise à jour ---"
  systemctl stop joseph
fi

if [[ -d "$JOSEPH_DIR/.git" ]]; then
  echo "--- Mise à jour du dépôt existant ---"
  git -C "$JOSEPH_DIR" fetch --depth 1 origin "$JOSEPH_REF"
  git -C "$JOSEPH_DIR" checkout -q "$JOSEPH_REF"
  git -C "$JOSEPH_DIR" reset --hard "origin/$JOSEPH_REF" 2>/dev/null || git -C "$JOSEPH_DIR" reset --hard FETCH_HEAD
else
  echo "--- Clonage du dépôt (ref: $JOSEPH_REF) ---"
  rm -rf "$JOSEPH_DIR"
  git clone --branch "$JOSEPH_REF" --depth 1 "$JOSEPH_REPO" "$JOSEPH_DIR"
fi

echo "--- Compilation ---"
(cd "$JOSEPH_DIR" && CGO_ENABLED=0 go build -ldflags '-s -w' -o server .)

mkdir -p "$JOSEPH_CACHE_DIR"
chown -R "$JOSEPH_USER":"$JOSEPH_USER" "$JOSEPH_DIR" "$JOSEPH_CACHE_DIR"

echo "--- Fichier d'environnement ---"
# Le fichier contient un secret (mot de passe OPDS) : on le crée avec des
# permissions restrictives *avant* d'y écrire, pour qu'il ne soit jamais
# lisible par d'autres utilisateurs même un court instant (pas de fenêtre
# entre écriture et chmod).
install -m 600 -o "$JOSEPH_USER" -g "$JOSEPH_USER" /dev/null "$ENV_FILE"
cat > "$ENV_FILE" <<EOF
BOOKORBIT_URL=$BOOKORBIT_URL
BOOKORBIT_OPDS_USER=$BOOKORBIT_OPDS_USER
BOOKORBIT_OPDS_PASSWORD=$BOOKORBIT_OPDS_PASSWORD
PORT=$JOSEPH_PORT
CACHE_DIR=$JOSEPH_CACHE_DIR
EOF

echo "--- Service systemd ---"
cat > "$SERVICE_FILE" <<EOF
[Unit]
Description=Joseph - lecteur Kobo pour BookOrbit
After=network.target

[Service]
Type=simple
User=$JOSEPH_USER
WorkingDirectory=$JOSEPH_DIR
EnvironmentFile=$ENV_FILE
ExecStart=$JOSEPH_DIR/server
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now joseph

echo
echo "=== Terminé ==="
echo "Joseph écoute sur le port $JOSEPH_PORT."
echo "Logs : journalctl -u joseph -f"
echo "Config : $ENV_FILE"
echo
echo "Astuce hve : ajoute les tags dns_joseph;port_$JOSEPH_PORT sur ce LXC dans Proxmox"
echo "pour une exposition automatique via Caddy (voir proxmox-caddy-sync)."
