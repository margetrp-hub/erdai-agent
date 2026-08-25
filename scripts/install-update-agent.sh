#!/bin/sh
set -eu

root=${1:-${ERDAI_INSTALL_ROOT:-/opt/erdai-agent}}
repository=${ERDAI_UPDATE_REPOSITORY:-margetrp-hub/erdai-agent}
unit=/etc/systemd/system/erdai-update-agent.service
agent=$root/app/scripts/erdai-update-agent.sh

case "$root" in *[!A-Za-z0-9._/-]*|'') echo "invalid installation root" >&2; exit 2 ;; esac
case "$repository" in *[!A-Za-z0-9._/-]*|'') echo "invalid update repository" >&2; exit 2 ;; esac
test "$(id -u)" = 0 || { echo "run as root" >&2; exit 1; }
test -x "$agent" || { echo "update agent is missing: $agent" >&2; exit 1; }
command -v systemctl >/dev/null || { echo "systemctl is required" >&2; exit 1; }

install -d -m 700 "$root/data/update-work"
install -d -m 755 "$root/update-status"
cat > "$unit" <<EOF
[Unit]
Description=ErDai Agent Stable update executor
After=docker.service
Requires=docker.service

[Service]
Type=simple
ExecStart=$agent
Restart=always
RestartSec=5
User=root
PrivateTmp=true
ProtectSystem=strict
NoNewPrivileges=true
ReadWritePaths=$root
Environment=ERDAI_INSTALL_ROOT=$root
Environment=ERDAI_UPDATE_REPOSITORY=$repository
Environment=ERDAI_UPDATE_REQUEST_FILE=$root/data/update-request.json
Environment=ERDAI_UPDATE_STATUS_FILE=$root/update-status/update-status.json
Environment=ERDAI_UPDATE_WORK_DIR=$root/data/update-work

[Install]
WantedBy=multi-user.target
EOF

chmod 0644 "$unit"
systemctl daemon-reload
systemctl enable --now erdai-update-agent.service
systemctl --no-pager --full status erdai-update-agent.service
