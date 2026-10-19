import { describe, expect, it } from "vitest";
import { createEnDict } from "../../i18n/en";
import { resolveContent } from "./hero";

describe("resolveContent", () => {
  it("offers the fork APK to Android clients", () => {
    expect(
      resolveContent(
        { os: "android", arch: "arm64", archConfident: true },
        { androidApk: "https://downloads.test/android.apk" },
        false,
        createEnDict(true, "/docs").download.hero,
      ).primary,
    ).toEqual({
      href: "https://downloads.test/android.apk",
      label: "Download (.apk)",
      disabled: false,
    });
  });
  it("links confidently detected Intel Macs to the x64 installers", () => {
    const content = resolveContent(
      { os: "mac", arch: "x64", archConfident: true },
      {
        macArm64Dmg: "https://downloads.test/mac-arm64.dmg",
        macArm64Zip: "https://downloads.test/mac-arm64.zip",
        macX64Dmg: "https://downloads.test/mac-x64.dmg",
        macX64Zip: "https://downloads.test/mac-x64.zip",
      },
      false,
      createEnDict(true, "/docs").download.hero,
    );

    expect(content.primary).toEqual({
      href: "https://downloads.test/mac-x64.dmg",
      label: "Download (.dmg)",
      disabled: false,
    });
    expect(content.alt).toEqual({
      href: "https://downloads.test/mac-x64.zip",
      label: "or download .zip",
    });
  });
});
