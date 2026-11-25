#!/bin/sh
# Reports what a build on this server can reach (D-170). It starts marker
# listeners on the host, a container on another network and one on an
# untrusted preview's kind of network, then runs builds that try each of
# them the three ways a build has: a fetch (ADD <url>), a remote build
# context, and a RUN step. Run it as root on a server with Toft's host
# firewall loaded; it changes no rules and removes what it started.
#
#   scripts/build-network-check.sh [BUILDER]
#
# BUILDER is toft-build (default), toft-build-untrusted, or "default":
# Docker's built-in BuildKit, where every build ran before 1.2.3 and where
# the fetches fail this check. A toft- builder is made by
# backend/internal/builder/ensure.sh, after lock.sh beside it, as the panel
# makes it before a build (ENSURE names another copy).
#
# Optional: OTHER="address:port ...", listeners on another server's private
# address that the operator's list doesn't open (another port of a listed
# address is one); META_URL, the metadata address to try (default:
# Hetzner's); ALLOWED="address:port ...", destinations the operator's list
# opens for this server: reachable from toft-build, and from nowhere else.
#
# The builders' networks have no IPv6 (ensure.sh), so nothing is reached
# over it: the v6 rows try a container and the host at unique local
# addresses, a link-local address and the IPv6 metadata address.
#
# Exits 1 when a build reached what it must not, or didn't reach what it
# must. Needs python3 and Docker with buildx and compose.
set -eu

