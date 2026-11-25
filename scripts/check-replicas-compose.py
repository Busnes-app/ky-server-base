#!/usr/bin/env python3
"""Render docker-compose.replicas.yml merges with synthetic inputs; start no containers."""
import hashlib
import json
import os
import re
import subprocess
from pathlib import Path

root = Path(__file__).resolve().parents[1]
required = {
    "KY_DB_DSN": "postgres://ky_server:synthetic@postgres:5432/ky_server?sslmode=disable",
    "KY_SESSION_SECRET": "synthetic-replica-session-secret",
    "KY_SCIM_TOKEN": "synthetic-replica-scim-token",
    "KY_ENCRYPTION_KEY": "ab" * 32,
    "KY_POSTGRES_PASSWORD": "synthetic-postgres-password",
    "KY_PROXY_IP": "172.31.250.2",
    "KY_REPLICA_SUBNET": "172.31.250.0/24",
}
# PostgreSQL 18, and the one-shot that dumps a 17 volume for it (scripts/postgres-17-to-18.sh,
# vendored byte for byte from the shared upgrade kit).
PG18 = "postgres:18.7-alpine3.24@sha256:0115267ab5a509022df9319c77be365cef335adb71556c75f60f5360ee247e71"
PG17 = "postgres:17.12-alpine3.24@sha256:f9cf3e1f0066f0eb0c48707d864f0ac5941e9ff454a072077d97a4d278d07d73"
KIT_SHA256 = "2992644109716cd34297fbcb76eba8907f4a5e3031a12c05aade010e5ea2b571"
kit = root / "scripts" / "postgres-17-to-18.sh"
assert hashlib.sha256(kit.read_bytes()).hexdigest() == KIT_SHA256, f"{kit} differs from the upgrade kit"
assert kit.stat().st_mode & 0o777 == 0o755, f"{kit} is not mode 755"
base_env = {k: v for k, v in os.environ.items() if k not in required and not k.startswith("KY_")}


def compose(files, env):
    command = ["docker", "compose", "--env-file", "/dev/null"]
    for name in files:
        command += ["-f", name]
    return subprocess.run(command + ["config", "--format", "json"], cwd=root, env=env,
                          capture_output=True, text=True)


files = ["docker-compose.yml", "docker-compose.replicas.yml"]
for missing in required:
    result = compose(files, base_env | {k: v for k, v in required.items() if k != missing})
    assert result.returncode != 0, f"rendered without {missing}"
    assert missing in result.stderr, f"error does not name {missing}: {result.stderr}"
print("PASS replicas: each required variable is named when missing")

for chain, image in [([], "ghcr.io/busnes-app/ky-server-base:latest"), (["docker-compose.build.yml"], "ky_server_base:local")]:
    chain_files = ["docker-compose.yml"] + chain + ["docker-compose.replicas.yml"]
    result = compose(chain_files, base_env | required | {"KY_REPLICAS": "3"})
    assert result.returncode == 0, result.stderr
    services = json.loads(result.stdout)["services"]
    app, db, proxy = services["app"], services["postgres"], services["proxy"]
    assert app["image"] == image, app["image"]
    assert app["deploy"]["replicas"] == 3, "KY_REPLICAS not applied"
    assert "container_name" not in app, "a fixed container name allows one replica"
    assert not app.get("ports"), "an app replica publishes a host port"
    # Only the backup archive is shared (writes and pruning run under backup-run); each
    # replica's /app/data stays its own.
    mounts = [(v["type"], v["target"]) for v in app.get("volumes", [])]
    assert mounts == [("bind", "/app/backups")], f"app mounts {mounts}, want only the shared backups bind"
    assert app["volumes"][0]["source"] == str(root / "backups"), app["volumes"][0]["source"]
    env = app["environment"]
    assert env["KY_DB_DRIVER"] == "postgres"
    # KyRecovery claim/deposit contract (limits.go): one path-safe service label.
    service = env["KY_APP_NAME"]
    assert service == "Busnes.app" and re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,63}", service) and ".." not in service, service
    assert env["KY_TRUSTED_PROXIES"] == required["KY_PROXY_IP"] + "/32", env["KY_TRUSTED_PROXIES"]
    assert env["KY_ENCRYPTION_KEY"] == required["KY_ENCRYPTION_KEY"]
    assert set(app["networks"]) == {"replicas", "database"}
    assert "container_name" not in db and not db.get("ports"), "PostgreSQL is reachable from the host"
    assert "profiles" not in db, "PostgreSQL stays behind a profile"
    assert db["environment"]["POSTGRES_PASSWORD"] == required["KY_POSTGRES_PASSWORD"]
    assert set(db["networks"]) == {"database"}
    assert db["healthcheck"]["test"] == ["CMD", "pg_isready", "-h", "127.0.0.1", "-U", "ky_server", "-d", "ky_server"], "PostgreSQL readiness must wait for its final TCP server"
    script = ("bind", str(kit), "/ky/postgres-17-to-18.sh")
    db_mounts = [(v["type"], v["source"], v["target"]) for v in db["volumes"]]
    # 18 refuses anything mounted at /var/lib/postgresql/data.
    assert db_mounts == [("volume", "postgres-replicas", "/var/lib/postgresql"), script], \
        f"PostgreSQL mounts {db_mounts}; the base volumes carry a public password"
    assert db["image"] == PG18, db["image"]
    assert db["entrypoint"] == ["bash", "/ky/postgres-17-to-18.sh", "restore"] and db["command"] == ["postgres"], "PostgreSQL does not restore a 17 dump first"
    assert "PGDATA" not in db["environment"], "PGDATA moves 18 off /var/lib/postgresql/18/docker"
    assert db["depends_on"]["postgres-17-dump"]["condition"] == "service_completed_successfully"
    assert db["healthcheck"]["start_period"] == "1h0m0s", "a long restore would mark PostgreSQL unhealthy"
    dump = services["postgres-17-dump"]
    assert dump["image"] == PG17 and dump["entrypoint"] == ["bash", "/ky/postgres-17-to-18.sh", "dump"], dump["image"]
    assert dump["network_mode"] == "none" and dump["restart"] == "no" and "profiles" not in dump
    assert dump["environment"] == {"POSTGRES_USER": "ky_server", "OLD": "/var/lib/postgresql/data", "NEW": "/pg18"}, dump["environment"]
    dump_mounts = [(v["type"], v["source"], v["target"]) + ((v.get("read_only", False),) if v["type"] == "bind" else ()) for v in dump["volumes"]]
    assert dump_mounts == [("volume", "pgdata-replicas", "/var/lib/postgresql/data"), ("volume", "postgres-replicas", "/pg18"), script + (True,)], \
        f"the dump one-shot mounts {dump_mounts}"
    assert proxy["environment"]["KY_REPLICAS"] == "3", "HAProxy has a slot count other than KY_REPLICAS"
    for name, svc in [("postgres", db), ("postgres-17-dump", dump), ("proxy", proxy)]:
        assert "@sha256:" in svc["image"], f"{name} image is not pinned by digest"
    assert [p["host_ip"] for p in proxy["ports"]] == ["127.0.0.1"], "proxy is publicly bound"
    assert proxy["networks"]["replicas"]["ipv4_address"] == required["KY_PROXY_IP"]
    assert json.loads(result.stdout)["networks"]["database"].get("internal") is True
    assert app["healthcheck"]["test"][-1].endswith("/readyz")
    print("PASS replicas:", " + ".join(chain_files))

