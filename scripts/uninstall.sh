#!/usr/bin/env bash
# Remove the read-only OpenCode Go quota plugin binary. The configuration block
# under plugins.configs has to be removed by the operator; this script does not
# edit config.yaml.
set -euo pipefail

ROOT="${CPA_ROOT:-/opt/cliproxyapi}"
PLUGIN_ID="cpa-opencodego-quota"

for dir in "${ROOT}/plugins/linux/amd64" "${ROOT}/plugins"; do
  shopt -s nullglob
  for existing in "${dir}/${PLUGIN_ID}"-v*.so; do
    rm -f "${existing}"
    echo "removed ${existing}"
  done
  shopt -u nullglob
done

echo "now remove plugins.configs.${PLUGIN_ID} from ${ROOT}/conf/config.yaml and restart CLIProxyAPI"
