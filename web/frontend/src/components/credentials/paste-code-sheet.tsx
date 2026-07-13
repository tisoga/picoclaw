import { IconExternalLink, IconKey, IconLoader2 } from "@tabler/icons-react"
import { useTranslation } from "react-i18next"

import type { OAuthFlowState } from "@/api/oauth"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"

interface PasteCodeSheetProps {
  open: boolean
  flow: OAuthFlowState | null
  pasteCode: string
  actionBusy: boolean
  flowHint: string
  onPasteCodeChange: (value: string) => void
  onSubmit: () => void
  onOpenChange: (open: boolean) => void
}

export function PasteCodeSheet({
  open,
  flow,
  pasteCode,
  actionBusy,
  flowHint,
  onPasteCodeChange,
  onSubmit,
  onOpenChange,
}: PasteCodeSheetProps) {
  const { t } = useTranslation()

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side="right"
        className="data-[side=right]:!w-full data-[side=right]:sm:!w-[480px] data-[side=right]:sm:!max-w-[480px]"
      >
        <SheetHeader className="border-b-muted border-b px-6 py-5">
          <SheetTitle>
            {t("credentials.pasteCode.title", "Complete Login")}
          </SheetTitle>
          <SheetDescription>
            {t(
              "credentials.pasteCode.description",
              "A new tab should have opened for you to log in. Once you authorize, you'll be redirected to a page that may fail to load. Copy the 'code' parameter from the URL address bar and paste it below.",
            )}
          </SheetDescription>
        </SheetHeader>

        <div className="space-y-4 px-6 py-5">
          <div>
            <p className="text-muted-foreground text-xs uppercase">
              {t("credentials.pasteCode.authUrl", "Authorization URL")}
            </p>
            <div className="mt-1">
              {flow?.auth_url ? (
                <a
                  href={flow.auth_url}
                  target="_blank"
                  rel="noreferrer"
                  className="text-primary inline-flex items-center gap-1 text-sm break-all underline"
                >
                  <IconExternalLink className="size-4" />
                  {t(
                    "credentials.pasteCode.openLinkAgain",
                    "Open auth page again",
                  )}
                </a>
              ) : (
                <span className="text-sm">-</span>
              )}
            </div>
          </div>

          <div>
            <p className="text-muted-foreground mb-2 text-xs uppercase">
              {t(
                "credentials.pasteCode.codeLabel",
                "Authorization Code or URL",
              )}
            </p>
            <Input
              value={pasteCode}
              onChange={(e) => onPasteCodeChange(e.target.value)}
              placeholder={t(
                "credentials.pasteCode.placeholder",
                "Paste the entire failed localhost URL or just the code here",
              )}
              className="font-mono"
            />
          </div>

          {flow && flow.status !== "pending" && (
            <div className="bg-muted rounded-md border px-3 py-2 text-sm">
              {flowHint}
            </div>
          )}
        </div>

        <SheetFooter className="border-t-muted border-t px-6 py-4">
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <Button
            onClick={onSubmit}
            disabled={
              actionBusy || !pasteCode.trim() || flow?.status !== "pending"
            }
          >
            {actionBusy && <IconLoader2 className="mr-2 size-4 animate-spin" />}
            <IconKey className="mr-2 size-4" />
            {t("credentials.pasteCode.submit", "Submit")}
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  )
}
