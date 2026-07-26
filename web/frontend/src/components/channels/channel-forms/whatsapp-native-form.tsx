import {
  IconBrandWhatsapp,
  IconCheck,
  IconEye,
  IconMoodSmile,
  IconPhoto,
  IconShieldLock,
} from "@tabler/icons-react"

import type { ChannelConfig } from "@/api/channels"
import {
  type ArrayFieldFlusher,
  ChannelArrayListField,
} from "@/components/channels/channel-array-list-field"
import {
  asStringArray,
  parseAllowFromInput,
} from "@/components/channels/channel-array-utils"
import { ConfigChangeNotice } from "@/components/config-change-notice"
import { Field, SwitchCardField } from "@/components/shared-form"
import { Badge } from "@/components/ui/badge"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"

interface WhatsAppNativeFormProps {
  config: ChannelConfig
  onChange: (key: string, value: unknown) => void
  registerArrayFieldFlusher?: (
    fieldPath: string,
    flusher: ArrayFieldFlusher | null,
  ) => void
  arrayFieldResetVersion?: number
}

function asString(value: unknown): string {
  return typeof value === "string" ? value : ""
}

function asBool(value: unknown): boolean {
  return value === true
}

function PolicyField({
  label,
  hint,
  value,
  onChange,
}: {
  label: string
  hint: string
  value: string
  onChange: (value: string) => void
}) {
  return (
    <Field label={label} hint={hint}>
      <Select value={value || "default"} onValueChange={onChange}>
        <SelectTrigger>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="default">Default — use Allow From</SelectItem>
          <SelectItem value="open">Open — accept everyone</SelectItem>
          <SelectItem value="allowlist">
            Allowlist — accept listed IDs only
          </SelectItem>
          <SelectItem value="disabled">Disabled — ignore messages</SelectItem>
        </SelectContent>
      </Select>
    </Field>
  )
}

