// @vitest-environment node
import { describe, expect, it } from "vitest";
import { createLandingDict } from "./dictionary";

describe("createLandingDict", () => {
  it.each([
    ["en", "https://multica.ai/docs/zh"],
    ["zh-Hans", "https://multica.ai/docs/zh"],
    ["ko", "https://multica.ai/docs/zh"],
    ["ja", "https://multica.ai/docs/zh"],
    ["fr", "https://multica.ai/docs/zh"],
  ] as const)("links the %s footer to %s", (locale, docsHref) => {
    const links = createLandingDict(locale, true).footer.groups.resources.links;
    expect(links[0]?.href).toBe(docsHref);
  });
});
