import {
  IconAlertTriangle,
  IconCalendarClock,
  IconEdit,
  IconInfoCircle,
  IconPlayerPlay,
  IconPlus,
  IconTrash,
} from "@tabler/icons-react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useMemo, useState } from "react"
import { toast } from "sonner"

import {
  type ScheduledJob,
  type ScheduledJobInput,
  createScheduledJob,
  deleteScheduledJob,
  getScheduledJobs,
  runScheduledJob,
  updateScheduledJob,
} from "@/api/schedules"
import { PageHeader } from "@/components/page-header"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"

type ScheduleKind = "cron" | "at" | "every"

interface JobForm {
  name: string
  enabled: boolean
  kind: ScheduleKind
  cronExpr: string
  atLocal: string
  everyMinutes: string
  channel: string
  destination: string
  message: string
}

const EMPTY_FORM: JobForm = {
  name: "",
  enabled: true,
  kind: "cron",
  cronExpr: "0 9 * * *",
  atLocal: "",
  everyMinutes: "60",
  channel: "telegram",
  destination: "",
  message: "",
}

function dateTimeLocal(ms?: number) {
  if (!ms) return ""
  const date = new Date(ms)
  const offset = date.getTimezoneOffset() * 60_000
  return new Date(ms - offset).toISOString().slice(0, 16)
}

function jobToForm(job: ScheduledJob): JobForm {
  return {
    name: job.name,
    enabled: job.enabled,
    kind: job.schedule.kind,
    cronExpr: job.schedule.expr || "0 9 * * *",
    atLocal: dateTimeLocal(job.schedule.atMs),
    everyMinutes: String((job.schedule.everyMs || 3_600_000) / 60_000),
    channel: job.payload.channel,
    destination: job.payload.to,
    message: job.payload.message,
  }
}

function formToInput(form: JobForm): ScheduledJobInput {
  const schedule =
    form.kind === "cron"
      ? { kind: "cron" as const, expr: form.cronExpr.trim() }
      : form.kind === "at"
        ? {
            kind: "at" as const,
            atMs: new Date(form.atLocal).getTime(),
          }
        : {
            kind: "every" as const,
            everyMs: Number(form.everyMinutes) * 60_000,
          }
  return {
    name: form.name.trim(),
    enabled: form.enabled,
    schedule,
    payload: {
      kind: "agent_turn",
      message: form.message.trim(),
      channel: form.channel.trim(),
      to: form.destination.trim(),
    },
  }
}

function formatSchedule(job: ScheduledJob) {
  if (job.schedule.kind === "cron") return job.schedule.expr
  if (job.schedule.kind === "at")
    return `Once on ${new Date(job.schedule.atMs || 0).toLocaleString()}`
  return `Every ${Math.round((job.schedule.everyMs || 0) / 60_000)} minutes`
}

function formatDate(ms?: number) {
  return ms ? new Date(ms).toLocaleString() : "—"
}

interface CronExplanation {
  summary: string
  warning?: string
}

function twoDigits(value: string) {
  return value.padStart(2, "0")
}

function describeCronPart(
  value: string,
  singular: string,
  plural: string,
): string {
  if (value === "*") return `every ${singular}`
  if (/^\*\/\d+$/.test(value)) return `every ${value.slice(2)} ${plural}`
  if (/^\d+$/.test(value)) return `${singular} ${value}`
  if (/^\d+(,\d+)+$/.test(value))
    return `${plural} ${value.replaceAll(",", ", ")}`
  if (/^\d+-\d+$/.test(value)) return `${plural} ${value}`
  return `${plural} matching “${value}”`
}

