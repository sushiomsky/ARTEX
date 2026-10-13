"use client";

import * as React from "react";

import {
  CheckCircle2Icon,
  DownloadIcon,
  ExternalLinkIcon,
  RefreshCwIcon,
  RotateCcwIcon,
  TriangleAlertIcon,
} from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Progress } from "@/components/ui/progress";
import { api, sseUrl } from "@/lib/api";
import type { UpdateCheck, UpdateProgress } from "@/lib/types";

/** Waits out the longest current launch. Each upgrade runs three process launches (stage → install → current),
 *  everytimeall is seclevel,three minutesenoughcoverslowdisk and  Docker containerrebuild. */
const RESTART_TIMEOUT_MS = 180_000;

function humanSize(n?: number): string {
  if (!n || n <= 0) return "";
  const units = ["B", "KB", "MB", "GB"];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

export function UpdateCard() {
  const [info, setInfo] = React.useState<UpdateCheck | null>(null);
  const [checking, setChecking] = React.useState(true);
  const [progress, setProgress] = React.useState<UpdateProgress | null>(null);
  // and progress is tracked separately: after staging the process is gone and SSE breaks, so switch to polling /api/health.
  const [restarting, setRestarting] = React.useState(false);
  const [busy, setBusy] = React.useState(false);

  // quiet samewhendecideneeddon'tbypassbackendcache:enterpagewhen Autocheckusecache(top barjust checked),
  // A manual 'check for updates' always hits the source; otherwise release versions wait for cache expiry to appear.
  const check = React.useCallback((quiet = false) => {
    setChecking(true);
    api
      .checkUpdate(!quiet)
      .then((r) => {
        setInfo(r);
        if (!quiet) {
          if (r.error) toast.error("Check-for-update failed: " + r.error);
          else if (r.has_update) toast.success(`FindingsCurrentthis ${r.latest}`);
          else if (r.comparable) toast.success("current  is mostCurrentthis");
        }
      })
      .catch((e) => {
        if (!quiet) toast.error("Check-for-update failed: " + (e as Error).message);
      })
      .finally(() => setChecking(false));
  }, []);

  React.useEffect(() => {
    check(true);
  }, [check]);

  // Poll /api/health until the version number changes.
  //
  // The signal must be "Version changed", not "Connected": mid-install the legacy build briefly comes back once
  // (that time it only ushers artex.new onto the switch, then exits) — connectivity alone would fake success.
  const waitForNewVersion = React.useCallback(async (fromVersion: string) => {
    setRestarting(true);
    const deadline = Date.now() + RESTART_TIMEOUT_MS;
    while (Date.now() < deadline) {
      await sleep(2000);
      try {
        const r = await fetch("/api/health", { cache: "no-store" });
        if (r.ok) {
          const j = (await r.json()) as { version?: string };
          if (j.version && j.version !== fromVersion) {
            toast.success(`Updated to ${j.version},exact in re-newloadpage`);
            await sleep(800);
            window.location.reload();
            return;
          }
        }
      } catch {
        // Connection failures inside the restart window are expected — keep polling.
      }
    }
    setRestarting(false);
    toast.error("Timed out waiting for the service to restart. Check the backend logs, or confirm artex was started via start.sh / start.bat.");
  }, []);

  // Subscribe to update progress. SSE must not go through Next /api rewrites (that layer may rush-cache; events won't arrive).
  const openStream = React.useCallback(
    (fromVersion: string) => {
      const es = new EventSource(sseUrl("/api/update/stream"));
      es.onmessage = (ev) => {
        let p: UpdateProgress;
        try {
          p = JSON.parse(ev.data) as UpdateProgress;
        } catch {
          return;
        }
        setProgress(p);
        if (p.phase === "failed") {
          es.close();
          setBusy(false);
          toast.error("Update failed: " + (p.error || p.message));
          return;
        }
        if (p.phase === "staged") {
          es.close();
          void waitForNewVersion(fromVersion);
        }
      };
      es.onerror = () => {
        // SSE inevitably disconnects when the process exits. If already waiting for restart, that's healthy —
        // hand over to /api/health polling.
        es.close();
      };
      return es;
    },
    [waitForNewVersion],
  );

  const doUpdate = () => {
    if (!info) return;
    const from = info.current;
    const ok = window.confirm(
      `Confirm update to ${info.latest}?\n\n` +
        "Updating restarts the program; running tasks will be interrupted.\n" +
        (info.mode === "docker"
          ? "\nNote: in-container updates only replace the switch binary itself, not playwright / nmap and other toolchains in the image;" +
            "If the new version needs new tools, use docker compose pull instead."
          : ""),
    );
    if (!ok) return;

    setBusy(true);
    setProgress({ phase: "downloading", percent: 0, message: "Preparing..." });
    const es = openStream(from);
    api.applyUpdate().catch((e) => {
      es.close();
      setBusy(false);
      setProgress(null);
      toast.error("Failed to start update: " + (e as Error).message);
    });
  };

  const doRollback = () => {
    if (!info) return;
    if (
      !window.confirm(
        "Roll back to the previous version?\n\nThe program will restart and running tasks will be interrupted.\nNote: the DB schema won't roll back; the legacy build may not read data written by current.",
      )
    )
      return;
    const from = info.current;
    setBusy(true);
    api
      .rollbackUpdate()
      .then(() => {
        toast.success("Rolled back to the previous version, restarting...");
        void waitForNewVersion(from);
      })
      .catch((e) => {
        setBusy(false);
        toast.error("backroll failed: " + (e as Error).message);
      });
  };

  const phase = progress?.phase;
  const showProgress = busy || restarting;
  // onlyDownloadstagetakegetreal percent(By  Content-Length calculate).validate/decompress/waitrestartall is 
  // When the stage is unknown for a while, fill the progress bar with a pulsing animation meaning "Busy, ETA unknown".
  const downloading = !restarting && phase === "downloading";
  const pct = downloading ? Math.max(progress?.percent ?? 0, 0) : 100;

  return (
    // The settings page is a multi-column waterfall; each card owns its row spacing and forbids cross-column breaks (see page.tsx release notes).
    <Card className="mb-4 break-inside-avoid md:mb-6">
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <DownloadIcon className="size-4" />
          versionthisandmorenew
        </CardTitle>
        <CardDescription>Check GitHub and install the current build. Updating restarts the program; running tasks will be interrupted.</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <span className="text-muted-foreground">current versionthis</span>
          <Badge variant="secondary" className="font-mono">
            {info?.current ?? "..."}
          </Badge>
          {info && (
            <>
              <Badge variant="outline" className="font-mono">
                {info.os}/{info.arch}
              </Badge>
              <Badge variant="outline">{info.mode === "docker" ? "Docker" : "Standalone"}</Badge>
            </>
          )}
          {info?.latest && (
            <>
              <span className="text-muted-foreground">mostCurrentthis</span>
              <Badge variant={info.has_update ? "default" : "secondary"} className="font-mono">
                {info.latest}
              </Badge>
            </>
          )}
          {info?.html_url && (
            <a
              href={info.html_url}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1 text-xs text-muted-foreground underline-offset-4 hover:underline"
            >
              morenewlogs <ExternalLinkIcon className="size-3" />
            </a>
          )}
        </div>

        {info?.boot_notice && (
          <p className="flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-500/10 p-2 text-xs text-amber-700 dark:text-amber-400">
            <TriangleAlertIcon className="mt-0.5 size-3.5 shrink-0" />
            {info.boot_notice}
          </p>
        )}

        {info?.error && (
          <p className="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 p-2 text-xs text-destructive">
            <TriangleAlertIcon className="mt-0.5 size-3.5 shrink-0" />
            Cannot reach GitHub: {info.error}
            {" "}can in aboveconfigglobalproxyafterRetry.
          </p>
        )}

        {info && !info.comparable && info.reason && <p className="text-xs text-muted-foreground">{info.reason}</p>}

        {info?.has_update && info.asset_available === false && (
          <p className="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 p-2 text-xs text-destructive">
            <TriangleAlertIcon className="mt-0.5 size-3.5 shrink-0" />
            {info.latest} has no {info.os}/{info.arch} release asset (missing {info.asset}); auto-update unavailable.
          </p>
        )}

        {info?.has_update && info.asset_available !== false && (
          <p className="text-xs text-muted-foreground">
            willDownload <span className="font-mono">{info.asset}</span>
            {info.size ? `(${humanSize(info.size)})` : ""},validates SHA256 and smoke-tests before replacing the switch; on failure it keeps the current version.
          </p>
        )}

        {info && !info.has_update && info.comparable && !info.error && (
          <p className="flex items-center gap-2 text-xs text-muted-foreground">
            <CheckCircle2Icon className="size-3.5 text-emerald-600" />
            current  is mostCurrentthis.
          </p>
        )}

        {info?.mode === "docker" && info.has_update && (
          <p className="text-xs text-muted-foreground">
            Under docker, updates only replace the switch binary, not playwright / nmap and other image toolchains, and
            <span className="font-mono"> docker compose up -d </span>
            rebuilding the container reverts to the image's version. To upgrade image and all, run
            <span className="font-mono"> docker compose pull artex &amp;&amp; docker compose up -d artex</span>.
          </p>
        )}

        {showProgress && (
          <div className="space-y-1.5">
            <Progress value={pct} className={downloading ? undefined : "animate-pulse"} />
            <p className="text-xs text-muted-foreground">
              {restarting ? "exact in restartandApplicationCurrentthis,pleasewait(pagewillAuto-refresh)..." : progress?.message}
            </p>
          </div>
        )}

        <div className="flex flex-wrap gap-2">
          <Button variant="outline" size="sm" onClick={() => check(false)} disabled={checking || busy || restarting}>
            <RefreshCwIcon className={checking ? "size-4 animate-spin" : "size-4"} />
            checkmorenew
          </Button>
          <Button
            size="sm"
            onClick={doUpdate}
            disabled={busy || restarting || !info?.has_update || info?.asset_available === false}
          >
            <DownloadIcon className="size-4" />
            {info?.has_update ? `Update to ${info.latest}` : "Update now"}
          </Button>
          {info?.has_backup && (
            <Button variant="ghost" size="sm" onClick={doRollback} disabled={busy || restarting}>
              <RotateCcwIcon className="size-4" />
              backrolltopreviousversionthis
            </Button>
          )}
        </div>

        <p className="text-xs text-muted-foreground">
          one-clickmorenewdependguardfootthisrestartprogram.pleasethrough <span className="font-mono">start.sh</span>(Windows for
          <span className="font-mono"> start.bat</span>) to launch ARTEX; when running the artex binary directly, nothing relaunches it after exit.
        </p>
      </CardContent>
    </Card>
  );
}
