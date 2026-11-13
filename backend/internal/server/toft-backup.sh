#!/bin/sh
# Toft backups (D-43). The provisioner (remote servers) and toft upgrade
# (the panel host) install this file at /usr/local/lib/toft/backup.sh;
# toft-backup.timer runs it every 15 minutes through toft-backup.service.
# Change it here, never on a server.
#
# Without $DIR/backup.env (no destination set) it does nothing. Otherwise,
# once a day from TOFT_BACKUP_AT (server time), or at once with "now", it:
#   1. dumps each running database container of the platform, and the
#      panel's own database when TOFT_PANEL_DB names its container; then,
#      where paas-git runs, has it bundle the git repositories that changed
#      (D-79), so they are never behind the database;
#   2. backs up the dumps, the platform's Docker volumes (paas-*) and its
#      containers' bind-mounted folders with restic, encrypted, to
#      RESTIC_REPOSITORY, the repositories as those bundles rather than
#      their files, which a push during the copy could leave inconsistent,
#      and prunes old snapshots (7 daily, 4 weekly, 6 monthly);
#   3. writes the result to $DIR/last.json, which the panel reads.
# The env file is written by the panel, root-only; its values never appear
# on a command line.
set -u

DIR=/var/lib/toft/backup
ENV_FILE=$DIR/backup.env
RESTIC_IMAGE=restic/restic:0.19.1@sha256:136600b6ff6843d61d355f7f71f460a166429f35de6fd11b568fece3c9a4d510

[ -f "$ENV_FILE" ] || exit 0
set -a
# shellcheck source=/dev/null
. "$ENV_FILE"
set +a
umask 077

minutes() { h=${1%%:*} m=${1#*:}; echo $((${h#0} * 60 + ${m#0})); }

today=$(date +%F)
if [ "${1:-}" != now ]; then
	[ "$(cat "$DIR/last-start" 2>/dev/null)" = "$today" ] && exit 0
	[ "$(minutes "$(date +%H:%M)")" -lt "$(minutes "${TOFT_BACKUP_AT:-03:00}")" ] && exit 0
fi
echo "$today" >"$DIR/last-start"
started=$(date -u +%FT%TZ)
errors=""
fail() { errors="${errors:+$errors; }$1"; }
# last bytes of a command's error output, on one line
tail_of() { tail -c 300 "$1" 2>/dev/null | tr '\n' ' '; }

# ── 1. Dumps ──────────────────────────────────────────────────────────────────
rm -rf "$DIR/dumps"
mkdir -p "$DIR/dumps" "$DIR/cache"

# dump writes a dump of database container $1, of kind $2, to stdout. The
# credentials are the container's own environment, read inside it.
# shellcheck disable=SC2016
dump() {
	case "$2" in
	mysql | mariadb)
		docker exec "$1" sh -c 'MYSQL_PWD=${MYSQL_ROOT_PASSWORD:-${MARIADB_ROOT_PASSWORD:-}} exec "$(command -v mysqldump || command -v mariadb-dump)" -uroot --all-databases --single-transaction --routines --events --triggers' ;;
	postgres | postgresql)
		docker exec "$1" sh -c 'exec pg_dumpall -U "${POSTGRES_USER:-postgres}"' ;;
	mongo)
		docker exec "$1" sh -c 'if [ -n "${MONGO_INITDB_ROOT_USERNAME:-}" ]; then exec mongodump --archive --quiet -u "$MONGO_INITDB_ROOT_USERNAME" -p "$MONGO_INITDB_ROOT_PASSWORD" --authenticationDatabase admin; else exec mongodump --archive --quiet; fi' ;;
	esac
}

# The platform's running containers: single-container services carry
# paas.service_id, compose stacks a paas-* project.
# "|", not a tab: read collapses empty fields between whitespace separators,
# which shifted a container's compose project into its service ID.
docker ps --format '{{.Names}}|{{.Image}}|{{.Label "paas.service_id"}}|{{.Label "com.docker.compose.project"}}' >"$DIR/containers" ||
	fail "could not list containers"
: >"$DIR/platform"
while IFS='|' read -r name image sid project; do
	case "$project" in paas-*) sid=${sid:-compose} ;; esac
	[ -n "$sid" ] || [ "$name" = "${TOFT_PANEL_DB:-}" ] || continue
	echo "$name" >>"$DIR/platform"
	kind=${image##*/}
	kind=${kind%%@*}
	kind=${kind%%:*}
	case "$kind" in
	mysql | mariadb | postgres | postgresql | mongo) ;;
	*) continue ;;
	esac
	if ! dump "$name" "$kind" >"$DIR/dumps/$name.dump" 2>"$DIR/dump.err"; then
		fail "the dump of $name failed: $(tail_of "$DIR/dump.err")"
		rm -f "$DIR/dumps/$name.dump"
	fi
