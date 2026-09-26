#!/usr/bin/env python3
"""打包原生 Linux PAT 插件（native-go，无 runner、无 Node 附件）。"""
import argparse
import hashlib
import json
from pathlib import Path
import re
import zipfile


ROOT = Path(__file__).resolve().parents[1]


def version(provider: str) -> str:
    source = (ROOT / f"examples/plugin/{provider}/go/types.go").read_text()
    match = re.search(r'pluginVersion\s*=\s*"([0-9][0-9A-Za-z.+-]*)"', source)
    if not match:
        raise ValueError(f"Missing {provider} pluginVersion")
    return match[1]


def archive(destination: Path, files: list[tuple[Path, str]]) -> None:
    with zipfile.ZipFile(destination, "w", compression=zipfile.ZIP_DEFLATED) as output:
        for source, name in sorted(files, key=lambda item: item[1]):
            if source.is_symlink() or not source.is_file():
                raise ValueError(f"Not a regular input file: {source.name}")
            info = zipfile.ZipInfo(name, date_time=(1980, 1, 1, 0, 0, 0))
            info.compress_type = zipfile.ZIP_DEFLATED
            info.external_attr = (source.stat().st_mode & 0xFFFF) << 16
            output.writestr(info, source.read_bytes())


def package(libraries: Path, output: Path, arch: str, commit: str, core_version: str) -> dict:
    if arch not in ("amd64", "arm64"):
        raise ValueError("Only linux/amd64 and linux/arm64 are supported")
    if commit != "unknown" and not re.fullmatch(r"[0-9a-f]{40}", commit):
        raise ValueError("core-commit must be a full SHA or unknown")
    plugin_versions = {name: version(name) for name in ("codebuddy", "copilot", "qoder")}
    for name in plugin_versions:
        if not (libraries / f"cpa-provider-{name}.so").is_file():
            raise ValueError(f"Missing {name} dynamic library")
    output.mkdir(parents=True, exist_ok=True)
    assets = []
    for name, plugin_version in plugin_versions.items():
        library = f"cpa-provider-{name}.so"
        target = output / f"cpa-provider-{name}_{plugin_version}_linux_{arch}.zip"
        files = [(libraries / library, library)]
        if name == "qoder":
            files.append((ROOT / "examples/plugin/qoder/THIRD_PARTY_NOTICES.md", "THIRD_PARTY_NOTICES.md"))
        archive(target, files)
        assets.append(target)
    hashes = {path.name: hashlib.sha256(path.read_bytes()).hexdigest() for path in assets}
    manifest = {
        "core_commit": commit, "core_version": core_version,
        "platform": f"linux/{arch}", "plugins": plugin_versions,
        "runner": None, "node_major": None,
        "runtime": "native-go", "runner_required": False,
        "transport": "direct_openai", "assets": hashes,
        "compatibility": "CPA-Core-LTS execution lifecycle extensions required; not upstream-only CPA",
    }
    (output / "pat-provider-bundle.json").write_text(json.dumps(manifest, indent=2) + "\n")
    (output / "provider-checksums.txt").write_text("".join(f"{sha}  {name}\n" for name, sha in sorted(hashes.items())))
    return manifest


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--libraries-dir", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--arch", choices=("amd64", "arm64"), required=True)
    parser.add_argument("--core-commit", required=True)
    parser.add_argument("--core-version", default="dev")
    args = parser.parse_args()
    package(args.libraries_dir, args.output, args.arch, args.core_commit, args.core_version)


if __name__ == "__main__":
    main()
