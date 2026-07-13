import { createFileRoute } from "@tanstack/react-router"

import { QuotaPage } from "@/components/credentials/quota-page"

export const Route = createFileRoute("/quota")({
  component: QuotaPage,
})
