"use client";

import { Info } from "lucide-react";
import { useT } from "../../i18n";

type ModelDiscoveryNoticeProps = {
  runtimeOnline: boolean;
  error: Error | null;
};

/**
 * Shows why discovery is unavailable while keeping manual model entry usable.
 * @param props Runtime availability and the latest discovery error.
 * @returns An actionable notice, or null when discovery is available.
 */
export function ModelDiscoveryNotice({ runtimeOnline, error }: ModelDiscoveryNoticeProps) {
  const { t } = useT("agents");
  if (runtimeOnline && error === null) return null;
  const detail = runtimeOnline && error !== null ? error.message.trim() : null;

  return (
    <div role="status" className="flex items-start gap-2 px-3 py-4 text-body text-muted-foreground">
      <Info className="mt-0.5 h-4 w-4 shrink-0" aria-hidden="true" />
      <div className="min-w-0">
        <div className="font-medium text-foreground">
          {runtimeOnline
            ? t(($) => $.pickers.model_discovery_failed_title)
            : t(($) => $.model_dropdown.runtime_offline_manual)}
        </div>
        {detail && (
          <div className="mt-1 whitespace-pre-wrap break-words text-caption select-text">{detail}</div>
        )}
        {runtimeOnline && (
          <div className="mt-1.5 text-caption">{t(($) => $.pickers.model_discovery_failed_hint)}</div>
        )}
      </div>
    </div>
  );
}
