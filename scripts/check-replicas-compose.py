#!/usr/bin/env python3
"""Render docker-compose.replicas.yml merges with synthetic inputs; start no containers."""
import json
import os
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
    assert not app.get("volumes"), "replicas share a bind-mounted directory"
    env = app["environment"]
    assert env["KY_DB_DRIVER"] == "postgres"
    assert env["KY_TRUSTED_PROXIES"] == required["KY_PROXY_IP"] + "/32", env["KY_TRUSTED_PROXIES"]
    assert env["KY_ENCRYPTION_KEY"] == required["KY_ENCRYPTION_KEY"]
    assert set(app["networks"]) == {"replicas", "database"}
    assert "container_name" not in db and not db.get("ports"), "PostgreSQL is reachable from the host"
    assert "profiles" not in db, "PostgreSQL stays behind a profile"
    assert db["environment"]["POSTGRES_PASSWORD"] == required["KY_POSTGRES_PASSWORD"]
    assert set(db["networks"]) == {"database"}
    for name, svc in [("postgres", db), ("proxy", proxy)]:
        assert "@sha256:" in svc["image"], f"{name} image is not pinned by digest"
    assert [p["host_ip"] for p in proxy["ports"]] == ["127.0.0.1"], "proxy is publicly bound"
    assert proxy["networks"]["replicas"]["ipv4_address"] == required["KY_PROXY_IP"]
    assert json.loads(result.stdout)["networks"]["database"].get("internal") is True
    assert app["healthcheck"]["test"][-1].endswith("/readyz")
    print("PASS replicas:", " + ".join(chain_files))
