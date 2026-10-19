#!/usr/bin/env python3
"""Verify the packaged fork identity before uploading installers."""
import json
import os
from pathlib import Path
import plistlib
import subprocess

root = Path("apps/desktop/dist")
apps = list(root.glob("mac*/Multica.app"))
assert len(apps) == 1, f"Expected one packaged app, found {apps}"
contents = apps[0] / "Contents"
for notice in ("LICENSE", "NOTICE"):
    assert (contents / "Resources" / notice).read_bytes() == Path(notice).read_bytes()
with (contents / "Info.plist").open("rb") as source:
    info = plistlib.load(source)
assert info["CFBundleShortVersionString"] == os.environ["VERSION"]
defaults = json.loads((contents / "Resources/app.asar.unpacked/resources/desktop-defaults.json").read_text())
assert defaults["apiUrl"] == os.environ["SELFHOST_URL"]
assert defaults["appUrl"] == os.environ["SELFHOST_URL"]
assert defaults["wsUrl"] == os.environ["SELFHOST_URL"].replace("https:", "wss:") + "/ws"
feed = (contents / "Resources/app-update.yml").read_text()
owner, repo = os.environ["GITHUB_REPOSITORY"].split("/")
assert f"owner: {owner}" in feed and f"repo: {repo}" in feed
binary = contents / "MacOS/Multica"
arch = "arm64" if os.environ["ARCH"] == "arm64" else "x86_64"
assert subprocess.check_output(["lipo", "-archs", str(binary)], text=True).strip() == arch
cli = contents / "Resources/app.asar.unpacked/resources/bin/multica"
assert subprocess.check_output(["lipo", "-archs", str(cli)], text=True).strip() == arch
metadata = subprocess.check_output(["go", "version", "-m", str(cli)], text=True)
assert os.environ["VERSION"] in metadata
for extension in ("dmg", "zip"):
    artifact = root / f"multica-desktop-{os.environ['VERSION']}-mac-{os.environ['ARCH']}.{extension}"
    assert artifact.stat().st_size > 1_000_000
print("Verified packaged version, architecture, deployment defaults, update repository and installers")
