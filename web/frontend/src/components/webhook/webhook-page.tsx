import {
  IconCheck,
  IconCopy,
  IconDeviceFloppy,
  IconKey,
  IconRefresh,
  IconRoute,
  IconSend,
  IconShieldLock,
  IconSparkles,
  IconWebhook,
} from "@tabler/icons-react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { useEffect, useMemo, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import {
  getAppConfig,
  getChannelsCatalog,
  patchAppConfig,
  testWebhook,
} from "@/api/channels"
import { ConfigChangeNotice } from "@/components/config-change-notice"
import { PageHeader } from "@/components/page-header"
import { Field, KeyInput, SwitchCardField } from "@/components/shared-form"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { useGateway } from "@/hooks/use-gateway"
import { showSaveSuccessOrRestartToast } from "@/lib/restart-required"
import { refreshGatewayState } from "@/store/gateway"

interface WebhookForm {
  enabled: boolean
  token: string
  path: string
  allowedChannels: string[]
  allowedIPsText: string
  allowAllIPs: boolean
  mediaMaxMB: number
}

const EMPTY_FORM: WebhookForm = {
  enabled: false,
  token: "",
  path: "/webhook/send",
  allowedChannels: [],
  allowedIPsText: "",
  allowAllIPs: false,
  mediaMaxMB: 10,
}

function asRecord(value: unknown): Record<string, unknown> {
  return value !== null && typeof value === "object"
    ? (value as Record<string, unknown>)
    : {}
}

function parseWebhookForm(config: Record<string, unknown>): WebhookForm {
  const gateway = asRecord(config.gateway)
  const webhook = asRecord(gateway.webhook)
  return {
    enabled: webhook.enabled === true,
    token: "",
    path:
      typeof webhook.path === "string" && webhook.path.trim()
        ? webhook.path
        : "/webhook/send",
    allowedChannels: Array.isArray(webhook.allowed_channels)
      ? webhook.allowed_channels.filter(
          (value): value is string => typeof value === "string",
        )
      : [],
    allowedIPsText: Array.isArray(webhook.allowed_ips)
      ? webhook.allowed_ips
          .filter((value): value is string => typeof value === "string")
          .join("\n")
      : "",
    allowAllIPs: webhook.allow_all_ips === true,
    mediaMaxMB:
      typeof webhook.media_max_mb === "number" && webhook.media_max_mb > 0
        ? webhook.media_max_mb
        : 10,
  }
}

function generateToken(): string {
  const bytes = new Uint8Array(32)
  crypto.getRandomValues(bytes)
  return btoa(String.fromCharCode(...bytes))
    .replaceAll("+", "-")
    .replaceAll("/", "_")
    .replaceAll("=", "")
}

