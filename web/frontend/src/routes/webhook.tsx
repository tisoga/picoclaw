import { createFileRoute } from "@tanstack/react-router"

import { WebhookPage } from "@/components/webhook/webhook-page"

export const Route = createFileRoute("/webhook")({
  component: WebhookPage,
})
