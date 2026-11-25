#!/bin/bash
# Proves this repo's compose files move real PostgreSQL 17 data to 18 on `up`: 17 data made
# by the previous compose files keeps its rows, roles, passwords and collation, upgrading over
# the running 17; a dump beside a live 17 is refused; the 17 volume is untouched and 17 still
# starts on it; a rerun is a no-op; a bad dump never yields an empty cluster; a fresh install
# initialises. Needs Docker and git.
# usage: test-postgres-17-to-18.sh base|replicas
set -Eeuo pipefail
mode=${1:?base|replicas}
root=$(cd "$(dirname "$0")/.." && pwd)
# The last master commit whose compose files ran PostgreSQL 17.
prev=${PREV:-fed0fb34a0e58248f08780ad878ac626f1b906eb}
files=(docker-compose.yml)
case $mode in
base) U=postgres P=postgrespassword old_vol=pgdata new_vol=postgres ;;
replicas)
  files+=(docker-compose.replicas.yml)
  U=ky_server P=synthetic-upgrade-password old_vol=pgdata-replicas new_vol=postgres-replicas
  export KY_POSTGRES_PASSWORD=$P KY_DB_DSN="postgres://ky_server:$P@postgres:5432/ky_server?sslmode=disable" \
    KY_SESSION_SECRET=synthetic-upgrade-session-secret KY_SCIM_TOKEN=synthetic-upgrade-scim-token \
    KY_ENCRYPTION_KEY=abababababababababababababababababababababababababababababababab \
    KY_PROXY_IP=172.31.251.2 KY_REPLICA_SUBNET=172.31.251.0/24 ;;
*) echo "usage: $0 base|replicas" >&2; exit 2 ;;
esac
proj=kypg1718-$mode-$$
work=$(mktemp -d)
mkdir -p "$work/old" "$work/new/scripts"
git -C "$root" cat-file -e "$prev^{commit}" 2>/dev/null || git -C "$root" fetch -q --depth=1 origin "$prev"
for f in "${files[@]}"; do
  git -C "$root" show "$prev:$f" > "$work/old/$f"
  cp "$root/$f" "$work/new/$f"
done
cp -p "$root/scripts/postgres-17-to-18.sh" "$work/new/scripts/"
# Ports do not take part in the upgrade, and 5432 may be taken on a shared host.
printf 'services:\n  postgres:\n    ports: !reset []\n' > "$work/ports.yml"

