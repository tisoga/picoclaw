import {
  IconAlertCircle,
  IconBrandGoogle,
  IconCheck,
  IconClock,
  IconCloudOff,
  IconLoader2,
  IconRefresh,
} from "@tabler/icons-react"
import { useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { useEffect, useMemo, useState } from "react"
import { useTranslation } from "react-i18next"

import { type AntigravityQuotaModel, getAntigravityQuota } from "@/api/oauth"
import { PageHeader } from "@/components/page-header"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { Switch } from "@/components/ui/switch"
import { cn } from "@/lib/utils"

const refreshIntervalMs = 60_000
const autoRefreshStorageKey = "picoclaw.quota.auto-refresh"

function clampPercentage(value: number) {
  if (!Number.isFinite(value)) return 0
  return Math.min(100, Math.max(0, Math.round(value * 100)))
}

function quotaTone(percent: number) {
  if (percent <= 20) {
    return {
      bar: "bg-red-500",
      text: "text-red-600 dark:text-red-400",
      track: "bg-red-500/10",
    }
  }
  if (percent <= 50) {
    return {
      bar: "bg-amber-500",
      text: "text-amber-700 dark:text-amber-400",
      track: "bg-amber-500/10",
    }
  }
  return {
    bar: "bg-emerald-500",
    text: "text-emerald-700 dark:text-emerald-400",
    track: "bg-emerald-500/10",
  }
}

function formatResetCountdown(resetTime: string, nowMs: number) {
  if (!resetTime) return null
  const resetMs = new Date(resetTime).getTime()
  if (!Number.isFinite(resetMs)) return null

  const diffSeconds = Math.max(0, Math.ceil((resetMs - nowMs) / 1000))
  if (diffSeconds === 0) return "Reset due"
  const days = Math.floor(diffSeconds / 86_400)
  const hours = Math.floor((diffSeconds % 86_400) / 3_600)
  const minutes = Math.floor((diffSeconds % 3_600) / 60)
  if (days > 0) return `Resets in ${days}d ${hours}h`
  if (hours > 0) return `Resets in ${hours}h ${minutes}m`
  return `Resets in ${Math.max(1, minutes)}m`
}

function formatResetDate(resetTime: string) {
  if (!resetTime) return null
  const date = new Date(resetTime)
  if (!Number.isFinite(date.getTime())) return null
  return date.toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
  })
}

function QuotaRow({
  model,
  nowMs,
}: {
  model: AntigravityQuotaModel
  nowMs: number
}) {
  const percent = clampPercentage(model.remaining_fraction)
  const tone = quotaTone(percent)
  const countdown = formatResetCountdown(model.reset_time, nowMs)
  const resetDate = formatResetDate(model.reset_time)

  return (
    <div className="space-y-2.5 py-4 first:pt-0 last:pb-0">
      <div className="flex min-w-0 items-start justify-between gap-4">
        <div className="min-w-0">
          <div className="truncate text-sm font-medium">
            {model.display_name}
          </div>
          <div className="text-muted-foreground mt-0.5 truncate font-mono text-[11px]">
            {model.id}
          </div>
        </div>
        <div
          className={cn(
            "shrink-0 text-sm font-semibold tabular-nums",
            tone.text,
          )}
        >
          {model.is_exhausted ? "Exhausted" : `${percent}%`}
        </div>
      </div>

      <div className={cn("h-2 overflow-hidden rounded-full", tone.track)}>
        <div
          className={cn(
            "h-full rounded-full transition-[width] duration-500",
            tone.bar,
          )}
          style={{ width: `${percent}%` }}
        />
      </div>

      <div className="text-muted-foreground flex flex-wrap items-center justify-between gap-x-3 gap-y-1 text-xs">
        <span>{percent}% remaining</span>
        {countdown && (
          <span className="inline-flex items-center gap-1">
            <IconClock className="size-3.5" />
            {countdown}
            {resetDate ? ` · ${resetDate}` : ""}
          </span>
        )}
      </div>
    </div>
  )
}