function explainCron(expression: string): CronExplanation {
  const fields = expression.trim().split(/\s+/)
  if (fields.length !== 5) {
    return {
      summary: `Enter exactly 5 fields. This expression currently has ${fields.length}.`,
      warning: "Format: minute hour day-of-month month day-of-week",
    }
  }

  const [minute, hour, day, month, weekday] = fields
  const everyDay = day === "*" && month === "*" && weekday === "*"

  if (minute === "*" && /^\d+$/.test(hour) && everyDay) {
    return {
      summary: `Every minute from ${twoDigits(hour)}:00 through ${twoDigits(hour)}:59, every day.`,
      warning: `To run only once at ${twoDigits(hour)}:00, use “0 ${hour} * * *”.`,
    }
  }
  if (/^\d+$/.test(minute) && /^\d+$/.test(hour) && everyDay) {
    return {
      summary: `Every day at ${twoDigits(hour)}:${twoDigits(minute)}.`,
    }
  }
  if (/^\*\/\d+$/.test(minute) && hour === "*" && everyDay) {
    return {
      summary: `Every ${minute.slice(2)} minutes, every day.`,
    }
  }
  if (
    /^\d+$/.test(minute) &&
    /^\d+$/.test(hour) &&
    day === "*" &&
    month === "*" &&
    weekday === "1-5"
  ) {
    return {
      summary: `Monday through Friday at ${twoDigits(hour)}:${twoDigits(minute)}.`,
    }
  }
  if (
    /^\d+$/.test(minute) &&
    /^\d+$/.test(hour) &&
    /^\d+$/.test(day) &&
    month === "*" &&
    weekday === "*"
  ) {
    return {
      summary: `On day ${day} of every month at ${twoDigits(hour)}:${twoDigits(minute)}.`,
    }
  }

  return {
    summary: `Runs when ${describeCronPart(minute, "minute", "minutes")}; ${describeCronPart(hour, "hour", "hours")}; ${describeCronPart(day, "day of month", "days of month")}; ${describeCronPart(month, "month", "months")}; and ${describeCronPart(weekday, "day of week", "days of week")}.`,
  }
}

