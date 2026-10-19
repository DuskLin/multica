// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";
import { detectOS } from "./os-detect";

afterEach(() => vi.unstubAllGlobals());

describe("Android download detection", () => {
  it("recognizes Android before the Linux user agent fallback", async () => {
    vi.stubGlobal("navigator", {
      platform: "Linux armv8l",
      userAgent:
        "Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 Chrome/130.0 Mobile Safari/537.36",
    });
    expect(await detectOS()).toMatchObject({ os: "android", arch: "arm64" });
  });

  it("recognizes the Chromium Android platform", async () => {
    vi.stubGlobal("navigator", {
      userAgentData: {
        getHighEntropyValues: async () => ({
          platform: "Android",
          architecture: "arm",
        }),
      },
    });
    expect(await detectOS()).toEqual({
      os: "android",
      arch: "arm64",
      archConfident: true,
    });
  });
});
