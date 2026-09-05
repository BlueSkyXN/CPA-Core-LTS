#!/usr/bin/env python3
"""Publish prevalidated LTS assets without ever deleting an existing asset.

Same-name identical assets are reusable. Different bytes require a new tag, not
--clobber. New releases stay draft until the complete asset set is verified.
"""
from __future__ import annotations
import argparse
import hashlib
import json
import re
import subprocess
import tempfile
from pathlib import Path
from urllib.parse import quote


class PublicationError(RuntimeError):
    pass


class GitHub:
    def __init__(self, repo: str):
        if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repo):
            raise PublicationError("Expected repository in owner/name form")
        self.repo = repo

    def run(self, *args: str, missing_ok: bool = False):
        result = subprocess.run(["gh", *args], text=True, capture_output=True, check=False)
        if result.returncode:
            # An outage or an authentication failure must never become 'not found'.
            if missing_ok and re.search(r"\(HTTP 404\)", result.stderr):
                return None
            raise PublicationError(f"GitHub command failed ({result.returncode}): {result.stderr.strip()}")
        return result.stdout

    def release(self, tag: str):
        raw = self.run("api", f"repos/{self.repo}/releases/tags/{quote(tag, safe='')}", missing_ok=True)
        return None if raw is None else json.loads(raw)

    def assets(self, release_id: int):
        pages = json.loads(self.run("api", "--paginate", "--slurp", f"repos/{self.repo}/releases/{release_id}/assets?per_page=100"))
        return [asset for page in pages for asset in page]

    def digest(self, tag: str, asset: dict):
        digest = asset.get("digest") or ""
        if re.fullmatch(r"sha256:[0-9a-fA-F]{64}", digest):
            return digest[7:].lower()
        # Older releases have no server digest. Download to an isolated temporary
        # directory; do not trust size alone and do not overwrite local artifacts.
        with tempfile.TemporaryDirectory(prefix="lts-asset-") as temp:
            self.run("release", "download", tag, "--repo", self.repo,
                     "--pattern", asset["name"], "--dir", temp)
            return sha256(Path(temp) / asset["name"])

    def create(self, tag: str, title: str, notes: Path):
        self.run("release", "create", tag, "--repo", self.repo, "--draft",
                 "--verify-tag", "--title", title, "--notes-file", str(notes))

    def upload(self, tag: str, path: Path):
        # Intentionally no --clobber, including retry/concurrent-publisher paths.
        self.run("release", "upload", tag, str(path), "--repo", self.repo)

    def edit_notes(self, tag: str, title: str, notes: Path):
        self.run("release", "edit", tag, "--repo", self.repo,
                 "--title", title, "--notes-file", str(notes))

    def publish(self, tag: str):
        self.run("release", "edit", tag, "--repo", self.repo, "--draft=false")


def sha256(path: Path) -> str:
    with path.open("rb") as source:
        return hashlib.file_digest(source, "sha256").hexdigest()


def asset_map(assets: list[dict]) -> dict[str, dict]:
    result = {}
    for asset in assets:
        name = asset["name"]
        if name in result:
            raise PublicationError(f"Duplicate remote asset: {name}")
        result[name] = asset
    return result


def publish(api: GitHub, tag: str, files: dict[str, Path], title: str,
            notes: Path, rewrite_notes: bool = False) -> None:
    if not re.fullmatch(r"v[^/\s]+-lts-[^/\s]+", tag):
        raise PublicationError("Expected v*-lts-* tag")
    if not files:
        raise PublicationError("Empty asset set")
    for name, path in files.items():
        if not re.fullmatch(r"[A-Za-z0-9_.-]+", name) or path.name != name or not path.is_file():
            raise PublicationError(f"Invalid or missing local asset: {name}")
    local = {name: sha256(path) for name, path in files.items()}
    release = api.release(tag)
    initial = asset_map(api.assets(release["id"])) if release else {}
    if set(initial) - set(files):
        raise PublicationError("Unexpected remote assets: " + ", ".join(sorted(set(initial) - set(files))))
    # Finish every collision check BEFORE the first mutation. A conflicting
    # rebuild must leave title, notes, and all previous assets untouched.
    for name, asset in initial.items():
        if api.digest(tag, asset) != local[name]:
            raise PublicationError(f"Existing asset differs: {name}. Publish changed bytes under a new LTS tag.")
    if release is None or rewrite_notes:
        if not title.strip() or not notes.is_file() or not notes.read_text(encoding="utf-8").strip():
            raise PublicationError("Missing release title/notes")
    was_draft = release is None or release["draft"]
    if release is None:
        api.create(tag, title.strip(), notes)
    for name in sorted(set(files) - set(initial)):
        api.upload(tag, files[name])
    final_release = api.release(tag)
    if final_release is None:
        raise PublicationError("Release disappeared during publication")
    final = asset_map(api.assets(final_release["id"]))
    if set(final) != set(files):
        raise PublicationError("Remote asset set differs from the validated local set")
    for name, asset in final.items():
        if api.digest(tag, asset) != local[name]:
            raise PublicationError(f"Uploaded asset digest differs: {name}")
    if release is not None and rewrite_notes:
        api.edit_notes(tag, title.strip(), notes)
    if was_draft:
        api.publish(tag)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", required=True)
    parser.add_argument("--tag", required=True)
    parser.add_argument("--assets", type=Path, required=True, help="Text file of validated basenames")
    parser.add_argument("--title-file", type=Path, required=True)
    parser.add_argument("--notes", type=Path, required=True)
    parser.add_argument("--rewrite-notes", choices=("true", "false"), default="false")
    args = parser.parse_args()
    names = [line.strip() for line in args.assets.read_text().splitlines() if line.strip()]
    if len(names) != len(set(names)):
        raise PublicationError("Duplicate local asset name")
    title = args.title_file.read_text().strip() if args.title_file.is_file() else ""
    files = {name: args.assets.resolve().parent / name for name in names}
    publish(GitHub(args.repo), args.tag, files, title, args.notes.resolve(), args.rewrite_notes == "true")
    print(f"Published {len(files)} assets for {args.tag}; no existing assets were deleted.")


if __name__ == "__main__":
    try:
        main()
    except (PublicationError, OSError, ValueError, KeyError) as error:
        raise SystemExit(str(error)) from error
