Self-hosted Multica release built by GitHub Actions.

## v0.6.8-local.2 — 2026-10-22

Changes since `v0.6.7-local.1` ([#14](https://github.com/DuskLin/multica/pull/14)).

### Features

- Opt in per workspace to let the local daemon use GitHub CLI to link PRs from completed runs to issues on a remote server, without uploading GitHub credentials.
- Sync PR merge status and apply the workspace status setting after all linked PRs merge, while preserving manual exclusions, reopened issues, and newer webhook data.

### Improvements

- PR sync supports pagination, shared lookup caching, retries, and continued scanning of large task histories. The server validates runtime ownership and the complete delivery snapshot before accepting reports.

### Fixes

- Build server and desktop binaries with Go 1.26.10 to include the certificate verification security fix [GO-2026-6809](https://pkg.go.dev/vuln/GO-2026-6809).

- Ordinary `.DS_Store` files and the managed `codex-home/.models_cache_config.sha256` file no longer block safe work directory cleanup. User files, symbolic links, and work that cannot be recovered from a remote remain protected.

### Enabling remote PR sync

Update both the server and local daemon. The feature is disabled by default: set `pr_sync_workspace_id` in the daemon profile and optionally `pr_sync_gh_path`, then restart the daemon after active runs finish. The local GitHub CLI must be logged in and able to read the repositories, and automatic PR linking must be enabled in the workspace. Sync runs every 30 seconds. CI and mergeability details still require the GitHub App. See the [GitHub integration guide](https://github.com/DuskLin/multica/blob/v0.6.8-local.2/apps/docs/content/docs/github-integration.zh.mdx).

## v0.6.7-local.1 — 2026-10-20

Changes since `v0.6.6-local.1`.

### Fixes

- Fixed missing Android icons in tabs, navigation, status controls, and workspace switching.
- Profile photo actions and message/comment long-press menus now work on Android, including cancellation with the back button or backdrop.
- Android due-date selection now closes its native dialog after selection or cancellation and can be reopened.
- Closing the native search bar clears the search query.
- API requests, uploads, and WebSocket connections now report the actual mobile operating system.
- Enabled the camera permission needed to take a profile photo.

## v0.6.6-local.1 — 2026-10-20

Changes since `v0.6.5-local.3`.

### Features

- Desktop Settings now lists managed work directories across local profiles, with disk usage, task links, and asynchronous cleanup progress.
- Completed script work directories can be reclaimed when every repository is recoverable from its remote. A later run initializes the directory again and restores the recorded branches.
- Quick create shows live script output and initialization progress while preparing a project workspace.

### Improvements

- Cleanup protects active directories and work that cannot be recovered from a remote; offline profiles remain available for inspection.
- Simplified the landing page and sidebar, removed the GitHub star counter and community prompts, and updated documentation and feedback links.
- The changelog now includes cumulative local features and fixes alongside upstream releases.

### Fixes

- Centered desktop and PWA icons and regenerated platform icon assets.
- Task links and status queries in directory management use the verified workspace, including when switching workspaces.

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
