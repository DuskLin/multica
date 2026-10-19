import { z } from "zod";
import { parseWithFallback } from "@multica/core/api/schema";
import {
  hasCompleteAssetSet,
  parseReleaseAssets,
  type DownloadAssets,
} from "./parse-release-assets";

/**
 * Server-side fetcher for the latest downloadable Multica release,
 * designed to run inside a Next.js server component. Response is cached
 * by the Next.js fetch cache for 5 minutes (Vercel ISR) so hitting
 * /download costs at most one GitHub API call per region per 5 minutes.
 *
 * Desktop assets don't all land at the same time: CI uploads Linux and
 * Windows within a minute of each other, but macOS is packaged manually
 * (notarization credentials aren't wired into CI yet) and lands tens of
 * minutes later. A packaging job can also fail outright and leave a
 * release permanently short of some platforms. Either way the newest
 * release is not always the newest *downloadable* one, so we pull a
 * short window of recent releases and show the newest whose desktop
 * asset set is complete — every button on the page then resolves to a
 * real file.
 *
 * On any failure (network, rate limit, malformed payload) returns a
 * `null`-shaped result and logs — the page degrades to a "version
 * unavailable" view rather than 500ing.
 */

export interface LatestRelease {
  version: string | null;
  publishedAt: string | null;
  htmlUrl: string | null;
  assets: DownloadAssets;
}

// Resolve at request time so one image can serve different deployments.
export function getDownloadSource() {
  const repository =
    process.env.MULTICA_RELEASE_REPOSITORY || "multica-ai/multica";
  if (!/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(repository)) {
    throw new Error("Invalid MULTICA_RELEASE_REPOSITORY");
  }
  return {
    repository,
    releasesUrl: `https://github.com/${repository}/releases`,
    local: process.env.MULTICA_RELEASE_CHANNEL === "local",
  };
}

const REVALIDATE_SECONDS = 300;

const releaseSchema = z.object({
  tag_name: z.string(),
  published_at: z.string().nullable().optional(),
  html_url: z.url().optional(),
  prerelease: z.boolean().optional().default(false),
  draft: z.boolean().optional().default(false),
  assets: z
    .array(
      z.object({
        name: z.string(),
        browser_download_url: z
          .url()
          .refine((url) => url.startsWith("https://")),
      }),
    )
    .optional()
    .default([]),
});
type GitHubReleasePayload = z.infer<typeof releaseSchema>;

export async function fetchLatestRelease(): Promise<LatestRelease> {
  const headers: Record<string, string> = {
    Accept: "application/vnd.github+json",
    "X-GitHub-Api-Version": "2022-11-28",
  };
  // Optional PAT for local development and self-hosted deploys where
  // the shared outbound IP keeps hitting the 60-requests/hour
  // unauthenticated limit. Vercel's fetch cache is shared across all
  // regions so production rarely needs this — but the env var lets
  // anyone running the site locally avoid the rate-limit dance. Never
  // prefix this with `NEXT_PUBLIC_`; the token must stay server-side.
  const token = process.env.GITHUB_TOKEN;
  if (token) {
    headers.Authorization = `Bearer ${token}`;
  }

  try {
    const source = getDownloadSource();
    const endpoint = `https://api.github.com/repos/${source.repository}/releases?per_page=20`;
    const res = await fetch(endpoint, {
      next: { revalidate: REVALIDATE_SECONDS },
      headers,
    });
    if (!res.ok) {
      throw new Error(`GitHub API responded ${res.status}`);
    }
    const data = parseWithFallback<GitHubReleasePayload[]>(
      await res.json(),
      z.array(releaseSchema),
      [],
      { endpoint },
    );

    // Local deployments only offer completed fork prereleases. Upstream
    // deployments retain their stable release policy.
    const candidates = data.filter(
      (r) =>
        !r.draft &&
        (source.local
          ? /^v\d+\.\d+\.\d+-local\.\d+$/.test(r.tag_name)
          : !r.prerelease),
    );
    const chosen = source.local
      ? candidates
          .map((release) => ({
            release,
            assets: parseReleaseAssets(
              release.assets.filter((asset) =>
                asset.browser_download_url.startsWith(
                  `https://github.com/${source.repository}/releases/download/${release.tag_name}/`,
                ),
              ),
            ),
          }))
          .find(
            ({ assets }) =>
              assets.androidApk &&
              assets.macArm64Dmg &&
              assets.macArm64Zip &&
              assets.macX64Dmg &&
              assets.macX64Zip,
          )
      : pickRelease(candidates);
    if (!chosen) {
      return emptyRelease();
    }

    return {
      version: chosen.release.tag_name ?? null,
      publishedAt: chosen.release.published_at ?? null,
      htmlUrl: source.local
        ? `${source.releasesUrl}/tag/${chosen.release.tag_name}`
        : (chosen.release.html_url ?? null),
      assets: chosen.assets,
    };
  } catch (err) {
    console.warn("[download] fetchLatestRelease failed:", err);
    return emptyRelease();
  }
}

interface PickedRelease {
  release: GitHubReleasePayload;
  assets: DownloadAssets;
}

/**
 * Newest release whose desktop assets are all present. Falls back to the
 * newest release overall when no candidate is complete — the page then
 * shows what does exist plus its "all releases" escape hatch, which
 * still beats reporting no version at all.
 */
function pickRelease(
  candidates: GitHubReleasePayload[],
): PickedRelease | undefined {
  const parsed = candidates.map((release) => ({
    release,
    assets: parseReleaseAssets(release.assets ?? []),
  }));

  const complete = parsed.find((c) => hasCompleteAssetSet(c.assets));
  if (complete) {
    if (complete !== parsed[0]) {
      // Worth a line in the logs: a release that never completes its
      // asset set is invisible on /download until someone notices.
      console.warn(
        `[download] skipping ${parsed[0]?.release.tag_name ?? "latest release"}` +
          ` — incomplete desktop assets; showing ${complete.release.tag_name}`,
      );
    }
    return complete;
  }
  return parsed[0];
}

function emptyRelease(): LatestRelease {
  return {
    version: null,
    publishedAt: null,
    htmlUrl: null,
    assets: {},
  };
}
