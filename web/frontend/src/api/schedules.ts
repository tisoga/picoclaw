import { launcherFetch } from "@/api/http"

export interface CronSchedule {
  kind: "cron" | "at" | "every"
  atMs?: number
  everyMs?: number
  expr?: string
  tz?: string
}

export interface ScheduledJob {
  id: string
  name: string
  enabled: boolean
  schedule: CronSchedule
  payload: {
    kind: "agent_turn"
    message: string
    channel: string
    to: string
  }
  state: {
    nextRunAtMs?: number
    lastRunAtMs?: number
    lastStatus?: string
    lastError?: string
  }
  createdAtMs: number
  updatedAtMs: number
  deleteAfterRun: boolean
}

export type ScheduledJobInput = Pick<
  ScheduledJob,
  "name" | "enabled" | "schedule" | "payload"
>

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const response = await launcherFetch(path, options)
  if (!response.ok) {
    const body = (await response.json().catch(() => ({}))) as { error?: string }
    throw new Error(body.error || `Request failed (${response.status})`)
  }
  return response.json() as Promise<T>
}

export async function getScheduledJobs(): Promise<ScheduledJob[]> {
  const response = await request<{ jobs: ScheduledJob[] }>("/api/schedules")
  return response.jobs
}

export function createScheduledJob(input: ScheduledJobInput) {
  return request<ScheduledJob>("/api/schedules", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(input),
  })
}

export function updateScheduledJob(id: string, input: ScheduledJobInput) {
  return request<ScheduledJob>(`/api/schedules/${encodeURIComponent(id)}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(input),
  })
}

export function deleteScheduledJob(id: string) {
  return request<{ status: string }>(
    `/api/schedules/${encodeURIComponent(id)}`,
    { method: "DELETE" },
  )
}

export function runScheduledJob(id: string) {
  return request<{ status: string }>(
    `/api/schedules/${encodeURIComponent(id)}/run`,
    { method: "POST" },
  )
}