export function WebhookPage() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [form, setForm] = useState<WebhookForm>(EMPTY_FORM)
  const [baseline, setBaseline] = useState<WebhookForm>(EMPTY_FORM)
  const [saving, setSaving] = useState(false)
  const [copied, setCopied] = useState(false)
  const [testChannel, setTestChannel] = useState("telegram")
  const [testChatID, setTestChatID] = useState("")
  const [testMessage, setTestMessage] = useState("Hello from PicoClaw webhook")
  const [testing, setTesting] = useState(false)
  const {
    state: gatewayState,
    loading: gatewayLoading,
    restartRequired,
    restart: restartGateway,
  } = useGateway()

  const configQuery = useQuery({
    queryKey: ["config"],
    queryFn: getAppConfig,
  })
  const channelsQuery = useQuery({
    queryKey: ["channels", "catalog"],
    queryFn: getChannelsCatalog,
  })

  useEffect(() => {
    if (!configQuery.data) return
    const parsed = parseWebhookForm(configQuery.data)
    setForm(parsed)
    setBaseline(parsed)
  }, [configQuery.data])

  const isDirty = JSON.stringify(form) !== JSON.stringify(baseline)
  const endpointPath = form.path.trim() || "/webhook/send"
  const endpoint = `http://<gateway-host>:<gateway-port>${endpointPath}`
  const curlExample = useMemo(
    () => `curl -X POST '${endpoint}' \\
  -H 'Authorization: Bearer <your-token>' \\
  -H 'Content-Type: application/json' \\
  -d '{
    "channel": "telegram",
    "chat_id": "12345678",
    "message": "**Deployment finished**\\nStatus: *successful*"
  }'`,
    [endpoint],
  )
  const imageURLExample = `{
  "channel": "whatsapp",
  "chat_id": "136953756426480@lid",
  "message": "**Deployment complete**",
  "media_url": "https://example.com/report.pdf"
}`
  const imageUploadExample = `curl -X POST '${endpoint}' \\
  -H 'Authorization: Bearer <your-token>' \\
  -F 'channel=whatsapp' \\
  -F 'chat_id=136953756426480@lid' \\
  -F 'message=**Deployment complete**' \\
  -F 'media=@report.pdf'`

  const setField = <K extends keyof WebhookForm>(
    key: K,
    value: WebhookForm[K],
  ) => setForm((current) => ({ ...current, [key]: value }))

  const toggleChannel = (channel: string) => {
    setForm((current) => ({
      ...current,
      allowedChannels: current.allowedChannels.includes(channel)
        ? current.allowedChannels.filter((value) => value !== channel)
        : [...current.allowedChannels, channel],
    }))
  }

  const handleSave = async () => {
    const normalizedPath = endpointPath.startsWith("/")
      ? endpointPath
      : `/${endpointPath}`
    setSaving(true)
    try {
      await patchAppConfig({
        gateway: {
          webhook: {
            enabled: form.enabled,
            ...(form.token.trim() ? { token: form.token.trim() } : {}),
            path: normalizedPath,
            allowed_channels: form.allowedChannels,
            allowed_ips: form.allowedIPsText
              .split(/\r?\n|,/)
              .map((value) => value.trim())
              .filter(Boolean),
            allow_all_ips: form.allowAllIPs,
            media_max_mb: form.mediaMaxMB,
          },
        },
      })
      const saved = { ...form, token: "", path: normalizedPath }
      setForm(saved)
      setBaseline(saved)
      await queryClient.invalidateQueries({ queryKey: ["config"] })
      const gateway = await refreshGatewayState({ force: true })
      showSaveSuccessOrRestartToast(
        t,
        "Webhook settings saved",
        "Webhook",
        gateway?.restartRequired === true,
      )
    } catch (error) {
      toast.error(
        error instanceof Error ? error.message : "Failed to save webhook",
      )
    } finally {
      setSaving(false)
    }
  }

  const handleTest = async () => {
    if (!testChannel.trim() || !testChatID.trim() || !testMessage.trim()) {
      toast.error("Channel, Chat ID, and message are required")
      return
    }
    setTesting(true)
    try {
      const result = await testWebhook({
        channel: testChannel.trim(),
        chat_id: testChatID.trim(),
        message: testMessage,
      })
      if (result.status_code >= 400) {
        toast.error(`Test failed: HTTP ${result.status_code}`, {
          description: result.response,
        })
      } else {
        toast.success("Message delivered", {
          description: `Sent through ${testChannel.trim()}.`,
        })
      }
    } catch (error) {
      toast.error("Webhook test failed", {
        description: error instanceof Error ? error.message : String(error),
      })
    } finally {
      setTesting(false)
    }
  }

  const copyExample = async () => {
    await navigator.clipboard.writeText(curlExample)
    setCopied(true)
    setTimeout(() => setCopied(false), 1600)
  }

  if (configQuery.isLoading) {
    return (
      <div className="flex h-full items-center justify-center">
        <p className="text-muted-foreground text-sm">{t("labels.loading")}</p>
      </div>
    )
  }

  return (
    <div className="flex h-full flex-col">
      <PageHeader title={t("navigation.webhook", "Webhook")}>
        <Button onClick={handleSave} disabled={!isDirty || saving}>
          <IconDeviceFloppy className="size-4" />
          {saving ? t("common.saving") : t("common.save")}
        </Button>
      </PageHeader>

      <div className="min-h-0 flex-1 overflow-y-auto p-3 lg:p-6">
        <div className="mx-auto grid w-full max-w-[1120px] gap-5">
          {configQuery.error && (
            <ConfigChangeNotice
              kind="restart"
              title="Could not load webhook settings"
              description="Refresh the page or check the launcher logs."
            />
          )}

          {restartRequired && (
            <div className="flex flex-col gap-3 rounded-lg border border-amber-200 bg-amber-50 p-3 text-amber-900 sm:flex-row sm:items-center sm:justify-between">
              <ConfigChangeNotice
                kind="restart"
                title="Restart the gateway to apply webhook changes"
                description="The settings are saved, but the running gateway is still using the previous webhook token and IP policy."
                className="flex-1 border-0 bg-transparent p-0"
              />
              <Button
                variant="outline"
                onClick={() => void restartGateway()}
                disabled={
                  gatewayLoading ||
                  gatewayState === "restarting" ||
                  gatewayState !== "running"
                }
                className="border-amber-300 bg-white hover:bg-amber-100"
              >
                <IconRefresh className="size-4" />
                {gatewayLoading || gatewayState === "restarting"
                  ? "Restarting…"
                  : "Restart gateway"}
              </Button>
            </div>
          )}

          <Card className="overflow-hidden">
            <div className="from-primary/10 via-primary/5 grid gap-6 bg-gradient-to-br to-transparent p-5 md:grid-cols-[1fr_auto] md:items-center md:p-7">
              <div className="flex gap-4">
                <div className="bg-primary/10 text-primary flex size-11 shrink-0 items-center justify-center rounded-xl">
                  <IconWebhook className="size-6" />
                </div>
                <div>
                  <div className="flex flex-wrap items-center gap-2">
                    <h2 className="text-lg font-semibold">Channel Webhook</h2>
                    <Badge variant={form.enabled ? "default" : "secondary"}>
                      {form.enabled ? "Enabled" : "Disabled"}
                    </Badge>
                  </div>
                  <p className="text-muted-foreground mt-1 max-w-2xl text-sm">
                    Give external apps one secure HTTP endpoint for delivering
                    notifications directly to Telegram, WhatsApp, Discord, and
                    your other enabled channels.
                  </p>
                </div>
              </div>
              <SwitchCardField
                label={form.enabled ? "Webhook is active" : "Enable webhook"}
                hint="Changes take effect after saving and restarting when prompted."
                checked={form.enabled}
                onCheckedChange={(checked) => setField("enabled", checked)}
                ariaLabel="Enable webhook"
              />
            </div>
          </Card>

          <div className="grid items-start gap-5 lg:grid-cols-[minmax(0,1.25fr)_minmax(320px,0.75fr)]">
            <div className="grid gap-5">
              <Card>
                <CardHeader>
                  <div className="flex items-center gap-2">
                    <IconShieldLock className="text-primary size-5" />
                    <CardTitle>Secure the endpoint</CardTitle>
                  </div>
                  <CardDescription>
                    Authorize requests with a Bearer token, a trusted source IP,
                    or both.
                  </CardDescription>
                </CardHeader>
                <CardContent className="grid gap-5">
                  <Field
                    label="Access token"
                    hint="Leave blank to keep the saved token. Generate a new one to rotate access."
                  >
                    <div className="flex gap-2">
                      <div className="min-w-0 flex-1">
                        <KeyInput
                          value={form.token}
                          onChange={(value) => setField("token", value)}
                          placeholder="Saved token remains unchanged"
                        />
                      </div>
                      <Button
                        variant="outline"
                        onClick={() => setField("token", generateToken())}
                      >
                        <IconSparkles className="size-4" />
                        Generate
                      </Button>
                    </div>
                  </Field>

                  <Field
                    label="Trusted IP addresses"
                    hint="One IP address or CIDR range per line. Example: 192.168.1.198 or 192.168.1.0/24."
                  >
                    <Textarea
                      value={form.allowedIPsText}
                      onChange={(event) =>
                        setField("allowedIPsText", event.target.value)
                      }
                      rows={4}
                      className="font-mono text-sm"
                      placeholder={"192.168.1.198\n192.168.1.0/24"}
                      disabled={form.allowAllIPs}
                    />
                  </Field>

                  <SwitchCardField
                    label="Allow all IP addresses"
                    hint="Accept a valid token from any IP address. The Bearer token is still required."
                    checked={form.allowAllIPs}
                    onCheckedChange={(checked) =>
                      setField("allowAllIPs", checked)
                    }
                    ariaLabel="Allow all webhook IP addresses"
                  />

                  {form.allowAllIPs && (
                    <ConfigChangeNotice
                      kind="restart"
                      title="IP restriction is disabled"
                      description="Every request must still provide the valid Bearer token, regardless of its source IP."
                    />
                  )}

                  <Field
                    label="Endpoint path"
                    hint="Use a dedicated path beginning with a slash."
                  >
                    <div className="relative">
                      <IconRoute className="text-muted-foreground absolute top-2.5 left-3 size-4" />
                      <Input
                        value={form.path}
                        onChange={(event) =>
                          setField("path", event.target.value)
                        }
                        className="pl-9 font-mono"
                        placeholder="/webhook/send"
                      />
                    </div>
                  </Field>

                  <Field
                    label="Maximum attachment size"
                    hint="Applies to images and documents from uploads or media_url."
                  >
                    <div className="relative">
                      <Input
                        type="number"
                        min={1}
                        max={100}
                        value={form.mediaMaxMB}
                        onChange={(event) =>
                          setField(
                            "mediaMaxMB",
                            Math.max(1, Number(event.target.value) || 1),
                          )
                        }
                        className="pr-12"
                      />
                      <span className="text-muted-foreground pointer-events-none absolute top-2.5 right-3 text-xs">
                        MB
                      </span>
                    </div>
                  </Field>
                </CardContent>
              </Card>

              <Card>
                <CardHeader>
                  <CardTitle>Allowed channels</CardTitle>
                  <CardDescription>
                    Restrict where webhook requests may send messages. With
                    nothing selected, all enabled channels are allowed.
                  </CardDescription>
                </CardHeader>
                <CardContent>
                  <div className="flex flex-wrap gap-2">
                    {(channelsQuery.data?.channels ?? []).map((channel) => {
                      const selected = form.allowedChannels.includes(
                        channel.name,
                      )
                      return (
                        <Button
                          key={channel.name}
                          type="button"
                          size="sm"
                          variant={selected ? "default" : "outline"}
                          onClick={() => toggleChannel(channel.name)}
                          className="gap-1.5"
                        >
                          {selected && <IconCheck className="size-3.5" />}
                          {channel.display_name || channel.name}
                        </Button>
                      )
                    })}
                    {!channelsQuery.isLoading &&
                      (channelsQuery.data?.channels.length ?? 0) === 0 && (
                        <p className="text-muted-foreground text-sm">
                          No channels are available.
                        </p>
                      )}
                  </div>
                  <p className="text-muted-foreground mt-3 text-xs">
                    {form.allowedChannels.length === 0
                      ? "Current access: every enabled channel"
                      : `Current access: ${form.allowedChannels.join(", ")}`}
                  </p>
                </CardContent>
              </Card>

              <Card>
                <CardHeader>
                  <CardTitle>Send a request</CardTitle>
                  <CardDescription>
                    Use the gateway host and port—not necessarily the Web UI
                    address. All three JSON fields are required.
                  </CardDescription>
                </CardHeader>
                <CardContent className="grid gap-4">
                  <div className="bg-muted/50 flex items-center gap-3 rounded-lg border px-3 py-2.5">
                    <Badge variant="secondary" className="font-mono">
                      POST
                    </Badge>
                    <code className="min-w-0 flex-1 truncate text-xs">
                      {endpoint}
                    </code>
                  </div>
                  <div className="relative">
                    <pre className="bg-muted/50 overflow-x-auto rounded-lg border p-4 pr-12 font-mono text-xs leading-5">
                      {curlExample}
                    </pre>
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      onClick={copyExample}
                      className="absolute top-2 right-2"
                      title="Copy cURL"
                    >
                      {copied ? (
                        <IconCheck className="size-4" />
                      ) : (
                        <IconCopy className="size-4" />
                      )}
                    </Button>
                  </div>
                  <div className="flex gap-2 text-xs">
                    <IconKey className="text-muted-foreground mt-0.5 size-4 shrink-0" />
                    <p className="text-muted-foreground">
                      Replace <code>&lt;your-token&gt;</code> with the token you
                      configured. The token is always required; trusted IPs add
                      another access restriction. The message is delivered
                      directly; it does not invoke the AI agent.
                    </p>
                  </div>
                  <div className="bg-muted/40 rounded-lg border p-3 text-xs">
                    <p className="font-medium">WhatsApp formatting</p>
                    <p className="text-muted-foreground mt-1">
                      Webhook and AI messages are converted automatically. Use{" "}
                      <code>**bold**</code>, <code>*italic*</code>,{" "}
                      <code>~~strikethrough~~</code>, <code>`code`</code>,{" "}
                      headings, fenced code blocks, and Markdown links.
                    </p>
                  </div>
                  <div className="grid gap-3">
                    <div>
                      <p className="mb-1.5 text-xs font-medium">
                        Image or document from a public URL
                      </p>
                      <pre className="bg-muted/50 overflow-x-auto rounded-lg border p-3 font-mono text-xs leading-5">
                        {imageURLExample}
                      </pre>
                    </div>
                    <div>
                      <p className="mb-1.5 text-xs font-medium">
                        Upload a local image or document
                      </p>
                      <pre className="bg-muted/50 overflow-x-auto rounded-lg border p-3 font-mono text-xs leading-5">
                        {imageUploadExample}
                      </pre>
                    </div>
                    <p className="text-muted-foreground text-xs">
                      The <code>message</code> becomes the image or document
                      caption and may be omitted when sending an attachment. URL
                      downloads accept only public HTTP(S) destinations.
                    </p>
                  </div>
                </CardContent>
              </Card>
            </div>

            <div className="grid gap-5 lg:sticky lg:top-0">
              <Card>
                <CardHeader>
                  <div className="flex items-center gap-2">
                    <IconSend className="text-primary size-5" />
                    <CardTitle>Test delivery</CardTitle>
                  </div>
                  <CardDescription>
                    Save and restart first if prompted, then send a real message
                    to an enabled channel.
                  </CardDescription>
                </CardHeader>
                <CardContent className="grid gap-4">
                  <Field label="Channel">
                    <Input
                      value={testChannel}
                      onChange={(event) => setTestChannel(event.target.value)}
                      placeholder="telegram"
                    />
                  </Field>
                  <Field
                    label="Chat ID"
                    hint="The destination ID used by the selected channel."
                  >
                    <Input
                      value={testChatID}
                      onChange={(event) => setTestChatID(event.target.value)}
                      placeholder="12345678"
                      className="font-mono"
                    />
                  </Field>
                  <Field label="Message">
                    <Textarea
                      value={testMessage}
                      onChange={(event) => setTestMessage(event.target.value)}
                      rows={4}
                    />
                  </Field>
                  <Button
                    onClick={handleTest}
                    disabled={testing || isDirty || !form.enabled}
                    className="w-full"
                  >
                    {testing ? (
                      <IconRefresh className="size-4 animate-spin" />
                    ) : (
                      <IconSend className="size-4" />
                    )}
                    {testing ? "Sending…" : "Send test message"}
                  </Button>
                  {isDirty && (
                    <p className="text-center text-xs text-amber-600">
                      Save your changes before testing.
                    </p>
                  )}
                  {!form.enabled && (
                    <p className="text-muted-foreground text-center text-xs">
                      Enable and save the webhook before testing.
                    </p>
                  )}
                </CardContent>
              </Card>

              <Card>
                <CardHeader>
                  <CardTitle>Quick checklist</CardTitle>
                </CardHeader>
                <CardContent>
                  <ol className="grid gap-3 text-sm">
                    {[
                      "Enable the webhook.",
                      "Generate or enter a strong access token.",
                      "Allow all IPs or restrict access to trusted IPs.",
                      "Choose allowed channels, or leave all unselected.",
                      "Save and restart the gateway if prompted.",
                      "Send a test message before connecting your app.",
                    ].map((step, index) => (
                      <li key={step} className="flex gap-3">
                        <span className="bg-primary/10 text-primary flex size-6 shrink-0 items-center justify-center rounded-full text-xs font-semibold">
                          {index + 1}
                        </span>
                        <span className="pt-0.5">{step}</span>
                      </li>
                    ))}
                  </ol>
                </CardContent>
              </Card>
            </div>
          </div>
        </div>
      </div>

      {isDirty && (
        <div className="border-border/70 bg-background/95 shrink-0 border-t px-3 py-3 backdrop-blur lg:px-6">
          <div className="mx-auto flex w-full max-w-[1120px] flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
            <ConfigChangeNotice
              kind="save"
              title="Unsaved webhook changes"
              description="Save before testing or leaving this page."
              className="flex-1"
            />
            <div className="flex justify-end gap-2">
              <Button
                variant="outline"
                onClick={() => setForm(baseline)}
                disabled={saving}
              >
                {t("common.reset")}
              </Button>
              <Button onClick={handleSave} disabled={saving}>
                <IconDeviceFloppy className="size-4" />
                {saving ? t("common.saving") : t("common.save")}
              </Button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
