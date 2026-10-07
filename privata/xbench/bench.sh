#!/bin/bash
# Xray router benchmark driver. Client, server and target each run in their own
# network namespace on dedicated CPUs, joined by veth pairs:
#
#   cli (Xray clients + load) 10.99.0.2 -- 10.99.0.1 srv (Xray server) 10.98.0.1 -- 10.98.0.2 tgt (xbench serve)
#
# RPS on every veth steers receive processing to the CPUs of the receiving
# side, so the server's CPUs are only charged for the server's own work.
#
#   bench.sh setup                         namespaces, veths, certs, REALITY keys
#   bench.sh start <xray> <xhttp|grpc|raw> <tls|reality|none>
#   bench.sh load <xbench load flags>      e.g. -mode down -conns 32
#   bench.sh suite <label>                 fixed test set against the running server
#   bench.sh profile <name> <secs> <load flags>
#   bench.sh stop | teardown
set -euo pipefail

D=${XBENCH_DIR:-/opt/xbench}
RUN=$D/run
OUT=$D/out
SRV_CPUS=${SRV_CPUS:-0,4}
TGT_CPUS=${TGT_CPUS:-1,5}
CLI_CPUS=${CLI_CPUS:-2,3,6,7}
NCLI=${NCLI:-4}
# inproc: Xray clients run inside xbench (no SOCKS hop); socks: separate Xray processes.
CLIENT_MODE=${CLIENT_MODE:-inproc}
CLIENT_XRAY=${CLIENT_XRAY:-$D/xray-client}
SRV_ENV=${SRV_ENV:-}
TARGET=10.98.0.2:9000

mask() {
	local m=0 c
	IFS=, read -ra cpus <<<"$1"
	for c in "${cpus[@]}"; do m=$((m | 1 << c)); done
	printf '%x' "$m"
}

uuid() { printf '00000000-0000-4000-8000-%012d' "$1"; }

setup() {
	mkdir -p "$RUN" "$OUT"
	for ns in cli srv tgt; do
		ip netns add $ns 2>/dev/null || true
		ip -n $ns link set lo up
		ip netns exec $ns sysctl -q -w \
			net.ipv4.tcp_congestion_control=bbr \
			net.ipv4.tcp_rmem="4096 87380 67108864" \
			net.ipv4.tcp_wmem="4096 65536 67108864" \
			net.ipv4.tcp_fastopen=3 \
			net.ipv4.ip_local_port_range="10000 65000"
	done
	ip -n cli link show cli0 >/dev/null 2>&1 || ip link add cli0 netns cli type veth peer name srv0 netns srv
	ip -n tgt link show tgt0 >/dev/null 2>&1 || ip link add tgt0 netns tgt type veth peer name srv1 netns srv
	ip -n cli addr replace 10.99.0.2/24 dev cli0
	ip -n srv addr replace 10.99.0.1/24 dev srv0
	ip -n tgt addr replace 10.98.0.2/24 dev tgt0
	ip -n srv addr replace 10.98.0.1/24 dev srv1
	ip -n tgt link set tgt0 mtu 1420
	ip -n srv link set srv1 mtu 1420
	local pair ns dev cpus
	for pair in cli:cli0:$CLI_CPUS srv:srv0:$SRV_CPUS srv:srv1:$SRV_CPUS tgt:tgt0:$TGT_CPUS; do
		IFS=: read -r ns dev cpus <<<"$pair"
		ip -n "$ns" link set "$dev" up
		ip netns exec "$ns" tc qdisc replace dev "$dev" root fq
		ip netns exec "$ns" sh -c "echo $(mask "$cpus") > /sys/class/net/$dev/queues/rx-0/rps_cpus"
	done
	ip -n tgt route replace 10.99.0.0/24 via 10.98.0.1
	ip -n cli route replace 10.98.0.0/24 via 10.99.0.1
	if [ ! -e "$D/srv.pem" ]; then
		openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 3650 \
			-subj /CN=xbench-ca -keyout "$D/ca.key" -out "$D/ca.pem" 2>/dev/null
		openssl req -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes \
			-subj /CN=bench.local -keyout "$D/srv.key" -out "$D/srv.csr" 2>/dev/null
		openssl x509 -req -in "$D/srv.csr" -CA "$D/ca.pem" -CAkey "$D/ca.key" -CAcreateserial -days 3650 \
			-extfile <(printf 'subjectAltName=DNS:bench.local') -out "$D/srv.pem" 2>/dev/null
	fi
	if [ ! -e "$D/reality.env" ]; then
		"$CLIENT_XRAY" x25519 | awk -F': ' '/^PrivateKey/{print "REALITY_PRIV=" $2} /^(Password|PublicKey)/{print "REALITY_PUB=" $2}' >"$D/reality.env"
	fi
}

