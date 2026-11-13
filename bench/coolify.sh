# Coolify, for bench/run.sh. COOLIFY_VERSION pins the release; its API paths
# are from that release's openapi.json. The installer is given the root
# user, but refuses an address whose domain takes no mail (example.com's null
# MX): then the account, like the API token, is made by hand (D-131).
: "${COOLIFY_VERSION:=4.5.3}"
# shellcheck disable=SC2034 # read by run.sh
PANEL=http://$IP:8000 LOGIN=/login PAGE=/projects

api() { # METHOD PATH [JSON]
	curl -s --fail-with-body -X "$1" -H "Authorization: Bearer $TOKEN" \
		-H 'Content-Type: application/json' --data "${3-}" "$PANEL/api/v1$2"
}

install() {
	# AUTOUPDATE=false only so it can't update itself mid-run.
	printf "ROOT_USER_EMAIL='%s'\nROOT_USER_PASSWORD='%s'\nAUTOUPDATE=false\n" "$EMAIL" "$PASSWORD" |
		on "set -a; . /dev/stdin; set +a; f=\$(mktemp) && curl -fsSL https://cdn.coollabs.io/coolify/install.sh -o \"\$f\" && bash \"\$f\" $COOLIFY_VERSION"
	put version "$COOLIFY_VERSION"
}

panel_up() { curl -sf -o /dev/null --max-time 5 "$PANEL/api/v1/health"; }

account() {
	cat >&2 <<EOF

  By hand, not timed: open $PANEL and sign in as $EMAIL. If it asks for a
  root account instead, create it with that email and password within 10
  minutes of Coolify starting (after that: ssh in, docker restart coolify).
  1. Settings → Advanced: turn API Access on.
  2. Keys & Tokens → API Tokens: create one with root ticked.
  3. Servers → localhost: check its proxy is running (if you start it, say so
     with the results: it is outside the install time).
  4. Sign out and close the tab: an open dashboard would count in idle.
EOF
	printf '  Paste the token: ' >&2
	read -r TOKEN </dev/tty
}

setup() {
	project=$(api POST /projects '{"name":"bench"}' | jq -er .uuid) ||
		{ log "Coolify refused to create a project: is the token's permission root?"; exit 1; }
	server=$(api GET /servers | jq -er '.[0].uuid')
}

app() { # PATH JSON: creates the app and starts its first deploy
	api POST "$1" "$2" | jq -er .uuid
}

deploy_image() {
	app /applications/dockerimage "$(jq -n --arg p "$project" --arg s "$server" --arg d "https://$1" '{
		project_uuid: $p, server_uuid: $s, environment_name: "production",
		docker_registry_image_name: "traefik/whoami", docker_registry_image_tag: "v1.12.0",
		ports_exposes: "80", domains: $d, instant_deploy: true}')"
}

deploy_git() {
	app /applications/public "$(jq -n --arg p "$project" --arg s "$server" --arg d "https://$1" '{
		project_uuid: $p, server_uuid: $s, environment_name: "production",
		git_repository: "https://github.com/traefik/whoami", git_branch: "v1.12.0",
		build_pack: "dockerfile", ports_exposes: "80", domains: $d, instant_deploy: true}')"
}

redeploy() { api POST "/deploy?uuid=$1" >/dev/null; }