B=${1:-toft-build}
ENSURE=${ENSURE:-$(dirname "$0")/../backend/internal/builder/ensure.sh}
META_URL=${META_URL:-http://169.254.169.254/hetzner/v1/metadata/instance-id}
OTHER=${OTHER:-}
ALLOWED=${ALLOWED:-}
IMG=busybox:1
W=$(mktemp -d)
HITS=$W/hits
: >"$HITS"

command -v python3 >/dev/null || { echo "python3 is needed for the marker listeners" >&2; exit 2; }
nft list chain ip toft from_untrusted >/dev/null 2>&1 ||
	echo "warning: Toft's host firewall isn't loaded here: the result says nothing about a server" >&2

cleanup() {
	trap - EXIT INT TERM
	kill $(cat "$W"/*.pid 2>/dev/null) 2>/dev/null || true
	docker rm -f bnc-ctr bnc-uctr bnc-ctr6 >/dev/null 2>&1 || true
	docker network rm bnc-net bnc-unet bnc-net6 >/dev/null 2>&1 || true
	docker image ls -q 'bnc/*' | sort -u | xargs -r docker rmi -f >/dev/null 2>&1 || true
	rm -rf "$W"
}
trap cleanup EXIT INT TERM

# A listener answers every GET with "bnc-reached" and logs the path.
listen() {
	python3 - "$1" "$2" "$HITS" <<'EOF' &
import sys, socket, http.server
host, port, log = sys.argv[1], int(sys.argv[2]), sys.argv[3]
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        with open(log, "a") as f:
            f.write(f"{self.client_address[0]} {self.path} \n")
        self.send_response(200); self.send_header("Content-Length", "12"); self.end_headers()
        self.wfile.write(b"bnc-reached\n")
    do_HEAD = do_GET
    def log_message(self, *a): pass
class S(http.server.ThreadingHTTPServer):
    address_family = socket.AF_INET6 if ":" in host else socket.AF_INET
S((host, port), H).serve_forever()
EOF
	echo $! >"$W/$2.pid"
}
listen 127.0.0.1 18080
listen 0.0.0.0 18083
listen :: 18084

serve='mkdir /w && echo bnc-reached >/w/index.html && exec httpd -f -p 8080 -h /w'
docker network create bnc-net >/dev/null
docker network create -o com.docker.network.bridge.name=toft-ubnc bnc-unet >/dev/null
docker run -d --name bnc-ctr --network bnc-net "$IMG" sh -c "$serve" >/dev/null
docker run -d --name bnc-uctr --network bnc-unet "$IMG" sh -c "$serve" >/dev/null
addr() { docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$1"; }
gateway() { docker network inspect -f '{{range .IPAM.Config}}{{.Gateway}}{{end}}' "$1"; }
CTR=$(addr bnc-ctr)
UCTR=$(addr bnc-uctr)
OTHERGW=$(gateway bnc-net)
# A network with IPv6, for a container and the host at unique local
# addresses; V6 stays empty where this Docker makes none.
V6=fd00:b0c:6::
if docker network create --ipv6 --subnet "$V6/64" --gateway "${V6}1" bnc-net6 >/dev/null 2>&1 &&
	docker run -d --name bnc-ctr6 --network bnc-net6 --ip6 "${V6}2" "$IMG" sh -c "$serve" >/dev/null 2>&1; then :; else
	V6=
	echo "skip  the unique local v6 rows: this Docker made no IPv6 network"
fi
# The host's own addresses: those of interfaces that aren't a bridge.
HOSTADDRS=$(ip -4 -o addr show scope global | awk '$2 !~ /^(docker|br-|toft-u|veth)/ {sub(/\/.*/, "", $4); print $4}')

# config: a client config of its own for one build, as the panel makes
# one, with the builder's definition in it.
config() {
	c=$(mktemp -d "$W/cfg.XXXXXX")
	if [ "$B" != default ]; then
		bridge=toft-ubuild
		[ "$B" = toft-build ] || bridge=toft-upbuild
		DOCKER_CONFIG=$c TOFT_BUILDER=$B TOFT_BUILDER_BRIDGE=$bridge \
			TOFT_BUILDER_IMAGE=${TOFT_BUILDER_IMAGE:-moby/buildkit:v0.35.1} \
			TOFT_BUILDER_CACHE=${TOFT_BUILDER_CACHE:-10%} \
			TOFT_BUILDER_LOCK=/run/toft/$B.lock TOFT_BUILDER_WAIT=300 \
			sh -c "$(cat "$(dirname "$ENSURE")/lock.sh" "$ENSURE")" >&2
	fi
	echo "$c"
}
# build NAME [context]: builds $W/NAME (or the context given) the way the
# panel does, its log in $W/NAME.log.
build() {
	n=$1
	c=$(config)
	DOCKER_CONFIG=$c BUILDX_GIT_INFO=false timeout 180 docker buildx build --builder "$B" --load \
		--progress=plain -t "bnc/$n" ${3:+-f "$3"} "${2:-$W/$n}" >"$W/$n.log" 2>&1
}
dockerfile() { mkdir -p "$W/$1" && printf '%s\n' "$2" >"$W/$1/Dockerfile"; }
hit() { grep -Eq "/$1[^a-z0-9-]" "$HITS"; }

fail=0
jobs=
# In a RUN step that prints what it found, so that a second run on this
# server runs the step again and doesn't take it from the build cache.
stamp=$(date +%s)
# row WANT NAME GOT WHAT
row() {
	if [ "$3" = "$1" ]; then mark=ok; else mark=FAIL; fail=1; fi
	printf '%-5s %-16s %-8s (want %-7s) %s\n' "$mark" "$2" "$3" "$1" "$4"
}

# A fetch: reached when the build got the file, or the listener was asked.
add() {
	(
		dockerfile "$2" "FROM $IMG
ADD $3 /m"
		if build "$2" || hit "$2"; then got=open; else got=blocked; fi
		row "$1" "$2" "$got" "ADD $3" >"$W/$2.row"
	) &
	jobs="$jobs $!"
}
# A remote build context or Dockerfile, through compose: reached when the
# listener was asked.
ctx() {
	(
		mkdir -p "$W/$2" && printf 'FROM %s\nCOPY --from=x / /x\n' "$IMG" >"$W/$2/Dockerfile"
		printf 'services:\n  c:\n    build: %s\n' "$3" >"$W/$2/compose.yml"
		c=$(config)
		[ "$B" = default ] || export BUILDX_BUILDER="$B"
		(cd "$W/$2" && DOCKER_CONFIG=$c DOCKER_BUILDKIT=1 BUILDX_GIT_INFO=false timeout 180 \
			docker compose -p "bnc$2" build >"$W/$2.log" 2>&1) || true
		if hit "$2"; then got=open; else got=blocked; fi
		row "$1" "$2" "$got" "compose build: $3" >"$W/$2.row"
	) &
	jobs="$jobs $!"
}

echo "builder: $B"
[ "$B" = default ] || config >/dev/null
[ "$B" = default ] || row false net-ipv6 "$(docker network inspect -f '{{.EnableIPv6}}' "$B")" "IPv6 on the builder's network"
GW=$(gateway "$([ "$B" = default ] && echo bridge || echo "$B")")
LO=http://127.0.0.1:18080

add blocked add-lo "$LO/add-lo"
add blocked addgit-lo "$LO/addgit-lo.git#main"
add blocked add-gw "http://$GW:18083/add-gw"
add blocked add-othergw "http://$OTHERGW:18083/add-othergw"
i=0
for a in $HOSTADDRS; do
	i=$((i + 1))
	add blocked "add-host$i" "http://$a:18083/add-host$i"
done
if curl -fs -m 4 -o /dev/null "$META_URL"; then
	add blocked add-meta "$META_URL"
else
	echo "skip  add-meta: nothing answers the host at $META_URL"
fi
j=0
for d in $OTHER; do
	j=$((j + 1))
	add blocked "add-other$j" "http://$d/add-other$j"
done
[ -z "$V6" ] || add blocked add-v6ctr "http://[${V6}2]:8080/"
[ -z "$V6" ] || add blocked add-v6host "http://[${V6}1]:18084/add-v6host"
add blocked add-ctr "http://$CTR:8080/"
add blocked add-uctr "http://$UCTR:8080/"
j=0
for d in $ALLOWED; do
	j=$((j + 1))
	add "$([ "$B" = toft-build ] && echo open || echo blocked)" "add-allowed$j" "http://$d/add-allowed$j"
done
ctx blocked ctx-tar "{ context: \"$LO/ctx-tar.tar.gz\" }"
ctx blocked ctx-git "{ context: \"$LO/ctx-git.git#main\" }"
ctx blocked ctx-addl "{ context: ., additional_contexts: { x: \"$LO/ctx-addl.tar\" } }"
ctx blocked ctx-df "{ context: ., dockerfile: \"$LO/ctx-df\" }"
add open add-public https://example.com/
add open addgit-public https://github.com/docker-library/hello-world.git
wait $jobs
cat "$W"/*.row
grep -q FAIL "$W"/*.row && fail=1
rm -f "$W"/*.row

# A remote context given to the CLI itself.
(build ctx-cli "$LO/ctx-cli.tar.gz" || true)
if hit ctx-cli; then got=open; else got=blocked; fi
row blocked ctx-cli "$got" "docker buildx build $LO/ctx-cli.tar.gz"
if printf 'FROM %s\nCOPY README.md /\n' "$IMG" >"$W/df" && build ctx-public https://github.com/docker-library/hello-world.git "$W/df"; then got=open; else got=blocked; fi
row open ctx-public "$got" "a public git context"

# RUN steps: one build, a TCP connection to each (the internet one by
# name, so it is a name lookup too).
probes="lo=127.0.0.1:18080=blocked gw=$GW:18083=blocked othergw=$OTHERGW:18083=blocked meta=169.254.169.254:80=blocked ctr=$CTR:8080=blocked uctr=$UCTR:8080=blocked internet=example.com:443=open"
i=0
for a in $HOSTADDRS; do
	i=$((i + 1))
	probes="$probes host$i=$a:18083=blocked"
done
j=0
for d in $OTHER; do
	j=$((j + 1))
	probes="$probes other$j=$d=blocked"
done
[ -z "$V6" ] || probes="$probes v6ctr=${V6}2:8080=blocked v6host=${V6}1:18084=blocked"
probes="$probes v6linklocal=fe80::1:18084=blocked v6meta=fd00:ec2::254:80=blocked"
j=0
for d in $ALLOWED; do
	j=$((j + 1))
	probes="$probes allowed$j=$d=$([ "$B" = toft-build ] && echo open || echo blocked)"
done
dockerfile run "FROM $IMG
RUN : $stamp; for p in $probes; do n=\${p%%=*}; a=\${p#*=}; a=\${a%=*}; if nc -z -w 4 \${a%:*} \${a##*:}; then echo \"BNC \$n open\"; else echo \"BNC \$n blocked\"; fi; done"
build run || { echo "FAIL  the RUN build itself failed:"; tail -5 "$W/run.log"; fail=1; }
for p in $probes; do
	n=${p%%=*}
	a=${p#*=}
	got=$(sed -n "s/.*BNC $n \(open\|blocked\)\$/\1/p" "$W/run.log" | tail -1)
	row "${p##*=}" "run-$n" "${got:-missing}" "RUN, ${a%=*}"
done

dockerfile run-hostnet "FROM $IMG
RUN --network=host nc -z -w 4 127.0.0.1 18080"
if build run-hostnet; then got=open; else got=blocked; fi
row blocked run-hostnet "$got" "RUN --network=host"

if [ "$B" != default ]; then
	# A RUN step that takes all the memory it can: stopped by the builder's
	# limit, and the next build has a builder.
	dockerfile run-memory "FROM $IMG
RUN tail /dev/zero"
	if build run-memory; then got=open; else got=blocked; fi
	row blocked run-memory "$got" "RUN past the memory limit: $(grep -Eo 'ResourceExhausted|cannot allocate memory' "$W/run-memory.log" | sort -u | tr '\n' ' ')"
	dockerfile after-memory "FROM $IMG
RUN echo again"
	if build after-memory; then got=open; else got=blocked; fi
	row open after-memory "$got" "the build after it"

	# When the whole server runs out of memory, a build goes first: its
	# steps have the highest OOM score. A restart of the builder loses the
	# score and ensure.sh writes it again, so the builder is restarted
	# before each kind of build command Toft runs.
	# oomfile NAME FILE: a Dockerfile whose RUN step prints its score.
	oomfile() {
		mkdir -p "$(dirname "$W/$1/$2")"
		printf 'FROM %s\nRUN : %s %s; echo "BNC adj $(cat /proc/self/oom_score_adj)"\n' "$IMG" "$1" "$stamp" >"$W/$1/$2"
		docker restart "buildx_buildkit_${B}0" >/dev/null
	}
	oomrow() {
		got=$(sed -n 's/.*BNC adj \(-\{0,1\}[0-9]*\)$/\1/p' "$W/$1.log" | tail -1)
		row 1000 "$1" "${got:-missing}" "a RUN step's OOM score after a restart: $2"
	}
	oomfile oom-dockerfile Dockerfile
	(build oom-dockerfile || true)
	oomrow oom-dockerfile "a Dockerfile"
	oomfile oom-nixpacks .nixpacks/Dockerfile
	(build oom-nixpacks "$W/oom-nixpacks" "$W/oom-nixpacks/.nixpacks/Dockerfile" || true)
	oomrow oom-nixpacks "the Dockerfile Nixpacks writes"
	oomfile oom-compose Dockerfile
	printf 'services:\n  c:\n    image: bnc/oom-compose\n    build: .\n' >"$W/oom-compose/compose.yml"
	c=$(config)
	(cd "$W/oom-compose" && BUILDX_BUILDER=$B DOCKER_CONFIG=$c DOCKER_BUILDKIT=1 BUILDX_GIT_INFO=false BUILDKIT_PROGRESS=plain \
		timeout 180 docker compose -p bncoom build >"$W/oom-compose.log" 2>&1) || true
	oomrow oom-compose "docker compose build"
fi

[ -z "${KEEP_LOGS:-}" ] || { mkdir -p "$KEEP_LOGS" && cp "$W"/*.log "$HITS" "$KEEP_LOGS"/; }
exit $fail
