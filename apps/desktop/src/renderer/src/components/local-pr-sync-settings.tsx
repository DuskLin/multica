import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import type { LocalPRSyncCommand } from "@multica/core/api/local-pr-sync";
import { SettingsCard, SettingsRow } from "@multica/views/settings";
import { useT } from "@multica/views/i18n";
import { Switch } from "@multica/ui/components/ui/switch";
import { Button } from "@multica/ui/components/ui/button";

const REFRESH_INTERVAL_MS = 5_000;

/** Keep responses from an earlier workspace out of the currently visible settings. */
function prSyncKey(serverUrl: string, workspaceId: string): string[] {
  return ["desktop", "local-pr-sync", serverUrl, workspaceId];
}

/** Control only this desktop connection's current workspace, awaiting persisted changes. */
export function LocalPRSyncSettings() {
  const { t } = useT("settings");
  const workspaceId = useWorkspaceId();
  const runtime = window.desktopAPI.runtimeConfig;
  const serverUrl = runtime.ok ? runtime.config.apiUrl : "";
  const client = useQueryClient();
  const queryKey = prSyncKey(serverUrl, workspaceId);
  const settings = useQuery({
    queryKey,
    queryFn: () => window.daemonAPI.prSync({ action: "get", serverUrl, workspaceId }),
    enabled: Boolean(serverUrl && workspaceId),
    refetchInterval: REFRESH_INTERVAL_MS,
    retry: false,
  });
  const update = useMutation({
    mutationFn: (command: LocalPRSyncCommand) => window.daemonAPI.prSync(command),
    onSuccess: (data, command) => client.setQueryData(prSyncKey(command.serverUrl, command.workspaceId), data),
    onSettled: (_data, _error, command) => client.invalidateQueries({ queryKey: prSyncKey(command.serverUrl, command.workspaceId) }),
  });
  const data = settings.data;
  const blocked = data?.blocked;
  const busy = update.isPending;
  const failure = update.error || settings.error;
  return (
    <SettingsCard>
      <SettingsRow
        label={t(($) => $.desktop.pr_sync.title)}
        description={t(($) => $.desktop.pr_sync.description)}
      >
        <Switch
          aria-label={t(($) => $.desktop.pr_sync.title)}
          checked={data?.enabled === true}
          disabled={!data || Boolean(blocked) || busy || settings.isError}
          onCheckedChange={(enabled) => update.mutate({ action: "set", enabled, serverUrl, workspaceId })}
        />
      </SettingsRow>
      <div className="space-y-2 px-4 pb-4 text-caption text-muted-foreground" aria-live="polite">
        {blocked && <p>{t(($) => $.desktop.pr_sync.blocked[blocked])}</p>}
        {!data && !failure && <p>{t(($) => $.desktop.pr_sync.loading)}</p>}
        {data && !blocked && !data.pendingRestart && <p>{t(($) => $.desktop.pr_sync.phase[data.phase])}</p>}
        {data?.pendingRestart && (
          <div className="flex flex-wrap items-center gap-2">
            <p>{t(($) => $.desktop.pr_sync.pending)}</p>
            <Button
              size="sm"
              variant="outline"
              disabled={busy || !data.canRestart}
              onClick={() => update.mutate({ action: "restart", serverUrl, workspaceId })}
            >{t(($) => $.desktop.pr_sync.restart)}</Button>
            {!blocked && !data.canRestart && <p>{t(($) => $.desktop.pr_sync.busy)}</p>}
          </div>
        )}
        {data?.phase === "error" && (
          <Button size="sm" variant="outline" onClick={() => { void window.daemonAPI.openLogFile(); }}>
            {t(($) => $.desktop.daemon.view_logs)}
          </Button>
        )}
        {failure && <p role="alert" className="text-destructive">{t(($) => $.desktop.pr_sync.failed)} {failure.message}</p>}
      </div>
    </SettingsCard>
  );
}