done <"$DIR/containers"

# The git repositories (D-79), after the panel's database: a verified bundle
# of each one whose refs changed, on paas-git's own volume (@bundles). A
# repository that can't be bundled keeps its last good bundle and fails the
# run, naming it.
if grep -q '^paas-git|' "$DIR/containers"; then
	docker exec paas-git toft-git backup >"$DIR/git.json" 2>"$DIR/git.err" ||
		fail "the git repositories' bundles: $(tail_of "$DIR/git.err")"
fi

# ── 2. Back up and prune ──────────────────────────────────────────────────────
set -- -v "$DIR/dumps:/data/dumps:ro"
docker volume ls -q --filter name=paas- | grep '^paas-' >"$DIR/volumes"
while read -r v; do
	set -- "$@" -v "$v:/data/volumes/$v:ro"
done <"$DIR/volumes"
# Bind-mounted folders and files of the platform's containers. A path with
# ":" or "," cannot be passed to -v, and is reported.
while read -r name; do
	docker inspect -f '{{range .Mounts}}{{if eq .Type "bind"}}{{println .Source}}{{end}}{{end}}' "$name"
done <"$DIR/platform" | sort -u >"$DIR/binds"
while read -r src; do
	case "$src" in
	"" | /var/run/docker.sock | /run/docker.sock | /proc/* | /sys/* | /dev/*) ;;
	*:* | *,*) fail "not backed up, its path has \":\" or \",\": $src" ;;
	*) [ -e "$src" ] && set -- "$@" -v "$src:/data/binds$src:ro" ;;
	esac
done <"$DIR/binds"

restic() {
	docker run --rm --env-file "$ENV_FILE" --memory 512m -v "$DIR/cache:/root/.cache/restic" "$@"
}
host=$(hostname)
added=0 total=0 snapshot=""
# init first: it creates a missing bucket and repository in a second, and
# fails at once on one that exists. (Asking a missing bucket for the
# repository's config is retried for 15 minutes.)
if ! restic "$RESTIC_IMAGE" init >/dev/null 2>"$DIR/restic.err" &&
	! grep -q "already initialized" "$DIR/restic.err"; then
	fail "the repository could not be created: $(tail_of "$DIR/restic.err")"
else
	restic "$@" "$RESTIC_IMAGE" backup /data --host "$host" --tag toft --exclude /data/volumes/paas-git/@hashed \
		--json >"$DIR/backup.json" 2>"$DIR/restic.err"
	code=$?
	summary=$(grep '"message_type":"summary"' "$DIR/backup.json")
	added=$(printf '%s' "$summary" | sed -n 's/.*"data_added":\([0-9]*\).*/\1/p')
	total=$(printf '%s' "$summary" | sed -n 's/.*"total_bytes_processed":\([0-9]*\).*/\1/p')
	snapshot=$(printf '%s' "$summary" | sed -n 's/.*"snapshot_id":"\([0-9a-f]*\)".*/\1/p')
	case $code in
	0) ;;
	3) fail "some files could not be read: $(tail_of "$DIR/restic.err")" ;;
	*) fail "the backup failed: $(tail_of "$DIR/restic.err")" ;;
	esac
	if [ -n "$snapshot" ] && ! restic "$RESTIC_IMAGE" forget --host "$host" --tag toft \
		--keep-daily 7 --keep-weekly 4 --keep-monthly 6 --prune >/dev/null 2>"$DIR/restic.err"; then
		fail "old snapshots could not be pruned: $(tail_of "$DIR/restic.err")"
	fi
fi

# ── 3. The result ─────────────────────────────────────────────────────────────
json() { printf '%s' "$1" | tr -d '\000-\037' | sed 's/\\/\\\\/g; s/"/\\"/g'; }
ok=true
[ -n "$errors" ] && ok=false
printf '{"started_at":"%s","finished_at":"%s","ok":%s,"bytes_added":%s,"bytes_total":%s,"snapshot":"%s","error":"%s"}\n' \
	"$started" "$(date -u +%FT%TZ)" "$ok" "${added:-0}" "${total:-0}" "$snapshot" "$(json "$errors")" >"$DIR/last.json.tmp" &&
	mv "$DIR/last.json.tmp" "$DIR/last.json"
[ "$ok" = true ]
