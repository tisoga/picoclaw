import { IconDatabase, IconLoader2, IconRefresh } from "@tabler/icons-react"
import { useQuery } from "@tanstack/react-query"
import { useTranslation } from "react-i18next"

import { launcherFetch } from "@/api/http"
import { PageHeader } from "@/components/page-header"

interface AntigravityModelInfo {
  id: string
  display_name: string
  is_exhausted: boolean
  remaining_fraction: number
  reset_time: string
}

interface QuotaResponse {
  models: AntigravityModelInfo[]
}

export function QuotaPage() {
  const { t } = useTranslation()

  const { data, isLoading, error, refetch } = useQuery<QuotaResponse>({
    queryKey: ["oauth-quota", "google-antigravity"],
    queryFn: async () => {
      const res = await launcherFetch(
        "/api/oauth/quota?provider=google-antigravity",
      )
      if (!res.ok) {
        throw new Error(await res.text())
      }
      return res.json()
    },
    refetchInterval: 60000,
  })

  return (
    <div className="flex h-full flex-col">
      <PageHeader title={t("navigation.quota", "Quotas & Usage")}>
        <div className="flex items-center gap-4">
          <button
            onClick={() => refetch()}
            className="text-muted-foreground hover:text-foreground inline-flex items-center gap-2 text-sm"
          >
            <IconRefresh className="size-4" />
            Refresh
          </button>
        </div>
      </PageHeader>

      <div className="min-h-0 flex-1 overflow-y-auto px-4 sm:px-6">
        <div className="pt-2 pb-6">
          <p className="text-muted-foreground text-sm">
            Monitor your available upstream API quotas for connected providers.
          </p>
        </div>

        {error && (
          <div className="text-destructive bg-destructive/10 mb-6 rounded-lg px-4 py-3 text-sm">
            {error instanceof Error ? error.message : String(error)}
          </div>
        )}

        <div className="space-y-6">
          <div className="bg-card rounded-lg border">
            <div className="border-b px-4 py-3">
              <h3 className="flex items-center gap-2 font-semibold">
                <IconDatabase className="size-4 text-blue-500" />
                Google Cloud Code Assist
              </h3>
            </div>
            <div className="p-4">
              {isLoading ? (
                <div className="text-muted-foreground flex items-center gap-2 text-sm">
                  <IconLoader2 className="size-4 animate-spin" />
                  Loading quotas...
                </div>
              ) : !data?.models || data.models.length === 0 ? (
                <div className="text-muted-foreground text-sm">
                  No quota information available. Are you logged in?
                </div>
              ) : (
                <div className="space-y-6">
                  {data.models.map((model) => {
                    const percent = Math.round(model.remaining_fraction * 100)
                    let colorClass = "bg-green-500"
                    if (percent < 30) colorClass = "bg-red-500"
                    else if (percent < 70) colorClass = "bg-yellow-500"

                    let resetText = ""
                    if (model.reset_time) {
                      try {
                        const d = new Date(model.reset_time)
                        resetText = `Resets at ${d.toLocaleTimeString()}`
                      } catch {
                        resetText = `Resets at ${model.reset_time}`
                      }
                    }

                    return (
                      <div key={model.id} className="space-y-2">
                        <div className="flex items-center justify-between text-sm">
                          <span className="font-medium">
                            {model.display_name}
                          </span>
                          <span className="text-muted-foreground">
                            {model.is_exhausted
                              ? "Exhausted"
                              : `${percent}% remaining`}
                          </span>
                        </div>
                        <div className="bg-muted h-2 w-full overflow-hidden rounded-full">
                          <div
                            className={`h-full ${colorClass} transition-all duration-500`}
                            style={{ width: `${percent}%` }}
                          />
                        </div>
                        {resetText && (
                          <div className="text-muted-foreground text-right text-xs">
                            {resetText}
                          </div>
                        )}
                      </div>
                    )
                  })}
                </div>
              )}
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}
