# Toft, for bench/run.sh. TOFT_VERSION is the release (vX.Y.Z). Until launch
# the release and its images are private: with TOFT_REGISTRY_TOKEN set, the
# installer comes through `gh` here and the token goes to it on stdin.
: "${TOFT_VERSION:?set TOFT_VERSION to the Toft release, as vX.Y.Z}"
# shellcheck disable=SC2034 # read by run.sh
PANEL=https://$IP LOGIN=/ PAGE=/projects
GIT_TAG=v1.12.0

api() { # METHOD PATH [JSON]
	curl -sk --fail-with-body -X "$1" -H "Authorization: Bearer $TOKEN" \
		-H 'Content-Type: application/json' --data "${3-}" "$PANEL/api/v1$2"
}

install() {
	if [ -n "${TOFT_REGISTRY_TOKEN:-}" ]; then
		{
			printf 'export TOFT_REGISTRY_TOKEN=%s\n' "$TOFT_REGISTRY_TOKEN"
			gh release download "$TOFT_VERSION" -R Nickpap21/toft -p install.sh -O -
		} | on sh -s -- --version "$TOFT_VERSION"
	else
		on "curl -fsSL https://github.com/Nickpap21/toft/releases/download/$TOFT_VERSION/install.sh | sh -s -- --version $TOFT_VERSION"
	fi
	put version "$TOFT_VERSION"
}

panel_up() { curl -skf -o /dev/null --max-time 5 "$PANEL/api/v1/health"; }

account() {
	code=$(on "sed -n 's/^TOFT_SETUP_TOKEN=//p' /etc/toft/secrets.env | tail -n1")
	TOKEN=$(PW=$PASSWORD CODE=$code jq -n --arg e "$EMAIL" \
		'{email: $e, name: "Bench", password: env.PW, setup_token: env.CODE}' |
		curl -sk --fail-with-body -H 'Content-Type: application/json' --data @- "$PANEL/api/v1/auth/register" |
		jq -er .data.token)
	# Setup as a new owner finishes it on one server: apps on this server
	# (refused below 3.5 GiB, D-132), the recovery kit acknowledged. Until then
	# the dashboard sends an operator to /setup.
	role=$(curl -sk -o /dev/null -w '%{http_code}' -X PUT -H "Authorization: Bearer $TOKEN" \
		-H 'Content-Type: application/json' --data '{"role":"apps"}' "$PANEL/api/v1/instance/setup/role")
	put role_apps_http "$role"
	[ "$role" = 200 ] || { log "Toft refused the apps role on this server (HTTP $role)"; exit 1; }
	api PATCH /instance/setup '{"kit_acknowledged":true}' >/dev/null
	api PATCH /instance/setup '{"finished":true}' >/dev/null
	TOKEN=$(api POST /user/api-keys '{"name":"bench"}' | jq -er .data.key)
}

setup() {
	project=$(api POST /projects '{"name":"bench"}' | jq -er .data.id)
	ENV=$(api GET "/projects/$project/environments" | jq -er '.data[0].id')
}

# deploy_image and deploy_git create the app on the panel host, route the
# host name $1 to it and start its first deploy; they print its ID.
deploy_image() {
	id=$(api POST "/environments/$ENV/services" \
		'{"name":"image","type":"web","source_type":"docker-image","docker_image":"traefik/whoami:v1.12.0","port":80}' |
		jq -er .data.id)
	api POST "/services/$id/domains" "{\"hostname\":\"$1\",\"tls_enabled\":true}" >/dev/null
	redeploy "$id" image
	echo "$id"
}

deploy_git() {
	id=$(api POST "/environments/$ENV/services" \
		'{"name":"git","type":"web","source_type":"git","git_repo_url":"https://github.com/traefik/whoami","git_branch":"master","build_pack":"dockerfile","port":80}' |
		jq -er .data.id)
	api POST "/services/$id/domains" "{\"hostname\":\"$1\",\"tls_enabled\":true}" >/dev/null
	redeploy "$id" git
	echo "$id"
}

redeploy() { # ID KIND
	if [ "$2" = git ]; then
		api POST "/services/$1/deployments" "{\"git_ref\":\"$GIT_TAG\",\"git_ref_type\":\"tag\"}" >/dev/null
	else
		api POST "/services/$1/deployments" '{}' >/dev/null
	fi
}
