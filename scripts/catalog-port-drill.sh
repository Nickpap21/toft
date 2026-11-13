#!/bin/sh
# Deploys every catalog template through a Toft panel and checks that each
# one listens on the port the catalog declares (and its second port), from
# a non-loopback address, under Toft's own container settings. An image can
# move ports under them: pgAdmin binds 80 through a file capability that
# no-new-privileges (D-93) strips, so it moved to 8080. Prints one line per
# template with its idle memory, and deletes what it created.
#
# Usage: scripts/catalog-port-drill.sh [slug...]   (default: the whole catalog)
#
#   TOFT_URL     the panel, such as https://panel.example.com
#   TOFT_TOKEN   a session token or API key of an owner or developer
#   TOFT_ENV     an environment to create the services in (use a scratch one:
#                templates keep their network aliases)
#   TOFT_SERVER  the server to deploy to
#   SERVER_SH    runs sh on that server with the script on stdin, such as
#                "ssh root@203.0.113.10 sh" or "docker exec -i toft-vm sh"
#   WAIT         seconds to wait for a port after the deploy (default 180)
#   DEPLOY_WAIT  seconds to wait for a deploy to finish (default 900)
#   KEEP=1       leave the services in place
#   DRILL_DOMAIN the domain compose apps get a name under (default
#                example.com; nothing resolves or certifies it)
#
# Needs curl and jq here, and root on the server (it reads the containers'
# /proc/<pid>/net/tcp*, so it needs no tool inside the images).
set -eu

: "${TOFT_URL:?}" "${TOFT_TOKEN:?}" "${TOFT_ENV:?}" "${TOFT_SERVER:?}" "${SERVER_SH:?}"
WAIT="${WAIT:-180}"
API="$TOFT_URL/api/v1"

api() { # method path [body]
	curl -sS --max-time 60 -X "$1" -H "Authorization: Bearer $TOFT_TOKEN" -H 'Content-Type: application/json' \
		${3:+--data "$3"} "$API$2"
}

# The service being drilled is deleted however the script ends, unless KEEP=1.
id=
cleanup() { [ -z "$id" ] || [ "${KEEP:-}" = 1 ] || api DELETE "/services/$id?confirm=drill-$slug" >/dev/null || true; id=; }
trap cleanup EXIT
trap 'exit 130' INT TERM