dc() {
  local dir=$work/$1; shift
  local args=(--project-directory "$dir" -p "$proj" --profile postgres --env-file /dev/null)
  for f in "${files[@]}"; do args+=(-f "$dir/$f"); done
  docker compose "${args[@]}" -f "$work/ports.yml" "$@"
}
cleanup() { dc new down -v -t 5 >/dev/null 2>&1 || true; dc old down -v -t 5 >/dev/null 2>&1 || true; rm -rf "$work"; }
trap cleanup EXIT
fail() { echo "FAIL ($mode): $*" >&2; dc new logs --no-color --tail 20 >&2 2>&1 || true; exit 1; }
# Ready means the final server answers over TCP, never the entrypoint's socket-only one.
wait_ready() { for _ in $(seq 1 180); do dc "$1" exec -T postgres pg_isready -q -h 127.0.0.1 -U "$U" -d ky_server 2>/dev/null && return 0; sleep 1; done; return 1; }
# psql from a second container on the server's network: inside the server's own container
# initdb trusts 127.0.0.1, so a wrong password would pass there.
q18() {
  local id net
  id=$(dc new ps -q postgres)
  net=$(docker inspect -f '{{range $k, $v := .NetworkSettings.Networks}}{{$k}}{{"\n"}}{{end}}' "$id" | head -1)
  docker run --rm --network "$net" -e PGPASSWORD="${3:-$P}" "$(docker inspect -f '{{.Config.Image}}' "$id")" \
    psql -h "$(docker inspect -f "{{(index .NetworkSettings.Networks \"$net\").IPAddress}}" "$id")" \
    -U "${4:-$U}" -d "${2:-ky_server}" -XAtc "$1"
}
# Whole log first: grep -q closing a pipe early would fail it under pipefail.
logs() { dc new logs --no-color "$1" 2>&1; }
# A shell on one volume, through the service that mounts it, without starting PostgreSQL.
in_vol() { dc "$1" run --rm --no-deps -T --entrypoint sh postgres -c "$2"; }

# 1. Real 17 data, made by the previous compose files.
dc old up -d postgres >/dev/null 2>&1 || fail "previous compose did not start"
wait_ready old || fail "17 did not start"
dc old exec -T postgres psql -U "$U" -d ky_server -X -q -v ON_ERROR_STOP=1 >/dev/null <<'SQL' || fail "seed"
create role reader login password 'readerpass';
create database cdb owner reader locale 'C' encoding 'UTF8' template template0;
create table t(id bigserial primary key, s text unique, j jsonb);
insert into t(s, j) select 'row' || g, jsonb_build_object('g', g) from generate_series(1, 50000) g;
create extension pgcrypto;
select lo_from_bytea(0, 'blob');
\connect cdb reader
create table c(x text collate "C" primary key);
insert into c values ('a'), ('B'), ('é');
SQL
# 1b. The one-shot run by hand beside the live 17 refuses and writes nothing.
out=$(dc new run --rm --no-deps -T postgres-17-dump 2>&1) && fail "dump ran beside a live 17 server: $out"
grep -q 'postmaster.pid' <<<"$out" || fail "no live-server message: $out"
in_vol new 'test ! -e /var/lib/postgresql/upgrade-from-17/dump.sql' || fail "dump written beside a live 17 server"

# 2. The current compose files upgrade it on `up`, over the running 17 as an operator would:
# Compose must stop 17 before the one-shot, which refuses a data directory still in use.
dc new up -d postgres >/dev/null 2>&1 || fail "dump one-shot or 18 did not start"
wait_ready new || fail "18 did not start after the restore"
grep -q 'postgres-17-to-18: restored' <<<"$(logs postgres)" || fail "no restore ran"
[ "$(q18 'show server_version_num' | cut -c1-2)" = 18 ] || fail "not 18"
q18 'select 1' ky_server wrong-password >/dev/null 2>&1 && fail "a wrong password logged in: the password checks prove nothing"
[ "$(q18 "select count(*) || ',' || max(id) || ',' || sum((j->>'g')::int) from t")" = "50000,50000,1250025000" ] || fail "rows"
[ "$(q18 'select count(*) from pg_largeobject_metadata')" = 1 ] || fail "large object"
[ "$(q18 "select extname from pg_extension where extname = 'pgcrypto'")" = pgcrypto ] || fail "extension"
[ "$(q18 'show data_checksums')" = on ] || fail "checksums"
[ "$(q18 'show data_directory')" = /var/lib/postgresql/18/docker ] || fail "data directory"
[ "$(q18 'select string_agg(x, $$,$$ order by x) from c' cdb readerpass reader)" = "B,a,é" ] || fail "C collation or reader password"
[ "$(q18 "select datcollate from pg_database where datname = 'cdb'")" = C ] || fail "database locale"
[ "$(in_vol old 'cat /var/lib/postgresql/data/PG_VERSION')" = 17 ] || fail "17 data changed"

# 3. Rollback: the previous compose files still start 17 on the kept volume.
dc new down -t 30 >/dev/null 2>&1
dc old up -d postgres >/dev/null 2>&1 || fail "previous compose did not start again"
wait_ready old || fail "17 no longer starts on its data"
[ "$(dc old exec -T postgres psql -U "$U" -d ky_server -XAtc 'select count(*) from t')" = 50000 ] || fail "17 rows after rollback"
dc old down -t 30 >/dev/null 2>&1

# 4. Rerun is a no-op.
dc new up -d postgres >/dev/null 2>&1 || fail "18 did not start again"
wait_ready new || fail "18 did not restart"
grep -q 'already done' <<<"$(logs postgres-17-dump)" || fail "rerun dumped again"
grep -q 'restored' <<<"$(logs postgres)" && fail "rerun restored again"
[ "$(q18 'select count(*) from t')" = 50000 ] || fail "rows after rerun"
dc new down -t 30 >/dev/null 2>&1

# 5. A dump that fails to restore never yields an empty cluster, however often it restarts.
in_vol new 'mv /var/lib/postgresql/18/docker /var/lib/postgresql/18/docker.saved && echo "THIS IS NOT SQL;" >> /var/lib/postgresql/upgrade-from-17/dump.sql'
dc new up -d postgres >/dev/null 2>&1 || fail "bad dump: up failed before postgres started"
id=$(dc new ps -aq postgres)
for _ in $(seq 1 180); do [ "$(docker inspect -f '{{.RestartCount}}' "$id")" -ge 2 ] && break; sleep 1; done
[ "$(docker inspect -f '{{.RestartCount}}' "$id")" -ge 2 ] || fail "bad dump: postgres did not fail and restart"
dc new stop -t 5 postgres >/dev/null 2>&1
in_vol new 'test ! -e /var/lib/postgresql/18/docker' || fail "bad dump produced a cluster"
dc new down -t 5 >/dev/null 2>&1

# 6. A fresh install: nothing to move, a normal initdb.
dc new down -v >/dev/null 2>&1
docker volume inspect "${proj}_$old_vol" "${proj}_$new_vol" >/dev/null 2>&1 && fail "volumes left before the fresh install"
dc new up -d postgres >/dev/null 2>&1 || fail "fresh install did not start"
wait_ready new || fail "fresh 18 did not start"
grep -q 'nothing to move' <<<"$(logs postgres-17-dump)" || fail "fresh install did not skip the dump"
grep -q 'restored' <<<"$(logs postgres)" && fail "fresh install restored something"
[ "$(q18 'show data_directory')" = /var/lib/postgresql/18/docker ] || fail "fresh data directory"
echo "PASS ($mode): live 17 refused, upgrade over a running 17, rollback to 17, rerun, bad dump, fresh install"