function LoadingQuota() {
  return (
    <div className="space-y-5">
      {[0, 1, 2, 3].map((item) => (
        <div key={item} className="space-y-2.5">
          <div className="flex justify-between">
            <Skeleton className="h-4 w-48 max-w-[65%]" />
            <Skeleton className="h-4 w-10" />
          </div>
          <Skeleton className="h-2 w-full rounded-full" />
          <Skeleton className="h-3 w-32" />
        </div>
      ))}
    </div>
  )
}

export function QuotaPage() {
  const { t } = useTranslation()
  const [autoRefresh, setAutoRefresh] = useState(() => {
    if (typeof window === "undefined") return true
    return window.localStorage.getItem(autoRefreshStorageKey) !== "false"
  })
  const [nowMs, setNowMs] = useState(() => Date.now())

  const query = useQuery({
    queryKey: ["oauth-quota", "google-antigravity"],
    queryFn: getAntigravityQuota,
    refetchInterval: autoRefresh ? refreshIntervalMs : false,
    refetchIntervalInBackground: false,
    refetchOnWindowFocus: true,
    retry: 1,
  })

  useEffect(() => {
    window.localStorage.setItem(autoRefreshStorageKey, String(autoRefresh))
  }, [autoRefresh])

  useEffect(() => {
    const timer = window.setInterval(() => setNowMs(Date.now()), 1_000)
    return () => window.clearInterval(timer)
  }, [])

  const models = useMemo(
    () =>
      [...(query.data?.models ?? [])].sort(
        (a, b) => b.remaining_fraction - a.remaining_fraction,
      ),
    [query.data?.models],
  )
  const availableCount = models.filter((model) => !model.is_exhausted).length
  const averageRemaining = models.length
    ? Math.round(
        models.reduce(
          (total, model) => total + clampPercentage(model.remaining_fraction),
          0,
        ) / models.length,
      )
    : 0
  const nextReset = models
    .map((model) => new Date(model.reset_time).getTime())
    .filter((value) => Number.isFinite(value) && value > nowMs)
    .sort((a, b) => a - b)[0]
  const secondsUntilRefresh = query.dataUpdatedAt
    ? Math.max(
        0,
        Math.ceil((query.dataUpdatedAt + refreshIntervalMs - nowMs) / 1_000),
      )
    : 60
  const errorMessage =
    query.error instanceof Error ? query.error.message.trim() : ""
  const disconnected = errorMessage.toLowerCase().includes("not connected")

  return (
    <div className="flex h-full flex-col">
      <PageHeader title={t("navigation.quota", "Quotas & Usage")}>
        <div className="hidden items-center gap-2 sm:flex">
          <Switch
            id="quota-auto-refresh"
            size="sm"
            checked={autoRefresh}
            onCheckedChange={setAutoRefresh}
          />
          <label
            htmlFor="quota-auto-refresh"
            className="text-muted-foreground text-xs whitespace-nowrap"
          >
            Auto {autoRefresh && `· ${secondsUntilRefresh}s`}
          </label>
        </div>
        <Button
          variant="outline"
          size="icon-sm"
          title="Refresh quota"
          aria-label="Refresh quota"
          disabled={query.isFetching}
          onClick={() => void query.refetch()}
        >
          {query.isFetching ? (
            <IconLoader2 className="animate-spin" />
          ) : (
            <IconRefresh />
          )}
        </Button>
      </PageHeader>

      <div className="min-h-0 flex-1 overflow-y-auto px-4 pb-6 sm:px-6">
        <div className="mx-auto max-w-6xl space-y-4 pt-3">
          <div className="flex items-center justify-between gap-3 sm:hidden">
            <div className="flex items-center gap-2">
              <Switch
                id="quota-auto-refresh-mobile"
                size="sm"
                checked={autoRefresh}
                onCheckedChange={setAutoRefresh}
              />
              <label
                htmlFor="quota-auto-refresh-mobile"
                className="text-muted-foreground text-xs"
              >
                Auto refresh {autoRefresh && `in ${secondsUntilRefresh}s`}
              </label>
            </div>
          </div>

          {query.isError && !query.data ? (
            <section className="bg-card rounded-lg border px-5 py-12 text-center">
              {disconnected ? (
                <IconCloudOff className="text-muted-foreground/40 mx-auto size-12" />
              ) : (
                <IconAlertCircle className="text-destructive/70 mx-auto size-12" />
              )}
              <h3 className="mt-4 text-base font-semibold">
                {disconnected
                  ? "Antigravity is not connected"
                  : "Quota unavailable"}
              </h3>
              <p className="text-muted-foreground mx-auto mt-2 max-w-md text-sm">
                {disconnected
                  ? "Connect your Google Antigravity account before checking model quotas."
                  : errorMessage ||
                    "The upstream quota service did not respond."}
              </p>
              <div className="mt-5 flex justify-center gap-2">
                {disconnected && (
                  <Button asChild>
                    <Link to="/credentials">Open credentials</Link>
                  </Button>
                )}
                {!disconnected && (
                  <Button
                    variant="outline"
                    onClick={() => void query.refetch()}
                  >
                    <IconRefresh />
                    Retry
                  </Button>
                )}
              </div>
            </section>
          ) : (
            <section className="bg-card overflow-hidden rounded-lg border">
              <div className="flex flex-col gap-4 border-b px-4 py-4 sm:flex-row sm:items-center sm:justify-between sm:px-5">
                <div className="flex min-w-0 items-center gap-3">
                  <div className="flex size-10 shrink-0 items-center justify-center rounded-lg bg-blue-500/10 text-blue-600 dark:text-blue-400">
                    <IconBrandGoogle className="size-5" />
                  </div>
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-2">
                      <h3 className="font-semibold">
                        {query.data?.display_name ?? "Google Antigravity"}
                      </h3>
                      {query.data?.plan && (
                        <Badge variant="secondary">{query.data.plan}</Badge>
                      )}
                    </div>
                    <p className="text-muted-foreground mt-0.5 truncate text-xs">
                      {query.data?.email ||
                        query.data?.project_id ||
                        "Loading account..."}
                    </p>
                  </div>
                </div>
                {query.data?.updated_at && (
                  <div className="text-muted-foreground text-xs sm:text-right">
                    Updated{" "}
                    {new Date(query.data.updated_at).toLocaleTimeString([], {
                      hour: "2-digit",
                      minute: "2-digit",
                    })}
                  </div>
                )}
              </div>

              <div className="grid grid-cols-3 border-b">
                <div className="px-4 py-3 sm:px-5">
                  <div className="text-muted-foreground text-[11px] font-medium uppercase">
                    Available
                  </div>
                  <div className="mt-1 text-lg font-semibold tabular-nums">
                    {query.isLoading
                      ? "-"
                      : `${availableCount}/${models.length}`}
                  </div>
                </div>
                <div className="border-x px-4 py-3 sm:px-5">
                  <div className="text-muted-foreground text-[11px] font-medium uppercase">
                    Average
                  </div>
                  <div className="mt-1 text-lg font-semibold tabular-nums">
                    {query.isLoading ? "-" : `${averageRemaining}%`}
                  </div>
                </div>
                <div className="px-4 py-3 sm:px-5">
                  <div className="text-muted-foreground text-[11px] font-medium uppercase">
                    Next reset
                  </div>
                  <div className="mt-1 truncate text-sm font-semibold tabular-nums sm:text-lg">
                    {nextReset
                      ? formatResetCountdown(
                          new Date(nextReset).toISOString(),
                          nowMs,
                        )?.replace("Resets in ", "")
                      : "-"}
                  </div>
                </div>
              </div>

              <div className="px-4 py-5 sm:px-5">
                {query.isLoading ? (
                  <LoadingQuota />
                ) : models.length === 0 ? (
                  <div className="text-muted-foreground py-8 text-center text-sm">
                    Google returned no quota buckets for the supported
                    Antigravity models.
                  </div>
                ) : (
                  <div className="grid gap-x-8 divide-y lg:grid-cols-2 lg:divide-y-0">
                    {models.map((model) => (
                      <QuotaRow key={model.id} model={model} nowMs={nowMs} />
                    ))}
                  </div>
                )}
              </div>

              {models.length > 0 && (
                <div className="text-muted-foreground flex items-center gap-2 border-t px-4 py-3 text-xs sm:px-5">
                  <IconCheck className="size-3.5 text-emerald-600 dark:text-emerald-400" />
                  Quota values are reported directly by Google Cloud Code
                  Assist.
                </div>
              )}
            </section>
          )}
        </div>
      </div>
    </div>
  )
}