export function WhatsAppNativeForm({
  config,
  onChange,
  registerArrayFieldFlusher,
  arrayFieldResetVersion,
}: WhatsAppNativeFormProps) {
  const ackReaction = asString(config.ack_reaction)

  return (
    <div className="space-y-6">
      <Card className="overflow-hidden shadow-sm">
        <div className="grid gap-4 bg-gradient-to-br from-emerald-500/10 via-emerald-500/5 to-transparent p-6 md:grid-cols-[1fr_auto] md:items-center">
          <div className="flex gap-4">
            <div className="flex size-11 shrink-0 items-center justify-center rounded-xl bg-emerald-500/15 text-emerald-700">
              <IconBrandWhatsapp className="size-6" />
            </div>
            <div>
              <CardTitle>Native WhatsApp connection</CardTitle>
              <CardDescription className="mt-1 max-w-2xl">
                Connects directly through WhatsApp Web using the linked-device
                session. No external bridge is required.
              </CardDescription>
            </div>
          </div>
          <Badge variant="secondary" className="w-fit gap-1">
            <IconCheck className="size-3.5" />
            Native mode
          </Badge>
        </div>
        <CardContent className="px-6 py-5">
          <Field
            label="Session storage"
            hint="Keeps the linked-device login. Changing this path requires scanning the WhatsApp QR code again."
          >
            <Input
              value={asString(config.session_store_path)}
              onChange={(event) =>
                onChange("session_store_path", event.target.value)
              }
              placeholder="~/.picoclaw/workspace/whatsapp"
              className="font-mono"
            />
          </Field>
        </CardContent>
      </Card>

      <Card className="shadow-sm">
        <CardHeader>
          <div className="flex items-center gap-2">
            <IconShieldLock className="size-5 text-emerald-700" />
            <CardTitle>Who can use the bot</CardTitle>
          </div>
          <CardDescription>
            Use WhatsApp IDs such as a phone number with country code or an
            incoming LID, for example 628123456789 or 136953756426480@lid.
          </CardDescription>
        </CardHeader>
        <CardContent className="grid gap-5">
          <ChannelArrayListField
            label="Allow From"
            hint="Main sender allowlist. Leave empty for the legacy default, or set explicit IDs for tighter access."
            value={asStringArray(config.allow_from)}
            onChange={(value) => onChange("allow_from", value)}
            placeholder="136953756426480@lid"
            parser={parseAllowFromInput}
            fieldPath="allow_from"
            registerFlusher={registerArrayFieldFlusher}
            resetVersion={arrayFieldResetVersion}
          />
          <div className="grid gap-5 md:grid-cols-2">
            <PolicyField
              label="Direct-message policy"
              hint="Controls private conversations."
              value={asString(config.dm_policy)}
              onChange={(value) =>
                onChange("dm_policy", value === "default" ? "" : value)
              }
            />
            <PolicyField
              label="Group policy"
              hint="Controls messages received in groups."
              value={asString(config.group_policy)}
              onChange={(value) =>
                onChange("group_policy", value === "default" ? "" : value)
              }
            />
          </div>
          <ChannelArrayListField
            label="Group member allowlist"
            hint="Optional sender IDs allowed inside groups when Group policy is Allowlist."
            value={asStringArray(config.group_allow_from)}
            onChange={(value) => onChange("group_allow_from", value)}
            placeholder="628123456789"
            parser={parseAllowFromInput}
            fieldPath="group_allow_from"
            registerFlusher={registerArrayFieldFlusher}
            resetVersion={arrayFieldResetVersion}
          />
        </CardContent>
      </Card>

      <Card className="shadow-sm">
        <CardHeader>
          <div className="flex items-center gap-2">
            <IconMoodSmile className="size-5 text-amber-600" />
            <CardTitle>Message feedback</CardTitle>
          </div>
          <CardDescription>
            Control what people see when PicoClaw receives and processes a
            message.
          </CardDescription>
        </CardHeader>
        <CardContent className="grid gap-5">
          <Field
            label="Acknowledgement reaction"
            hint="Optional permanent reaction added immediately when a message arrives. Enter one standard Unicode emoji, such as ✅, 👍, or 🙏."
          >
            <Input
              value={ackReaction}
              onChange={(event) => onChange("ack_reaction", event.target.value)}
              placeholder="Disabled (no permanent reaction)"
              maxLength={16}
            />
          </Field>

          <ConfigChangeNotice
            kind="restart"
            title="The temporary 👀 reaction is separate"
            description={
              ackReaction
                ? `PicoClaw adds ${ackReaction} as the configured acknowledgement. It may also show 👀 while the AI is processing, then remove 👀 after delivery.`
                : "Leaving this field empty disables the permanent acknowledgement, but PicoClaw may still show 👀 temporarily while the AI is processing."
            }
          />

          <SwitchCardField
            label="Send read receipts"
            hint="Marks incoming messages as read. The sender may see blue check marks after PicoClaw receives the message."
            checked={asBool(config.send_read_receipts)}
            onCheckedChange={(checked) =>
              onChange("send_read_receipts", checked)
            }
            ariaLabel="Send WhatsApp read receipts"
          />
        </CardContent>
      </Card>

      <Card className="shadow-sm">
        <CardHeader>
          <div className="flex items-center gap-2">
            <IconPhoto className="size-5 text-blue-600" />
            <CardTitle>Media</CardTitle>
          </div>
          <CardDescription>
            Applies to incoming and outgoing images, audio, video, and
            documents.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <Field
            label="Maximum media size"
            hint="Files larger than this limit are rejected. Zero uses PicoClaw’s 50 MB default."
          >
            <div className="relative">
              <Input
                type="number"
                min={0}
                value={Number(config.media_max_mb) || 0}
                onChange={(event) =>
                  onChange(
                    "media_max_mb",
                    Math.max(0, Number(event.target.value) || 0),
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

      <div className="text-muted-foreground bg-muted/30 flex items-start gap-2 rounded-lg border p-3 text-xs">
        <IconEye className="mt-0.5 size-4 shrink-0" />
        Changes to WhatsApp configuration require a gateway restart. The header
        will show the restart action after saving.
      </div>
    </div>
  )
}