catalog=$(api GET /catalog)
[ $# -gt 0 ] || set -- $(printf '%s' "$catalog" | jq -r '.data[].slug')

fail=0
for slug in "$@"; do
	t=$(printf '%s' "$catalog" | jq -c --arg s "$slug" '.data[] | select(.slug == $s)')
	[ -n "$t" ] || { echo "$slug: not in the catalog"; fail=1; continue; }
	body=$(printf '%s' "$t" | jq -c --arg server "$TOFT_SERVER" '
		{name: ("drill-" + .slug), type: "web", port: .default_port, addon_slug: .slug, server_id: $server}
		+ if .kind == "compose"
		  then {source_type: "docker-compose", compose_content: .compose_content, compose_route_service: .route_service}
		  else {source_type: "docker-image", docker_image: .docker_image, network_alias: .network_alias, volumes: .volumes}
		       + if .start_command then {start_command: .start_command} else {} end
		  end')
	created=$(api POST "/environments/$TOFT_ENV/services" "$body")
	id=$(printf '%s' "$created" | jq -r '.data.id // empty')
	[ -n "$id" ] || { echo "$slug: not created: $(printf '%s' "$created" | jq -c '.error // .')"; fail=1; continue; }

	# Every variable: its default, or a random value for a secret or an empty one.
	printf '%s' "$t" | jq -c '.env_var_specs[]' | while read -r spec; do
		key=$(printf '%s' "$spec" | jq -r .key)
		val=$(printf '%s' "$spec" | jq -r 'if .is_secret or .default == "" then empty else .default end')
		# A generated one ("long", "hex64") gets 64 hex characters, as the panel makes.
		[ -z "$(printf '%s' "$spec" | jq -r '.generate // empty')" ] || val=$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')
		[ -n "$val" ] || val="Drill$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')"
		if [ "$(printf '%s' "$spec" | jq -r .is_secret)" = true ]; then
			api POST "/services/$id/secrets" "$(jq -nc --arg k "$key" --arg v "$val" '{key: $k, value: $v}')" >/dev/null
		else
			api POST "/services/$id/env-vars" "$(jq -nc --arg k "$key" --arg v "$val" '{key: $k, value: $v}')" >/dev/null
		fi
	done

	# Compose apps read their own address (TOFT_URL): a name that needs no DNS.
	if [ "$(printf '%s' "$t" | jq -r .kind)" = compose ]; then
		api POST "/services/$id/domains" "{\"hostname\":\"drill-$slug.${DRILL_DOMAIN:-example.com}\"}" >/dev/null
	fi
	started=$(api POST "/services/$id/deployments" '{}')
	dep=$(printf '%s' "$started" | jq -r '.data.id // empty')
	status=pending
	[ -n "$dep" ] || status="not started: $(printf '%s' "$started" | jq -c '.error // .')"
	waited=0
	while [ "$status" = pending ] || [ "$status" = building ]; do
		[ "$waited" -lt "${DEPLOY_WAIT:-900}" ] || { status="still $status after ${waited}s"; break; }
		sleep 5
		waited=$((waited + 5))
		status=$(api GET "/services/$id/deployments/$dep" | jq -r '.data.status')
	done

	ports=$(printf '%s' "$t" | jq -r '[.default_port, (.secondary_port // empty)] | map(tostring) | join(" ")')
	route=$(printf '%s' "$t" | jq -r '.route_service // ""')
	if [ "$status" != running ]; then
		echo "$slug: deploy $status (service $id)"
		fail=1
	else
		# On the server: the routed container, then each port in LISTEN (0A)
		# on an address other than 127.0.0.1 or ::1, until WAIT runs out.
		out=$($SERVER_SH <<EOF
id=$id; route=$route; wait=$WAIT
if [ -z "\$route" ]; then
	c=\$(docker ps -q --filter label=paas.service_id=\$id | head -1)
else
	c=\$(docker ps --filter label=com.docker.compose.service=\$route --format '{{.ID}} {{.Label "com.docker.compose.project"}}' |
		awk -v s="-\$(printf %.8s \$id)" 'substr(\$2, length(\$2) - 8) == s { print \$1; exit }')
fi
[ -n "\$c" ] || { echo "no running container"; exit 0; }
pid=\$(docker inspect -f '{{.State.Pid}}' "\$c")
listening() {
	awk 'NR > 1 && \$4 == "0A" && \$2 !~ /^0100007F:/ && substr(\$2, 1, 33) != "00000000000000000000000001000000:" { split(\$2, a, ":"); print a[2] }' \
		/proc/\$pid/net/tcp /proc/\$pid/net/tcp6 2>/dev/null | sort -u
}
missing="$ports"
for i in \$(seq 1 \$((wait / 3))); do
	missing=
	for p in $ports; do
		listening | grep -qx "\$(printf %04X \$p)" || missing="\$missing \$p"
	done
	[ -z "\$missing" ] && break
	sleep 3
done
if [ -n "\$missing" ]; then
	echo "not listening on\$missing; listens on: \$(for h in \$(listening); do printf '%d ' 0x\$h; done)"
else
	sleep 60 # idle a minute before reading memory
	echo "ok, idle memory \$(docker stats --no-stream --format '{{.MemUsage}}' "\$c")"
fi
EOF
)
		case "$out" in ok*) ;; *) fail=1 ;; esac
		echo "$slug ($ports): $out"
	fi
	cleanup
done
exit $fail
