#!/usr/bin/env bash
# Install the read-only OpenCode Go quota plugin into a CLIProxyAPI installation.
#
# This script only copies the plugin library. It never edits config.yaml: the
# configuration block that has to be added is printed at the end for the
# operator to paste, because plugin loading and enable flags are owned by the
# host configuration.
set -euo pipefail

VERSION="${1:-1.0.0}"
ROOT="${CPA_ROOT:-/opt/cliproxyapi}"
GOOS="${GOOS:-linux}"
GOARCH="${GOARCH:-amd64}"
PLUGIN_ID="cpa-opencodego-quota"

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST="${HERE}/dist"
TARGET_DIR="${ROOT}/plugins/${GOOS}/${GOARCH}"
SOURCE="${DIST}/${PLUGIN_ID}-v${VERSION}.so"

if [ ! -f "${SOURCE}" ]; then
  echo "missing ${SOURCE}; run 'make build' first" >&2
  exit 1
fi

mkdir -p "${TARGET_DIR}"

# The host selects one file per plugin id, but leaving older builds next to the
# new one only invites a stale library to win when the version comparison does
# not understand the tag.
shopt -s nullglob
for existing in "${TARGET_DIR}/${PLUGIN_ID}"-v*.so; do
  if [ "${existing}" != "${TARGET_DIR}/${PLUGIN_ID}-v${VERSION}.so" ]; then
    rm -f "${existing}"
    echo "removed older version ${existing}"
  fi
done
shopt -u nullglob

install -m 0644 "${SOURCE}" "${TARGET_DIR}/"
echo "installed ${TARGET_DIR}/${PLUGIN_ID}-v${VERSION}.so"

cat <<CONFIG

Add the plugin to ${ROOT}/conf/config.yaml, then restart CLIProxyAPI:

plugins:
    configs:
        ${PLUGIN_ID}:
            enabled: true

Optional settings (all have defaults, see README):
            # config-path: "/CLIProxyAPI/conf/config.yaml"
            # egress: auto            # auto | direct | proxy
            # proxy-url: ""           # used when egress: proxy
            # hosts: [opencode.ai]
            # usage-url: ""           # override …/inference/go/v1/usage
            # session: ""             # optional x-opencode-session header
            # timeout-seconds: 20
            # cache-seconds: 10

The page appears in the management centre sidebar as "${PLUGIN_ID}".
CONFIG
