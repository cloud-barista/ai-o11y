#!/usr/bin/env bash
# Installs VictoriaMetrics, vmagent and vmauth on one storage VM.
#
#   sudo ./install.sh node.env            install, then enable and start
#   sudo ./install.sh node.env --no-start install only
#
# Downloads the release archives from GitHub and checks them against the
# release's checksum files before installing anything.
set -euo pipefail

env_file=${1:?usage: install.sh <node.env> [--no-start]}
start=true
[[ ${2:-} == --no-start ]] && start=false
[[ $(id -u) == 0 ]] || { echo "run as root" >&2; exit 1; }

here=$(cd "$(dirname "$0")" && pwd)
set -a
# shellcheck source=/dev/null
. "$env_file"
set +a
for v in VM_VERSION SELF_ADDR PEER_ADDR DATA_DIR; do
	[[ -n ${!v:-} ]] || { echo "$v is not set in $env_file" >&2; exit 1; }
done

base=https://github.com/VictoriaMetrics/VictoriaMetrics/releases/download/$VM_VERSION
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cd "$work"
for archive in victoria-metrics vmutils; do
	file=$archive-linux-amd64-$VM_VERSION.tar.gz
	curl -fsSLO "$base/$file"
	curl -fsSLO "$base/${file%.tar.gz}_checksums.txt"
	grep " $file\$" "${file%.tar.gz}_checksums.txt" | sha256sum -c -
	tar -xzf "$file"
done
sha256sum -c --ignore-missing ./*_checksums.txt

install -m 0755 victoria-metrics-prod /usr/local/bin/victoria-metrics
install -m 0755 vmagent-prod /usr/local/bin/vmagent
install -m 0755 vmauth-prod /usr/local/bin/vmauth

id victoria >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin victoria
install -d -o victoria -g victoria "$DATA_DIR" "$DATA_DIR/storage" "$DATA_DIR/vmagent"
install -d /etc/aio11y-victoria
install -m 0644 "$env_file" /etc/aio11y-victoria/node.env
sed -e "s|\${SELF_ADDR}|$SELF_ADDR|g" -e "s|\${PEER_ADDR}|$PEER_ADDR|g" \
	"$here/vmauth.yml.tmpl" > /etc/aio11y-victoria/vmauth.yml
for unit in victoria-metrics vmagent vmauth; do
	install -m 0644 "$here/$unit.service" "/etc/systemd/system/$unit.service"
done

if $start; then
	systemctl daemon-reload
	systemctl enable --now victoria-metrics vmagent vmauth
fi
echo "installed $VM_VERSION on $SELF_ADDR (peer $PEER_ADDR)"