teardown() {
	stop
	for ns in cli srv tgt; do ip netns del $ns 2>/dev/null || true; done
}

stream_settings() { # <transport> <security> <server|client>
	local transport=$1 security=$2 side=$3 sec tr
	# shellcheck disable=SC1091
	. "$D/reality.env"
	if [ "$security" = none ]; then
		sec='"security":"none"'
	elif [ "$security" = tls ]; then
		if [ "$side" = server ]; then
			sec='"security":"tls","tlsSettings":{"certificates":[{"certificateFile":"'$D'/srv.pem","keyFile":"'$D'/srv.key"}]}'
		else
			sec='"security":"tls","tlsSettings":{"serverName":"bench.local","fingerprint":"chrome","certificates":[{"usage":"verify","certificateFile":"'$D'/ca.pem"}]}'
		fi
	else
		if [ "$side" = server ]; then
			sec='"security":"reality","realitySettings":{"show":false,"target":"10.98.0.2:8443","serverNames":["bench.local"],"privateKey":"'$REALITY_PRIV'","shortIds":["0123456789abcdef"]}'
		else
			sec='"security":"reality","realitySettings":{"serverName":"bench.local","fingerprint":"chrome","publicKey":"'$REALITY_PUB'","shortId":"0123456789abcdef","spiderX":"/"}'
		fi
	fi
	case "$transport/$side" in
	xhttp/server) tr='"network":"xhttp","xhttpSettings":{"path":"/x","mode":"auto","extra":{"xPaddingBytes":"100-1000","scStreamUpServerSecs":"20-80","maxReadFrameSize":16384}}' ;;
	xhttp/client) tr='"network":"xhttp","xhttpSettings":{"host":"bench.local","path":"/x","mode":"stream-up","extra":{"xPaddingBytes":"100-1000","maxReadFrameSize":16384,"xmux":{"maxConnections":"8"}}}' ;;
	grpc/*) tr='"network":"grpc","grpcSettings":{"serviceName":"grpc","multiMode":false}' ;;
	raw/*) tr='"network":"raw"' ;;
	esac
	printf '{%s,%s}' "$tr" "$sec"
}

gen_configs() { # <transport> <security>
	local clients="" i
	for i in $(seq 1 "$NCLI"); do
		clients+="${clients:+,}{\"id\":\"$(uuid "$i")\",\"email\":\"u$i\"}"
	done
	cat >"$RUN/srv.json" <<EOF
{
  "log": {"loglevel": "warning", "access": "none"},
  "api": {"tag": "api", "listen": "127.0.0.1:10085", "services": ["HandlerService", "StatsService"]},
  "metrics": {"tag": "metrics", "listen": "127.0.0.1:11111"},
  "stats": {},
  "policy": {"levels": {"0": {"statsUserUplink": true, "statsUserDownlink": true, "statsUserOnline": true}}},
  "inbounds": [{
    "tag": "vless-in", "listen": "10.99.0.1", "port": 443, "protocol": "vless",
    "settings": {"clients": [$clients], "decryption": "none"},
    "streamSettings": $(stream_settings "$1" "$2" server)
  }],
  "outbounds": [{"tag": "direct", "protocol": "freedom", "settings": {"finalRules": [{"action": "allow", "ip": ["10.98.0.0/24"]}]}}]
}
EOF
	for i in $(seq 1 "$NCLI"); do
		cat >"$RUN/cli$i.json" <<EOF
{
  "log": {"loglevel": "error", "access": "none"},
  "inbounds": [{"listen": "127.0.0.1", "port": $((1080 + i)), "protocol": "socks", "settings": {"udp": false}}],
  "outbounds": [{
    "protocol": "vless",
    "settings": {"vnext": [{"address": "10.99.0.1", "port": 443, "users": [{"id": "$(uuid "$i")", "encryption": "none"}]}]},
    "streamSettings": $(stream_settings "$1" "$2" client)
  }]
}
EOF
	done
}

spawn() { # <name> <ns> <cpus> <cmd...>
	local name=$1 ns=$2 cpus=$3
	shift 3
	setsid ip netns exec "$ns" taskset -c "$cpus" "$@" >"$RUN/$name.log" 2>&1 </dev/null &
	echo $! >"$RUN/$name.pid"
}

wait_port() { # <ns> <addr:port>
	local i
	for i in $(seq 1 50); do
		ip netns exec "$1" ss -ltnH "( sport = :${2##*:} )" | grep -q . && return 0
		sleep 0.1
	done
	echo "port $2 in $1 did not open" >&2
	return 1
}

start() { # <server-xray> <transport> <security>
	local bin=$1 transport=$2 security=$3 i
	stop
	gen_configs "$transport" "$security"
	spawn tgt tgt "$TGT_CPUS" "$D/xbench" serve -listen "$TARGET" -tls-listen 10.98.0.2:8443 -cert "$D/srv.pem" -key "$D/srv.key"
	# shellcheck disable=SC2086
	spawn srv srv "$SRV_CPUS" env $SRV_ENV "$bin" run -c "$RUN/srv.json"
	wait_port tgt "$TARGET"
	wait_port srv 10.99.0.1:443
	if [ "$CLIENT_MODE" = socks ]; then
		for i in $(seq 1 "$NCLI"); do
			spawn "cli$i" cli "$CLI_CPUS" "$CLIENT_XRAY" run -c "$RUN/cli$i.json"
		done
		for i in $(seq 1 "$NCLI"); do wait_port cli "127.0.0.1:$((1080 + i))"; done
	fi
	echo "started $(basename "$bin") $transport/$security srv_cpus=$SRV_CPUS clients=$NCLI env='$SRV_ENV'"
}

stop() {
	local f
	for f in "$RUN"/*.pid; do
		[ -e "$f" ] || continue
		kill "$(cat "$f")" 2>/dev/null || true
		rm -f "$f"
	done
	sleep 0.3
}

clients() {
	local s="" i
	for i in $(seq 1 "$NCLI"); do
		if [ "$CLIENT_MODE" = socks ]; then
			s+="${s:+,}127.0.0.1:$((1080 + i))"
		else
			s+="${s:+,}$RUN/cli$i.json"
		fi
	done
	if [ "$CLIENT_MODE" = socks ]; then echo "-socks $s"; else echo "-xray $s"; fi
}

load() {
	# shellcheck disable=SC2046
	ip netns exec cli taskset -c "$CLI_CPUS" "$D/xbench" load $(clients) -target "$TARGET" \
		-cpus "server=$SRV_CPUS;client=$CLI_CPUS;target=$TGT_CPUS" "$@"
}

suite() { # <label>
	local label=$1
	{
		echo "== $label"
		load -mode down -conns 1 -duration 10s
		load -mode down -conns 32 -duration 10s
		load -mode up -conns 1 -duration 10s
		load -mode up -conns 32 -duration 10s
		load -mode churn -conns 64 -size 16k -duration 10s
		load -mode ping -conns 1 -duration 5s
	} 2>&1 | tee -a "$OUT/results.txt"
}

profile() { # <name> <secs> <load flags...>
	local name=$1 secs=$2
	shift 2
	ip netns exec srv curl -s -o "$OUT/$name.pprof" "http://127.0.0.1:11111/debug/pprof/profile?seconds=$secs" &
	local curl=$!
	perf record -q -F 1999 -g -C "$SRV_CPUS" -o "$OUT/$name.perf" -- sleep "$secs" 2>/dev/null &
	local perf=$!
	load "$@" -warmup 1s -duration "$((secs + 1))s"
	wait $curl $perf || true
}

cmd=${1:-}
shift || true
case "$cmd" in
setup | teardown | start | stop | load | suite | profile) "$cmd" "$@" ;;
*) sed -n '2,15p' "$0" >&2; exit 2 ;;
esac
