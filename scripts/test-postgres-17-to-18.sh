#!/bin/bash
# Proves this repo's compose files move real PostgreSQL 17 data to 18 on `up`: 17 data made
# by the previous compose files keeps its rows, roles, passwords and collation, upgrading over
# the running 17; a dump beside a live 17 is refused; the 17 volume is untouched and 17 still
# starts on it; after a rollback that wrote on 17, 18 is refused until the operator moves 17
# again or keeps 18, also after the dump was cleaned up; a rerun is a no-op; a bad dump never
# yields an empty cluster; a fresh install initialises. Needs Docker and git.
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
start17() { { dc old up -d postgres >/dev/null 2>&1 && wait_ready old; } || fail "$1"; }
up18() { { dc new up -d postgres >/dev/null 2>&1 && wait_ready new; } || fail "$1"; }
# The one-shot refuses a 17 that ran after the move, with the operator's two choices, and 18
# does not start.
refused() {
  dc new up -d postgres >/dev/null 2>&1 && fail "$1"
  grep -q 'has run on .* since it was moved to 18' <<<"$(logs postgres-17-dump)" || fail "no rollback message: $(logs postgres-17-dump)"
  [ -z "$(dc new ps -q postgres)" ] || fail "$1 (postgres is running)"
  dc new down -t 30 >/dev/null 2>&1
}

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
[ "$(docker inspect -f '{{.State.Running}}' "$(dc old ps -q postgres)")" = true ] || fail "17 is not running before the upgrade"
dc new up -d postgres >/dev/null 2>&1 || fail "dump one-shot or 18 did not start"
wait_ready new || fail "18 did not start after the restore"
grep -q 'postgres-17-to-18: dumped' <<<"$(logs postgres-17-dump)" || fail "the one-shot did not dump the running 17"
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

# 3. Rollback: the previous compose files still start 17 on the kept volume, and it takes writes.
dc new down -t 30 >/dev/null 2>&1
start17 "previous compose did not start again"
[ "$(dc old exec -T postgres psql -U "$U" -d ky_server -XAtc 'select count(*) from t')" = 50000 ] || fail "17 rows after rollback"
dc old exec -T postgres psql -U "$U" -d ky_server -XAtqc "insert into t(s) values ('written on 17 after the move')" || fail "write on 17"
dc old down -t 30 >/dev/null 2>&1
# 3b. Back to the current files: 17 ran after the move, so 18 must not start on the older copy.
refused "18 started without the row written on 17 during the rollback"
# 3c. The documented "move again": rename 18/ and the dump, then up; 18 gets the new row.
in_vol new 'cd /var/lib/postgresql && mv 18 18.replaced && mv upgrade-from-17 upgrade-from-17.replaced'
up18 "18 did not start after moving again"
grep -q 'postgres-17-to-18: dumped' <<<"$(logs postgres-17-dump)" || fail "moving again did not dump"
[ "$(q18 'select count(*) from t')" = 50001 ] || fail "moving again lost the row written on 17"
in_vol new 'test -s /var/lib/postgresql/18.replaced/docker/PG_VERSION' || fail "the replaced 18 cluster is gone"
dc new down -t 30 >/dev/null 2>&1

# 4. Rerun is a no-op.
up18 "18 did not start again"
grep -q 'already done' <<<"$(logs postgres-17-dump)" || fail "rerun dumped again"
grep -q 'restored' <<<"$(logs postgres)" && fail "rerun restored again"
[ "$(q18 'select count(*) from t')" = 50001 ] || fail "rows after rerun"
dc new down -t 30 >/dev/null 2>&1

# 4b. "Keep 18" after a rollback: delete the checkpoint marker, then up.
start17 "17 did not start for the keep-18 check"; dc old down -t 30 >/dev/null 2>&1
refused "18 started after a rollback (keep-18 check)"
in_vol new 'rm /var/lib/postgresql/18/moved-from-17.checkpoint'
up18 "keep 18 was not accepted"
[ "$(q18 'select count(*) from t')" = 50001 ] || fail "rows after keep 18"
dc new down -t 30 >/dev/null 2>&1

# 4c. The documented clean-up removes the dump and keeps 17: 18 still starts, and a rollback
# after it is still refused. The dump is moved aside, not removed, for step 5.
in_vol new 'mv /var/lib/postgresql/upgrade-from-17 /var/lib/postgresql/upgrade-from-17.kept'
up18 "18 did not start after the documented clean-up"
dc new down -t 30 >/dev/null 2>&1
start17 "17 did not start for the clean-up check"; dc old down -t 30 >/dev/null 2>&1
refused "after the clean-up, 18 started without what 17 wrote since"
in_vol new 'cd /var/lib/postgresql && rm 18/moved-from-17.checkpoint && mv upgrade-from-17.kept upgrade-from-17'

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
echo "PASS ($mode): live 17 refused, upgrade over a running 17, rollback to 17, refused re-upgrade, move again, rerun, keep 18, clean-up, bad dump, fresh install"
