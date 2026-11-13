# Dokploy, for bench/run.sh. DOKPLOY_VERSION pins the release; its API paths
# are from that release's openapi.json. The admin and an API key are made by
# hand (D-131).
: "${DOKPLOY_VERSION:=v0.30.10}"
# shellcheck disable=SC2034 # read by run.sh
PANEL=http://$IP:3000 LOGIN=/ PAGE=/dashboard/projects

api() { # PROCEDURE JSON
	curl -s --fail-with-body -H "x-api-key: $TOKEN" \
		-H 'Content-Type: application/json' --data "$2" "$PANEL/api/$1"
}

install() {
	on "f=\$(mktemp) && curl -fsSL https://dokploy.com/install.sh -o \"\$f\" && DOKPLOY_VERSION=$DOKPLOY_VERSION sh \"\$f\""
	put version "$DOKPLOY_VERSION"
}

panel_up() { curl -sf -o /dev/null --max-time 5 "$PANEL/"; }

account() {
	cat >&2 <<EOF

  By hand, not timed: open $PANEL
  1. Create the admin: $EMAIL, the password above.
  2. Settings → Profile → API/CLI: generate a key.
  3. Sign out and close the tab: an open dashboard would count in idle.
EOF
	printf '  Paste the key: ' >&2
	read -r TOKEN </dev/tty
}

setup() {
	ENV=$(api project.create '{"name":"bench"}' | jq -er .environment.environmentId)
}

app() { # NAME HOST: creates the app with its domain, prints its ID
	a=$(api application.create "$(jq -n --arg n "$1" --arg e "$ENV" '{name: $n, environmentId: $e}')" |
		jq -er .applicationId)
	api domain.create "$(jq -n --arg h "$2" --arg a "$a" '{host: $h, port: 80, https: true,
		certificateType: "letsencrypt", applicationId: $a, domainType: "application", path: "/"}')" >/dev/null
	echo "$a"
}

deploy_image() {
	id=$(app image "$1")
	api application.saveDockerProvider "$(jq -n --arg a "$id" '{applicationId: $a,
		dockerImage: "traefik/whoami:v1.12.0", username: "", password: "", registryUrl: ""}')" >/dev/null
	redeploy "$id"
	echo "$id"
}

deploy_git() {
	id=$(app git "$1")
	# It clones with --branch, so a tag works.
	api application.saveGitProvider "$(jq -n --arg a "$id" '{applicationId: $a,
		customGitUrl: "https://github.com/traefik/whoami", customGitBranch: "v1.12.0",
		customGitBuildPath: "/", watchPaths: []}')" >/dev/null
	api application.saveBuildType "$(jq -n --arg a "$id" '{applicationId: $a, buildType: "dockerfile",
		dockerfile: "Dockerfile", dockerContextPath: "", dockerBuildStage: "",
		herokuVersion: null, railpackVersion: null}')" >/dev/null
	redeploy "$id"
	echo "$id"
}

redeploy() { api application.deploy "{\"applicationId\":\"$1\"}" >/dev/null; }
