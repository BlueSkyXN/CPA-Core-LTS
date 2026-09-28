#!/usr/bin/env python3
"""Docker 完整版 fixture smoke：四插件加载、手动账号、就绪诊断和重建持久化；禁止出站。"""
import argparse
import ipaddress
import json
from pathlib import Path
import secrets
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request


def docker(*args: str) -> str:
    return subprocess.check_output(["docker", *args], text=True, stderr=subprocess.PIPE).strip()


def write_coding_plan_fixture(root: Path) -> Path:
    private = root / "coding-plan"
    private.mkdir(mode=0o700)
    (private / "api-key.txt").write_text("synthetic-key.synthetic-secret")
    (private / "device-id.txt").write_text("synthetic-device")
    (private / "config.json").write_text(json.dumps({
        "credential": {"api_key_file": "api-key.txt"},
        "identity": {"device_id_file": "device-id.txt", "platform": "linux-x64", "os_category": "linux",
                     "os_version": "synthetic", "language": "en", "timezone": "UTC"},
        "models": ["GLM-5.3-Flash"], "host_logging_disabled": True, "prompt": {"mode": "preserve"},
    }))
    for path in private.iterdir():
        path.chmod(0o600)
    return private


def check_coding_plan_status(status: dict, selected: bool) -> None:
    assert status.get("Ready") is selected, "readiness must reflect selected local auth"
    checks = {check["Level"]: check["State"] for check in status.get("Checks", [])}
    assert checks.get("auth_ready") == ("ready" if selected else "unknown")
    text = json.dumps(status)
    assert not any(value in text for value in ("synthetic-key", "synthetic-secret", "synthetic-device", "/run/cpa-coding-plan")), "diagnostic leaked private fixture data"


def fixture_management_url(info: dict, network: str) -> str:
    if not info["State"]["Running"]:
        raise RuntimeError("Fixture container stopped before management became available")
    # internal 网络不保证生成发布端口；CI 在 Linux 宿主直接访问隔离网桥地址。
    address = ipaddress.IPv4Address(info["NetworkSettings"]["Networks"][network]["IPAddress"])
    return f"http://{address}:8317/v0/management"


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", required=True)
    args = parser.parse_args()
    name = "cpa-pat-smoke-" + secrets.token_hex(5)
    key = secrets.token_hex(24)
    with tempfile.TemporaryDirectory(prefix="cpa-pat-smoke-") as directory:
        root = Path(directory)
        (root / "auths").mkdir()
        private = write_coding_plan_fixture(root)
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
commercial-mode: true
proxy-url: http://127.0.0.1:9
request-retry: 0
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
    cpa-provider-copilot:
      enabled: true
      permissions: {{auth-read: true}}
    cpa-provider-qoder:
      enabled: true
      permissions: {{auth-read: true}}
    zcode-coding-plan:
      enabled: true
      config_file: /run/cpa-coding-plan/config.json