export function SchedulesPage() {
  const queryClient = useQueryClient()
  const [open, setOpen] = useState(false)
  const [editing, setEditing] = useState<ScheduledJob | null>(null)
  const [form, setForm] = useState<JobForm>(EMPTY_FORM)
  const [deleting, setDeleting] = useState<ScheduledJob | null>(null)
  const [runningJobIDs, setRunningJobIDs] = useState<string[]>([])
  const jobsQuery = useQuery({
    queryKey: ["schedules"],
    queryFn: getScheduledJobs,
    refetchInterval: 30_000,
  })
  const jobs = useMemo(() => jobsQuery.data || [], [jobsQuery.data])
  const cronExplanation = useMemo(
    () => explainCron(form.cronExpr),
    [form.cronExpr],
  )

  const saveMutation = useMutation({
    mutationFn: (input: ScheduledJobInput) =>
      editing
        ? updateScheduledJob(editing.id, input)
        : createScheduledJob(input),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["schedules"] })
      setOpen(false)
      toast.success(editing ? "Scheduled job updated" : "Scheduled job created")
    },
    onError: (error) => toast.error(error.message),
  })
  const deleteMutation = useMutation({
    mutationFn: (id: string) => deleteScheduledJob(id),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["schedules"] })
      setDeleting(null)
      toast.success("Scheduled job deleted")
    },
    onError: (error) => toast.error(error.message),
  })
  const runMutation = useMutation({
    mutationFn: (id: string) => runScheduledJob(id),
    onSuccess: async (_, id) => {
      toast.success("Job started. Its result will update automatically.")
      await queryClient.invalidateQueries({ queryKey: ["schedules"] })
      window.setTimeout(() => {
        void queryClient.invalidateQueries({ queryKey: ["schedules"] })
      }, 3_000)
      window.setTimeout(() => {
        setRunningJobIDs((current) => current.filter((jobID) => jobID !== id))
        void queryClient.invalidateQueries({ queryKey: ["schedules"] })
      }, 10_000)
    },
    onError: (error, id) => {
      setRunningJobIDs((current) => current.filter((jobID) => jobID !== id))
      toast.error(error.message)
    },
  })

  const openCreate = () => {
    setEditing(null)
    setForm(EMPTY_FORM)
    setOpen(true)
  }
  const openEdit = (job: ScheduledJob) => {
    setEditing(job)
    setForm(jobToForm(job))
    setOpen(true)
  }
  const updateField = <K extends keyof JobForm>(key: K, value: JobForm[K]) =>
    setForm((current) => ({ ...current, [key]: value }))

  const canSave =
    form.name.trim() &&
    form.message.trim() &&
    form.channel.trim() &&
    form.destination.trim() &&
    (form.kind !== "cron" || form.cronExpr.trim()) &&
    (form.kind !== "at" ||
      (form.atLocal && Number.isFinite(new Date(form.atLocal).getTime()))) &&
    (form.kind !== "every" || Number(form.everyMinutes) > 0)

  return (
    <div className="flex h-full flex-col">
      <PageHeader title="Scheduled Jobs">
        <Button size="sm" onClick={openCreate}>
          <IconPlus className="size-4" />
          New job
        </Button>
      </PageHeader>

      <div className="flex-1 overflow-auto p-4 sm:p-8">
        <Card className="mb-5">
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <IconCalendarClock className="size-5" />
              Automatic channel delivery
            </CardTitle>
            <CardDescription>
              Jobs run inside the live gateway. The channel and destination chat
              ID decide where the result is delivered. Changes apply immediately
              and do not require a gateway restart.
            </CardDescription>
          </CardHeader>
        </Card>

        {jobsQuery.isLoading ? (
          <p className="text-muted-foreground">Loading scheduled jobs…</p>
        ) : jobsQuery.isError ? (
          <Card>
            <CardContent className="text-destructive py-6 text-sm">
              {jobsQuery.error.message}
            </CardContent>
          </Card>
        ) : jobs.length === 0 ? (
          <Card>
            <CardContent className="flex flex-col items-center gap-3 py-12 text-center">
              <IconCalendarClock className="text-muted-foreground size-10" />
              <div>
                <p className="font-medium">No scheduled jobs</p>
                <p className="text-muted-foreground text-sm">
                  Create a job to send an AI-generated response at a chosen
                  time.
                </p>
              </div>
              <Button variant="outline" onClick={openCreate}>
                Create your first job
              </Button>
            </CardContent>
          </Card>
        ) : (
          <div className="grid gap-4 xl:grid-cols-2">
            {jobs.map((job) => (
              <Card key={job.id}>
                <CardHeader>
                  <div className="flex items-start justify-between gap-4">
                    <div className="min-w-0">
                      <CardTitle className="truncate">{job.name}</CardTitle>
                      <CardDescription className="mt-1 font-mono">
                        {formatSchedule(job)}
                      </CardDescription>
                    </div>
                    <Badge variant={job.enabled ? "default" : "secondary"}>
                      {job.enabled ? "Enabled" : "Disabled"}
                    </Badge>
                  </div>
                </CardHeader>
                <CardContent className="space-y-4">
                  <div className="grid grid-cols-[7rem_1fr] gap-x-3 gap-y-2 text-sm">
                    <span className="text-muted-foreground">Destination</span>
                    <span className="truncate">
                      {job.payload.channel} · {job.payload.to}
                    </span>
                    <span className="text-muted-foreground">Next run</span>
                    <span>{formatDate(job.state.nextRunAtMs)}</span>
                    <span className="text-muted-foreground">Last result</span>
                    <span>
                      {job.state.lastStatus || "Never run"}
                      {job.state.lastError ? ` — ${job.state.lastError}` : ""}
                    </span>
                  </div>
                  <p className="bg-muted/50 line-clamp-3 rounded-md p-3 text-sm whitespace-pre-wrap">
                    {job.payload.message}
                  </p>
                  <div className="flex justify-end gap-2">
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={runningJobIDs.includes(job.id)}
                      onClick={() => {
                        setRunningJobIDs((current) => [
                          ...new Set([...current, job.id]),
                        ])
                        runMutation.mutate(job.id)
                      }}
                    >
                      <IconPlayerPlay className="size-4" />
                      {runningJobIDs.includes(job.id) ? "Running…" : "Run now"}
                    </Button>
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={() => openEdit(job)}
                    >
                      <IconEdit className="size-4" />
                      Edit
                    </Button>
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={() => setDeleting(job)}
                    >
                      <IconTrash className="size-4" />
                      Delete
                    </Button>
                  </div>
                </CardContent>
              </Card>
            ))}
          </div>
        )}
      </div>

      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
          <DialogHeader>
            <DialogTitle>
              {editing ? "Edit scheduled job" : "Create scheduled job"}
            </DialogTitle>
            <DialogDescription>
              The destination must be the platform chat ID, such as a Telegram
              numeric ID or a WhatsApp JID ending in @lid or @s.whatsapp.net.
            </DialogDescription>
          </DialogHeader>
          <div className="grid gap-4">
            <div className="grid gap-2">
              <Label htmlFor="job-name">Name</Label>
              <Input
                id="job-name"
                value={form.name}
                onChange={(event) => updateField("name", event.target.value)}
                placeholder="Daily animal image"
              />
            </div>
            <div className="grid gap-2 sm:grid-cols-2">
              <div className="grid gap-2">
                <Label>Schedule type</Label>
                <Select
                  value={form.kind}
                  onValueChange={(value) =>
                    updateField("kind", value as ScheduleKind)
                  }
                >
                  <SelectTrigger className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="cron">Cron expression</SelectItem>
                    <SelectItem value="at">One time</SelectItem>
                    <SelectItem value="every">Repeating interval</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              {form.kind === "cron" ? (
                <div className="grid gap-2">
                  <Label htmlFor="cron-expression">Cron expression</Label>
                  <Input
                    id="cron-expression"
                    className="font-mono"
                    value={form.cronExpr}
                    onChange={(event) =>
                      updateField("cronExpr", event.target.value)
                    }
                    placeholder="0 9 * * *"
                  />
                </div>
              ) : form.kind === "at" ? (
                <div className="grid gap-2">
                  <Label htmlFor="run-at">Run at</Label>
                  <Input
                    id="run-at"
                    type="datetime-local"
                    value={form.atLocal}
                    onChange={(event) =>
                      updateField("atLocal", event.target.value)
                    }
                  />
                </div>
              ) : (
                <div className="grid gap-2">
                  <Label htmlFor="every-minutes">Every (minutes)</Label>
                  <Input
                    id="every-minutes"
                    type="number"
                    min="1"
                    value={form.everyMinutes}
                    onChange={(event) =>
                      updateField("everyMinutes", event.target.value)
                    }
                  />
                </div>
              )}
            </div>
            {form.kind === "cron" && (
              <div className="bg-muted/30 space-y-2 rounded-lg border p-3">
                <div className="flex items-start gap-2 text-sm">
                  <IconInfoCircle className="text-primary mt-0.5 size-4 shrink-0" />
                  <div>
                    <p className="font-medium">What this schedule means</p>
                    <p className="text-muted-foreground">
                      {cronExplanation.summary}
                    </p>
                  </div>
                </div>
                {cronExplanation.warning && (
                  <div className="flex items-start gap-2 rounded-md bg-amber-500/10 p-2 text-xs text-amber-700 dark:text-amber-300">
                    <IconAlertTriangle className="mt-0.5 size-4 shrink-0" />
                    <span>{cronExplanation.warning}</span>
                  </div>
                )}
                <div className="text-muted-foreground border-t pt-2 text-xs">
                  <p className="font-mono">
                    minute · hour · day of month · month · day of week
                  </p>
                  <p className="mt-1">
                    Uses the gateway machine’s local time. Days of week: 0 is
                    Sunday, 1 is Monday, and 6 is Saturday.
                  </p>
                </div>
              </div>
            )}
            <div className="grid gap-2 sm:grid-cols-2">
              <div className="grid gap-2">
                <Label>Channel</Label>
                <Select
                  value={form.channel}
                  onValueChange={(value) => updateField("channel", value)}
                >
                  <SelectTrigger className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="telegram">Telegram</SelectItem>
                    <SelectItem value="whatsapp">WhatsApp</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div className="grid gap-2">
                <Label htmlFor="destination">Destination chat ID</Label>
                <Input
                  id="destination"
                  value={form.destination}
                  onChange={(event) =>
                    updateField("destination", event.target.value)
                  }
                  placeholder={
                    form.channel === "telegram"
                      ? "609941175"
                      : "136953756426480@lid"
                  }
                />
              </div>
            </div>
            <div className="grid gap-2">
              <Label htmlFor="job-message">Agent instruction</Label>
              <Textarea
                id="job-message"
                rows={5}
                value={form.message}
                onChange={(event) => updateField("message", event.target.value)}
                placeholder="Generate an image and send it with a short caption."
              />
            </div>
            <div className="flex items-center justify-between rounded-lg border p-3">
              <div>
                <Label htmlFor="job-enabled">Enabled</Label>
                <p className="text-muted-foreground text-xs">
                  Disabled jobs remain saved but will not run.
                </p>
              </div>
              <Switch
                id="job-enabled"
                checked={form.enabled}
                onCheckedChange={(value) => updateField("enabled", value)}
              />
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setOpen(false)}>
              Cancel
            </Button>
            <Button
              disabled={!canSave || saveMutation.isPending}
              onClick={() => saveMutation.mutate(formToInput(form))}
            >
              {saveMutation.isPending ? "Saving…" : "Save job"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={Boolean(deleting)} onOpenChange={() => setDeleting(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete scheduled job?</DialogTitle>
            <DialogDescription>
              “{deleting?.name}” will be permanently removed and will no longer
              run.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleting(null)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              disabled={deleteMutation.isPending}
              onClick={() => deleting && deleteMutation.mutate(deleting.id)}
            >
              Delete job
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
