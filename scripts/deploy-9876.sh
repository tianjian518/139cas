#!/usr/bin/env bash
set -euo pipefail

APP_NAME="${APP_NAME:-139cas}"
SERVICE_NAME="${SERVICE_NAME:-139cas}"
HTTP_PORT="${HTTP_PORT:-9876}"
REPO="${REPO:-<GITHUB_USER>/139cas}"
VERSION="${VERSION:-latest}"
INSTALL_DIR="${INSTALL_DIR:-/opt/139cas}"
DATA_DIR="${DATA_DIR:-${INSTALL_DIR}/data}"
GH_PROXY="${GH_PROXY:-}"
UNIT_DIR="${UNIT_DIR:-/etc/systemd/system}"

BIN_PATH="${INSTALL_DIR}/${APP_NAME}"
CONFIG_PATH="${DATA_DIR}/config.json"
UNIT_PATH="${UNIT_DIR}/${SERVICE_NAME}.service"
TMP_DIR_TO_CLEAN=""

die() {
  echo "error: $*" >&2
  exit 1
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "missing required command: $1"
}

require_root() {
  if [ "$(id -u)" -ne 0 ]; then
    die "please run as root, for example: sudo bash deploy-9876.sh"
  fi
}

validate_port() {
  case "${HTTP_PORT}" in
    ''|*[!0-9]*)
      die "HTTP_PORT must be a number"
      ;;
  esac

  if [ "${HTTP_PORT}" -lt 1 ] || [ "${HTTP_PORT}" -gt 65535 ]; then
    die "HTTP_PORT must be between 1 and 65535"
  fi
}

detect_arch() {
  case "$(uname -s)" in
    Linux) ;;
    *) die "only Linux is supported" ;;
  esac

  case "$(uname -m)" in
    x86_64|amd64)
      ARCH="amd64"
      ;;
    aarch64|arm64)
      ARCH="arm64"
      ;;
    *)
      die "unsupported architecture: $(uname -m). Supported: x86_64, aarch64"
      ;;
  esac
}

download_url() {
  local asset="$1"
  if [ "${VERSION}" = "latest" ]; then
    printf '%s\n' "${GH_PROXY}https://github.com/${REPO}/releases/latest/download/${asset}"
  else
    printf '%s\n' "${GH_PROXY}https://github.com/${REPO}/releases/download/${VERSION}/${asset}"
  fi
}

install_or_update() {
  require_root
  validate_port
  need_cmd curl
  need_cmd install
  need_cmd systemctl
  need_cmd tar
  detect_arch

  local asset="${APP_NAME}_linux_${ARCH}.tar.gz"
  local url
  url="$(download_url "${asset}")"
  TMP_DIR_TO_CLEAN="$(mktemp -d)"
  cleanup() {
    if [ -n "${TMP_DIR_TO_CLEAN}" ]; then
      rm -rf "${TMP_DIR_TO_CLEAN}"
    fi
  }
  trap cleanup EXIT

  echo "Downloading ${url}"
  curl -fL --retry 3 --retry-delay 2 -o "${TMP_DIR_TO_CLEAN}/${asset}" "${url}"

  local sha_url="${url}.sha256"
  if command -v sha256sum >/dev/null 2>&1 && curl -fL --retry 3 --retry-delay 2 -o "${TMP_DIR_TO_CLEAN}/${asset}.sha256" "${sha_url}"; then
    (
      cd "${TMP_DIR_TO_CLEAN}"
      sha256sum -c "${asset}.sha256"
    )
  else
    echo "Skipping sha256 verification"
  fi

  mkdir -p "${TMP_DIR_TO_CLEAN}/extract"
  tar -xzf "${TMP_DIR_TO_CLEAN}/${asset}" -C "${TMP_DIR_TO_CLEAN}/extract"

  local extracted_bin=""
  if [ -f "${TMP_DIR_TO_CLEAN}/extract/${APP_NAME}" ]; then
    extracted_bin="${TMP_DIR_TO_CLEAN}/extract/${APP_NAME}"
  elif [ -f "${TMP_DIR_TO_CLEAN}/extract/openlist" ]; then
    extracted_bin="${TMP_DIR_TO_CLEAN}/extract/openlist"
  else
    extracted_bin="$(find "${TMP_DIR_TO_CLEAN}/extract" -maxdepth 2 -type f -perm -111 | head -n 1 || true)"
  fi

  if [ -z "${extracted_bin}" ] || [ ! -f "${extracted_bin}" ]; then
    die "downloaded archive does not contain an executable binary"
  fi

  mkdir -p "${INSTALL_DIR}" "${DATA_DIR}" "${UNIT_DIR}"
  install -m 0755 "${extracted_bin}" "${BIN_PATH}"

  if [ ! -f "${CONFIG_PATH}" ]; then
    cat >"${CONFIG_PATH}" <<JSON
{
  "scheme": {
    "address": "0.0.0.0",
    "http_port": ${HTTP_PORT},
    "https_port": -1
  }
}
JSON
  fi

  cat >"${UNIT_PATH}" <<UNIT
[Unit]
Description=OpenList GuangYaPan CAS
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=${INSTALL_DIR}
ExecStart=${BIN_PATH} server --data ${DATA_DIR} --log-std
Restart=on-failure
RestartSec=3
Environment=TZ=Asia/Shanghai
Environment=UMASK=022
Environment=OPENLIST_ADDR=0.0.0.0
Environment=OPENLIST_HTTP_PORT=${HTTP_PORT}

[Install]
WantedBy=multi-user.target
UNIT

  systemctl daemon-reload
  systemctl enable "${SERVICE_NAME}" >/dev/null
  systemctl restart "${SERVICE_NAME}"

  echo
  echo "Deployment complete."
  show_info
  echo
  systemctl --no-pager --full status "${SERVICE_NAME}" || true
}

