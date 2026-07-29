#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="${SCRIPT_DIR}/deploy-9876.sh"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

run_script() {
  bash "${SCRIPT}" "$@" 2>&1
}

menu_output="$(run_script menu || true)"
printf '%s\n' "${menu_output}" | grep -q "OpenList GuangYaPan 管理面板" || fail "menu command should print the management panel"
printf '%s\n' "${menu_output}" | grep -q "安装 / 更新" || fail "menu command should include install/update"
printf '%s\n' "${menu_output}" | grep -q "重置管理员密码" || fail "menu command should include password reset"

help_output="$(run_script help || true)"
printf '%s\n' "${help_output}" | grep -q "Usage:" || fail "help command should print usage"
printf '%s\n' "${help_output}" | grep -q "deploy-9876.sh menu" || fail "help command should document menu"

tmp_dir="$(mktemp -d)"
cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

mkdir -p "${tmp_dir}/bin"
cat >"${tmp_dir}/bin/curl" <<'SH'
#!/usr/bin/env bash
set -euo pipefail

out=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then
    shift
    out="$1"
  fi
  shift || true
done

if [ -z "${out}" ]; then
  exit 0
fi

case "${out}" in
  *.sha256)
    asset="${out%.sha256}"
    base="$(basename "${asset}")"
    (cd "$(dirname "${asset}")" && sha256sum "${base}") >"${out}"
    ;;
  *.tar.gz)
    build_dir="$(mktemp -d)"
    cat >"${build_dir}/139cas" <<'BIN'
#!/usr/bin/env bash
echo fake openlist "$@"
BIN
    chmod +x "${build_dir}/139cas"
    tar -C "${build_dir}" -czf "${out}" 139cas
    rm -rf "${build_dir}"
    ;;
esac
SH
chmod +x "${tmp_dir}/bin/curl"

cat >"${tmp_dir}/bin/systemctl" <<'SH'
#!/usr/bin/env bash
echo "systemctl $*" >>"${SYSTEMCTL_LOG}"
exit 0
SH
chmod +x "${tmp_dir}/bin/systemctl"

install_output="$(
  PATH="${tmp_dir}/bin:${PATH}" \
  SYSTEMCTL_LOG="${tmp_dir}/systemctl.log" \
  INSTALL_DIR="${tmp_dir}/install" \
  UNIT_DIR="${tmp_dir}/units" \
  bash "${SCRIPT}" 2>&1
)"
printf '%s\n' "${install_output}" | grep -q "Deployment complete." || fail "non-interactive default should install"
test -x "${tmp_dir}/install/139cas" || fail "install should write binary"
test -f "${tmp_dir}/install/data/config.json" || fail "install should write config"
test -f "${tmp_dir}/units/139cas.service" || fail "install should write service unit"
grep -q "systemctl restart 139cas" "${tmp_dir}/systemctl.log" || fail "install should restart service"

echo "deploy script tests passed"
