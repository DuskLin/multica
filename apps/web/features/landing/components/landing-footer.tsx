"use client";

import { MulticaIcon } from "@multica/ui/components/common/multica-icon";
import { cn } from "@multica/ui/lib/utils";
import { useLocale, locales, localeLabels } from "../i18n";

export function LandingFooter() {
  const { t, locale, setLocale } = useLocale();

  return (
    <footer className="bg-[#0a0d12] text-white">
      <div className="mx-auto max-w-[1320px] px-4 sm:px-6 lg:px-8">
        {/* Bottom: copyright + language switcher */}
        <div className="flex items-center justify-between py-6">
          <p className="text-label text-white/36">
            {t.footer.copyright.replace(
              "{year}",
              String(new Date().getFullYear()),
            )}
          </p>
          <div className="flex items-center">
            {locales.map((l, i) => (
              <button
                type="button"
                key={l}
                onClick={() => setLocale(l)}
                className={cn(
                  "px-1.5 py-1 text-caption font-medium transition-colors",
                  l === locale
                    ? "text-white/70"
                    : "text-white/30 hover:text-white/50",
                  i > 0 && "border-l border-white/16",
                )}
              >
                {localeLabels[l]}
              </button>
            ))}
          </div>
        </div>

        {/* Giant logo */}
        <div className="relative overflow-hidden pb-4">
          <div className="flex items-end gap-6 sm:gap-8">
            <MulticaIcon
              className="size-[clamp(4rem,12vw,10rem)] shrink-0 text-white"
              noSpin
            />
            <span className="landing-serif text-[clamp(6rem,22vw,16rem)] font-normal leading-[0.82] tracking-[-0.04em] text-white lowercase">
              multica
            </span>
          </div>
        </div>
      </div>
    </footer>
  );
}