start_service() {
  require_root
  need_cmd systemctl
  systemctl start "${SERVICE_NAME}"
  systemctl --no-pager --full status "${SERVICE_NAME}" || true
}

stop_service() {
  require_root
  need_cmd systemctl
  systemctl stop "${SERVICE_NAME}"
}

restart_service() {
  require_root
  need_cmd systemctl
  systemctl restart "${SERVICE_NAME}"
  systemctl --no-pager --full status "${SERVICE_NAME}" || true
}

status_service() {
  need_cmd systemctl
  systemctl --no-pager --full status "${SERVICE_NAME}" || true
}

show_logs() {
  need_cmd journalctl
  journalctl -u "${SERVICE_NAME}" -f
}

reset_password() {
  require_root
  if [ ! -x "${BIN_PATH}" ]; then
    die "binary not found: ${BIN_PATH}. Install first."
  fi

  local password="${1:-}"
  if [ -z "${password}" ]; then
    if [ -t 0 ]; then
      read -r -s -p "New admin password (empty for random): " password
      echo
    fi
  fi

  if [ -z "${password}" ] || [ "${password}" = "random" ]; then
    "${BIN_PATH}" admin random --data "${DATA_DIR}"
  else
    "${BIN_PATH}" admin set "${password}" --data "${DATA_DIR}"
  fi
}

uninstall_service() {
  require_root
  need_cmd systemctl
  systemctl stop "${SERVICE_NAME}" 2>/dev/null || true
  systemctl disable "${SERVICE_NAME}" >/dev/null 2>&1 || true
  rm -f "${UNIT_PATH}"
  systemctl daemon-reload
  rm -f "${BIN_PATH}"

  if [ "${REMOVE_DATA:-0}" = "1" ]; then
    rm -rf "${INSTALL_DIR}"
  else
    echo "Kept data directory: ${DATA_DIR}"
    echo "Set REMOVE_DATA=1 to remove ${INSTALL_DIR} too."
  fi
}

show_info() {
  cat <<INFO
Service: ${SERVICE_NAME}
Install dir: ${INSTALL_DIR}
Data dir: ${DATA_DIR}
Binary: ${BIN_PATH}
Config: ${CONFIG_PATH}
Unit: ${UNIT_PATH}
URL: http://127.0.0.1:${HTTP_PORT}
INFO
}

usage() {
  cat <<USAGE
Usage:
  deploy-9876.sh                 Interactive menu when run from a TTY; install/update when piped
  deploy-9876.sh menu            Show management menu
  deploy-9876.sh install         Install or update OpenList GuangYaPan
  deploy-9876.sh update          Same as install
  deploy-9876.sh start           Start service
  deploy-9876.sh stop            Stop service
  deploy-9876.sh restart         Restart service
  deploy-9876.sh status          Show service status
  deploy-9876.sh logs            Follow service logs
  deploy-9876.sh password [PWD]  Reset admin password; use "random" for random password
  deploy-9876.sh uninstall       Remove service and binary; keep data by default
  deploy-9876.sh info            Show paths and URL
  deploy-9876.sh help            Show this help

Environment:
  HTTP_PORT=${HTTP_PORT}
  INSTALL_DIR=${INSTALL_DIR}
  SERVICE_NAME=${SERVICE_NAME}
  VERSION=${VERSION}
USAGE
}

print_menu() {
  cat <<MENU
OpenList GuangYaPan 管理面板

1. 安装 / 更新
2. 启动服务
3. 停止服务
4. 重启服务
5. 查看状态
6. 查看日志
7. 重置管理员密码
8. 卸载
9. 显示安装信息
0. 退出
MENU
}

menu_loop() {
  print_menu
  if [ ! -t 0 ]; then
    return 0
  fi

  while true; do
    echo
    read -r -p "请选择操作 [0-9]: " choice
    case "${choice}" in
      1) install_or_update ;;
      2) start_service ;;
      3) stop_service ;;
      4) restart_service ;;
      5) status_service ;;
      6) show_logs ;;
      7) reset_password ;;
      8) uninstall_service ;;
      9) show_info ;;
      0) exit 0 ;;
      *) echo "Invalid choice: ${choice}" ;;
    esac
  done
}

main() {
  local command="${1:-}"
  if [ -z "${command}" ]; then
    if [ -t 0 ]; then
      command="menu"
    else
      command="install"
    fi
  else
    shift
  fi

  case "${command}" in
    install|update) install_or_update "$@" ;;
    start) start_service ;;
    stop) stop_service ;;
    restart) restart_service ;;
    status) status_service ;;
    logs|log) show_logs ;;
    password|admin) reset_password "$@" ;;
    uninstall|remove) uninstall_service ;;
    info) show_info ;;
    menu) menu_loop ;;
    help|-h|--help) usage ;;
    *)
      usage
      die "unknown command: ${command}"
      ;;
  esac
}

main "$@"
