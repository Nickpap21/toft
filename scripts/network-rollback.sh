#!/bin/sh
# Reconnects app containers to the control plane's network (TRAEFIK_NETWORK,
# default paas-network) with the aliases they have on their app networks
# (paas-env-*, paas-proj-*, paas-org-*). Run it before going back from
# TOFT_APP_NETWORKS=isolated to legacy, or before deploying a backend from
# before package C, so environments are not split across networks. It only
# adds connections; it never disconnects or restarts anything.
#
# App containers are those with a paas.service_id label, or in a compose
# project named like the platform's (paas-<name>-<id8> or paas-<id8>). The
# panel's own containers are skipped.
set -euf # -f: aliases are never globbed

NET="${TRAEFIK_NETWORK:-paas-network}"
docker network inspect "$NET" >/dev/null

for id in $(docker ps -q); do
    name=$(docker inspect -f '{{.Name}}' "$id")
    name=${name#/}
    case "$name" in
        paas-backend|paas-postgres|paas-frontend|paas-traefik) continue ;;
    esac
    svc=$(docker inspect -f '{{index .Config.Labels "paas.service_id"}}' "$id")
    proj=$(docker inspect -f '{{index .Config.Labels "com.docker.compose.project"}}' "$id")
    composesvc=$(docker inspect -f '{{index .Config.Labels "com.docker.compose.service"}}' "$id")
    if [ -z "$svc" ] && ! printf '%s' "$proj" | grep -Eq '^paas-([a-z0-9-]+-)?[0-9a-f]{8}$'; then
        continue
    fi
    if [ -n "$(docker inspect -f "{{if index .NetworkSettings.Networks \"$NET\"}}on{{end}}" "$id")" ]; then
        continue
    fi

    # Aliases on the container's app networks, plus its compose service name.
    aliases=$(docker inspect -f '{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{range $v.Aliases}}{{.}} {{end}}{{"\n"}}{{end}}' "$id" |
        while read -r net rest; do
            case "$net" in
                paas-env-*|paas-proj-*|paas-org-*) printf '%s ' "$rest" ;;
            esac
        done)
    if [ -z "$(docker inspect -f '{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}' "$id" | tr ' ' '\n' | grep -E '^paas-(env|proj|org)-')" ]; then
        continue # never was on an app network
    fi
    [ -n "$composesvc" ] && aliases="$aliases $composesvc"

    set --
    for a in $(printf '%s\n' $aliases | sort -u); do
        set -- "$@" --alias "$a"
    done
    docker network connect "$@" "$NET" "$id"
    echo "reconnected $name to $NET${aliases:+ (aliases:$(printf ' %s' $(printf '%s\n' $aliases | sort -u)))}"
done
