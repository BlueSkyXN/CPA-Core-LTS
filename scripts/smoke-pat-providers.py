#!/usr/bin/env python3
"""本地 Docker fixture smoke：插件加载、PAT 文件登记、容器重建持久化；不调用供应商。"""
import argparse
import json
from pathlib import Path
import secrets
import subprocess
import tempfile
import time
import urllib.error
import urllib.request


def docker(*args: str) -> str:
    return subprocess.check_output(["docker", *args], text=True, stderr=subprocess.PIPE).strip()


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", required=True)
    args = parser.parse_args()
    name = "cpa-pat-smoke-" + secrets.token_hex(5)
    key = secrets.token_hex(24)
    with tempfile.TemporaryDirectory(prefix="cpa-pat-smoke-") as directory:
        root = Path(directory)
        (root / "auths").mkdir()
        config = root / "config.yaml"
        config.write_text(f'''host: ""
port: 8317
auth-dir: /root/.cli-proxy-api
remote-management:
  allow-remote: true
  secret-key: "{key}"
  disable-control-panel: true
api-keys: ["{key}"]
usage-statistics-enabled: true
plugins:
  enabled: true
  dir: /opt/cpa-pat-plugins
  configs:
    cpa-provider-codebuddy:
      enabled: true
      permissions: {{auth-read: true}}
      endpoint: http://127.0.0.1:9/v2/chat/completions
      catalog_endpoint: http://127.0.0.1:9/v3/config
      billing_endpoint: http://127.0.0.1:9/v2/billing/meter/get-user-resource
    cpa-provider-qoder:
      enabled: true
      permissions: {{auth-read: true}}
''')
        config.chmod(0o600)

        def start() -> str:
            docker("run", "-d", "--name", name, "-p", "127.0.0.1::8317",
                   "-v", f"{config}:/CLIProxyAPI/config.yaml",
                   "-v", f"{root / 'auths'}:/root/.cli-proxy-api", args.image)
            port = docker("port", name, "8317/tcp").split(":")[-1]
            return f"http://127.0.0.1:{port}/v0/management"

        def request(base, path, method="GET", payload=None):
            data = json.dumps(payload).encode() if payload is not None else None
            req = urllib.request.Request(base + path, data=data, method=method,
                headers={"Authorization": f"Bearer {key}", "Content-Type": "application/json"})
            with urllib.request.urlopen(req, timeout=5) as response:
                return json.load(response)

        def wait_plugins(base):
            last = "no response"
            for _ in range(40):
                try:
                    response = request(base, "/plugins")
                    last = json.dumps({"plugins_enabled": response.get("plugins_enabled"), "plugins": [
                        {field: item.get(field) for field in ("id", "registered", "effective_enabled")}
                        for item in response.get("plugins", [])
                    ]})
                    active = {p["id"] for p in response["plugins"] if p.get("registered") and p.get("effective_enabled")}
                    if {"cpa-provider-codebuddy", "cpa-provider-qoder"} <= active:
                        return
                except (OSError, urllib.error.URLError, KeyError) as error:
                    last = type(error).__name__ + ":" + str(getattr(error, "code", ""))
                time.sleep(0.5)
            logs = subprocess.run(["docker", "logs", "--tail", "30", name], capture_output=True, text=True)
            print((logs.stdout + logs.stderr).replace(key, "[fixture-key]"))
            raise RuntimeError("PAT plugins did not register: " + last)

        try:
            base = start()
            docker("exec", name, "sh", "-ec",
                   "test -x /CLIProxyAPI/sky-cpa-core-lts; ! test -e /CLIProxyAPI/CLIProxyAPI")
            wait_plugins(base)
            startup_logs = docker("logs", name)
            if "sky-cpa-core-lts Version:" not in startup_logs:
                raise RuntimeError("PAT image did not report the sky-cpa-core-lts product name")
            if "CLIProxyAPI Version:" in startup_logs:
                raise RuntimeError("PAT image still reports the upstream CLIProxyAPI product name")
            docker("exec", name, "sh", "-ec",
                   "! command -v node; ! command -v qodercli; ! command -v qoderclicn; test ! -d /opt/cpa-qoder-runner")
            bundle = json.loads(docker("exec", name, "cat", "/opt/cpa-pat-plugins/bundle.json"))
            assert bundle["runtime"] == "native-go" and bundle["runner_required"] is False
            assert bundle["runner"] is None and bundle["node_major"] is None
            qoder_version = tuple(int(part) for part in bundle["plugins"]["qoder"].split(".")[:2])
            assert qoder_version >= (0, 3), "qoder plugin must be native PAT-only 0.3.0+"
            providers = {p["id"]: p for p in request(base, "/plugins")["plugins"]}
            qoder_meta = providers["cpa-provider-qoder"].get("metadata") or {}
            assert str(qoder_meta.get("version", "")) == bundle["plugins"]["qoder"], "qoder metadata version must match bundle"
            for provider in ("codebuddy", "qoder"):
                request(base, f"/auth-files?name={provider}-fixture.json", "POST",
                        {"type": provider, "auth_mode": "pat", "pat": "pt-fixture-not-a-real-credential", "label": "Fixture"})
            def check_files(base):
                files = request(base, "/auth-files")["files"]
                for provider in ("codebuddy", "qoder"):
                    assert any(f["name"] == f"{provider}-fixture.json" and f.get("auth_index") for f in files)
            check_files(base)
            docker("rm", "-f", name)
            base = start()
            wait_plugins(base)
            check_files(base)
            print("PASS: native plugin loading, PAT registration and container recreation persistence (no provider inference)")
        finally:
            subprocess.run(["docker", "rm", "-f", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)


if __name__ == "__main__":
    main()
