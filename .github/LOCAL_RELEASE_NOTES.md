Self-hosted Multica release built by GitHub Actions.

- Server images: one Docker Hub repository, with `backend-<tag>` and `web-<tag>` tags.
- macOS: Apple Silicon and Intel DMG/ZIP installers, bundled local daemon, deployment URL included.
- Android: signed release APK using the persistent fork signing key.
- `checksums.txt` and `release-manifest.txt` identify the artifacts, source commit and CI run.

The macOS installers are for internal use and are not notarized. On first launch, allow the app in System Settings → Privacy & Security. Install updates manually from this release page; automatic updating of unsigned macOS builds is not supported.

The cloud hosts only the platform. Agent tasks execute on your local computer.