''')
        config.chmod(0o600)

        def start() -> str:
            docker("run", "-d", "--name", name, "--network", name,
                   "-v", f"{config}:/CLIProxyAPI/config.yaml",
                   "-v", f"{private}:/run/cpa-coding-plan:ro",
                   "-v", f"{root / 'auths'}:/root/.cli-proxy-api", args.image,
                   "./sky-cpa-core-lts", "--config", "/CLIProxyAPI/config.yaml", "--local-model", "--no-browser")
            return fixture_management_url(json.loads(docker("inspect", name))[0], name)

        def docker_or_dump_logs(*args: str) -> str:
            try:
                return docker(*args)
            except subprocess.CalledProcessError:
                logs = subprocess.run(["docker", "logs", "--tail", "40", name], capture_output=True, text=True)
                print((logs.stdout + logs.stderr).replace(key, "[fixture-key]"))
                raise

        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

        def request(base, path, method="GET", payload=None):
            data = json.dumps(payload).encode() if payload is not None else None
            req = urllib.request.Request(base + path, data=data, method=method,
                headers={"Authorization": f"Bearer {key}", "Content-Type": "application/json"})
            with opener.open(req, timeout=5) as response:
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
                    if {"cpa-provider-codebuddy", "cpa-provider-copilot", "cpa-provider-qoder", "zcode-coding-plan"} <= active:
                        return
                except (OSError, urllib.error.URLError, KeyError) as error:
                    last = type(error).__name__ + ":" + str(getattr(error, "code", ""))
                time.sleep(0.5)
            logs = subprocess.run(["docker", "logs", "--tail", "30", name], capture_output=True, text=True)
            print((logs.stdout + logs.stderr).replace(key, "[fixture-key]"))
            raise RuntimeError("PAT plugins did not register: " + last)

        try:
            docker("network", "create", "--internal", name)
            assert json.loads(docker("network", "inspect", name))[0]["Internal"] is True
            base = start()
            docker_or_dump_logs("exec", name, "sh", "-ec",
                                "test -x /CLIProxyAPI/sky-cpa-core-lts; ! test -e /CLIProxyAPI/CLIProxyAPI")
            wait_plugins(base)
            startup_logs = docker("logs", name)
            if "sky-cpa-core-lts Version:" not in startup_logs:
                raise RuntimeError("PAT image did not report the sky-cpa-core-lts product name")
            if "CLIProxyAPI Version:" in startup_logs:
                raise RuntimeError("PAT image still reports the upstream CLIProxyAPI product name")
            docker("exec", name, "sh", "-ec",
                   "! command -v node; ! command -v qodercli; ! command -v qoderclicn; test ! -d /opt/cpa-qoder-runner")
            example = json.loads(docker("exec", name, "cat", "/opt/cpa-plugin-examples/zcode-coding-plan/config.example.json"))
            assert example["host_logging_disabled"] is False
            assert example["credential"] == {"api_key_env": "CP_API_KEY"}
            docker_or_dump_logs("exec", name, "sh", "-ec",
                                "test -f /opt/cpa-pat-plugins/zcode-coding-plan.so; test -s /opt/cpa-plugin-examples/zcode-coding-plan/LICENSE")
            bundle = json.loads(docker("exec", name, "cat", "/opt/cpa-pat-plugins/bundle.json"))
            assert bundle["runtime"] == "native-go" and bundle["runner_required"] is False
            assert bundle["runner"] is None and bundle["node_major"] is None
            qoder_version = tuple(int(part) for part in bundle["plugins"]["qoder"].split(".")[:2])
            assert qoder_version >= (0, 3), "qoder plugin must be native PAT-only 0.3.0+"
            assert set(bundle["plugins"]) == {"codebuddy", "copilot", "qoder", "zcode-coding-plan"}
            assert bundle["transport"] == "mixed"
            assert bundle["plugin_transports"]["zcode-coding-plan"] == "direct_anthropic"
            providers = {p["id"]: p for p in request(base, "/plugins")["plugins"]}
            qoder_meta = providers["cpa-provider-qoder"].get("metadata") or {}
            assert str(qoder_meta.get("version", "")) == bundle["plugins"]["qoder"], "qoder metadata version must match bundle"
            coding_plan = providers["zcode-coding-plan"]
            assert coding_plan["metadata"]["version"] == bundle["plugins"]["zcode-coding-plan"]
            assert coding_plan["supports_auth"] and coding_plan["supports_readiness"]
            assert not coding_plan["supports_oauth"]
            check_coding_plan_status(request(base, "/plugins/zcode-coding-plan/readiness"), selected=False)
            for provider in ("codebuddy", "copilot", "qoder", "zcode-coding-plan"):
                if provider == "zcode-coding-plan":
                    payload = {"type": provider, "label": "Synthetic local account", "request_retry": 0}
                elif provider == "copilot":
                    payload = {"type": provider, "auth_mode": "github_token",
                               "github_token": "gho-fixture-not-a-real-credential", "label": "Fixture"}
                else:
                    payload = {"type": provider, "auth_mode": "pat", "pat": "pt-fixture-not-a-real-credential", "label": "Fixture"}
                request(base, f"/auth-files?name={provider}-fixture.json", "POST", payload)
            def check_files(base):
                files = request(base, "/auth-files")["files"]
                for provider in ("codebuddy", "copilot", "qoder", "zcode-coding-plan"):
                    assert any(f["name"] == f"{provider}-fixture.json" and f.get("auth_index") for f in files)
                account = next(f for f in files if f["name"] == "zcode-coding-plan-fixture.json")
                query = urllib.parse.urlencode({"auth_index": account["auth_index"]})
                check_coding_plan_status(request(base, "/plugins/zcode-coding-plan/readiness?" + query), selected=True)
                stored = request(base, "/auth-files/download?name=zcode-coding-plan-fixture.json")
                assert stored["type"] == "zcode-coding-plan" and stored["request_retry"] == 0
                assert "synthetic-key" not in json.dumps(stored) and "synthetic-device" not in json.dumps(stored)
            check_files(base)
            docker("rm", "-f", name)
            base = start()
            wait_plugins(base)
            check_files(base)
            print("PASS: four native plugins, auth registration, Coding Plan readiness and recreation persistence (egress blocked, no inference)")
        except Exception:
            for command in (["docker", "inspect", "--format", "{{json .State}}", name], ["docker", "logs", "--tail", "50", name]):
                result = subprocess.run(command, capture_output=True, text=True)
                print((result.stdout + result.stderr).replace(key, "[fixture-key]"))
            raise
        finally:
            subprocess.run(["docker", "rm", "-f", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
            subprocess.run(["docker", "network", "rm", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)


if __name__ == "__main__":
    main()
