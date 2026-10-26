#!/usr/bin/env python3
"""Render real Compose merges using synthetic inputs; start no containers."""
import json
import os
import subprocess
from pathlib import Path

root = Path(__file__).resolve().parents[1]
env = os.environ | {
    "KY_APP_URL": "https://profile.example.invalid",
    "KY_SESSION_SECRET": "synthetic-profile-secret-for-config-checks",
    "KY_DNS": "192.0.2.53",
    "KY_CONTAINER_IP": "172.30.0.10",
    "KY_NETWORK_SUBNET": "172.30.0.0/24",
}


def render(files):
    command = ["docker", "compose", "--env-file", "/dev/null"]
    for file in files:
        command += ["-f", file]
    return json.loads(subprocess.check_output(
        command + ["config", "--format", "json"], cwd=root, env=env
    ))


for overlays in [[], ["build"], ["build", "lan-dns", "static-ip"]]:
    files = ["docker-compose.yml"] + [f"docker-compose.{name}.yml" for name in overlays]
    base = render(files)
    configured = render(files + ["docker-compose.strong-crypto.yml"])
    app = configured["services"]["app"]
    expected_env = base["services"]["app"]["environment"] | {
        "GODEBUG": "fips140=on", "KY_ENV": "production", "KY_COOKIE_SECURE": "true",
        "KY_APP_URL": env["KY_APP_URL"], "KY_SESSION_SECRET": env["KY_SESSION_SECRET"],
    }
    assert app["environment"] == expected_env, "unexpected environment change"
    assert len(app["ports"]) == 1, "retained an additional HTTP binding"
    assert app["ports"][0]["host_ip"] == "127.0.0.1", "HTTP is publicly bound"
    assert int(app["ports"][0]["published"]) == 8080
    assert app["ports"][0]["target"] == 8080
    # Every setting other than these profile choices must survive the merge.
    app["environment"] = base["services"]["app"]["environment"]
    app["ports"] = base["services"]["app"]["ports"]
    assert configured == base, "changed images/builds, volumes, DNS, networks or another service"
    print("PASS strong profile:", ", ".join(overlays) or "published image")
