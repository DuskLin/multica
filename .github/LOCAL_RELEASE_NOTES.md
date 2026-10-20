Self-hosted Multica release built by GitHub Actions.

## Cumulative local changes through v0.6.5-local.3

### Features

- Initialize isolated local project folders with Bash scripts, including non-Git folders and projects containing multiple repositories. Setup logs and run output are preserved.
- Link PRs from multiple repositories found in Issue descriptions and completed runs through GitHub CLI, then apply the workspace's status setting after all linked PRs merge. This is disabled by default and requires an explicitly configured workspace, a loopback backend URL, and no cloud connection. Manual unlinking and reopening are respected; CI and mergeability details still require the GitHub App.
- Build server images, macOS installers, and signed Android APKs together in CI, with the deployment URL bundled into clients.

### Improvements

- Reuse the script folder for follow-up runs and retries in the same Issue or conversation, including after daemon restarts. Runs sharing a folder execute in sequence; separate Issues or conversations can run in parallel.
- Rename the desktop app to Multica CD while keeping the existing Multica user data directory.
- Select a complete local release on the download page according to deployment configuration, with matching versions and platform installers.

### Fixes

- Fix Android startup crashes caused by mismatched native dependencies and check signed APK startup on an emulator in CI.
- Retry child-process cleanup when cancelling runs to reduce exit timeouts.
- Preserve cancellation status when an interrupted result races with a cancellation signal, without discarding completed results.
- Include same-size content edits in local Git snapshots.
- Fix client CI packaging configuration and missing license files, and stabilize release-check search-menu tests.

## Installation

- Server images: one Docker Hub repository, with `backend-<tag>` and `web-<tag>` tags.
- macOS: Apple Silicon and Intel DMG/ZIP installers containing `Multica CD.app`, bundled local daemon, deployment URL included. Existing user data stays in the `Multica` profile directory.
- Android: signed release APK using the persistent fork signing key.
- `checksums.txt` and `release-manifest.txt` identify the artifacts, source commit and CI run.

The macOS installers are for internal use and are not notarized. On first launch, allow the app in System Settings → Privacy & Security. Install updates manually from this release page; automatic updating of unsigned macOS builds is not supported.

The cloud hosts only the platform. Agent tasks execute on your local computer.