edge = compose(files, base_env | required | {"KY_EDGE_TRUSTED_PROXIES": "172.31.250.1/32"})
assert edge.returncode == 0, edge.stderr
trusted = json.loads(edge.stdout)["services"]["app"]["environment"]["KY_TRUSTED_PROXIES"]
assert trusted == required["KY_PROXY_IP"] + "/32,172.31.250.1/32", trusted
print("PASS replicas: KY_EDGE_TRUSTED_PROXIES extends only the trusted list")

named = compose(files, base_env | required | {"KY_APP_NAME": "existing-service"})
assert named.returncode == 0, named.stderr
assert json.loads(named.stdout)["services"]["app"]["environment"]["KY_APP_NAME"] == "existing-service", "configured service name was overwritten"
print("PASS replicas: wire-valid default and retained configured service name")

for bind in ["0.0.0.0", "127.0.0.1"]:
    both = compose(files, base_env | required | {"KY_EDGE_TRUSTED_PROXIES": "172.31.250.1/32", "KY_PROXY_BIND": bind})
    assert both.returncode != 0 and "KY_EDGE_TRUSTED_PROXIES is safe only with the default loopback publish" in both.stderr, both.stderr
public = compose(files, base_env | required | {"KY_PROXY_BIND": "0.0.0.0"})
assert public.returncode == 0, public.stderr
assert json.loads(public.stdout)["services"]["app"]["environment"]["KY_TRUSTED_PROXIES"] == required["KY_PROXY_IP"] + "/32"
print("PASS replicas: an edge gateway is trusted only with the default loopback publish")

# The base file's postgres profile: the same upgrade on its own volumes.
services = json.loads(subprocess.run(
    ["docker", "compose", "--env-file", "/dev/null", "--profile", "postgres", "-f", "docker-compose.yml", "config", "--format", "json"],
    cwd=root, env=base_env, capture_output=True, text=True, check=True).stdout)["services"]
db, dump = services["postgres"], services["postgres-17-dump"]
assert db["image"] == PG18 and dump["image"] == PG17
assert [(v["source"], v["target"]) for v in db["volumes"]] == [("postgres", "/var/lib/postgresql"), (str(kit), "/ky/postgres-17-to-18.sh")]
assert [(v["source"], v["target"]) for v in dump["volumes"]] == [("pgdata", "/var/lib/postgresql/data"), ("postgres", "/pg18"), (str(kit), "/ky/postgres-17-to-18.sh")]
assert db["entrypoint"][-1] == "restore" and dump["entrypoint"][-1] == "dump" and dump["network_mode"] == "none"
assert db["depends_on"]["postgres-17-dump"]["condition"] == "service_completed_successfully"
assert dump["profiles"] == db["profiles"] == ["postgres"] and dump["environment"]["POSTGRES_USER"] == db["environment"]["POSTGRES_USER"]
print("PASS postgres profile: PostgreSQL 18 restores the one-shot's dump of pgdata")
