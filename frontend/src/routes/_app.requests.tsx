import { useState } from 'react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { ChevronLeft, ChevronRight } from 'lucide-react'
import { PageHeader } from '@/components/page-header'
import { RequestTable } from '@/components/request-table'
import { RequestDetail } from '@/features/requests/request-detail'
import { StatusLabel } from '@/components/status'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { useLiveLogs } from '@/hooks/use-live-logs'
import { logsQuery } from '@/lib/queries'
import type { RequestLog } from '@/lib/api'
import type { StreamStatus } from '@/hooks/use-live-logs'

const PAGE_SIZE = 25

const streamCopy: Record<StreamStatus, { tone: 'positive' | 'muted' | 'warning' | 'negative'; label: string }> = {
  connecting: { tone: 'muted', label: 'Connecting' },
  live: { tone: 'positive', label: 'Live' },
  reconnecting: { tone: 'warning', label: 'Reconnecting' },
  unavailable: { tone: 'negative', label: 'Updates paused' },
}

export const Route = createFileRoute('/_app/requests')({
  validateSearch: (search: Record<string, unknown>): { page?: number } => {
    const page = Number(search.page)
    return { page: Number.isInteger(page) && page > 1 ? page : undefined }
  },
  component: RequestsPage,
})

function RequestsPage() {
  const { page: selectedPage } = Route.useSearch()
  const page = selectedPage ?? 1
  const navigate = useNavigate({ from: Route.fullPath })
  const offset = (page - 1) * PAGE_SIZE
  const logs = useQuery(logsQuery(offset, PAGE_SIZE))
  const [selected, setSelected] = useState<RequestLog | null>(null)
  // Older pages stay stable while you read them; only page one follows the feed.
  const { status: streamStatus } = useLiveLogs(page === 1)
  const stream = streamCopy[streamStatus]

  const rows = logs.data ?? []
  const hasNext = rows.length === PAGE_SIZE

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title="Requests"
        description="Pick a row to see the full request, provider, and token breakdown."
        actions={page === 1 ? <StatusLabel tone={stream.tone}>{stream.label}</StatusLabel> : undefined}
      />

      {logs.isError && (
        <Alert variant="destructive">
          <AlertTitle>Requests unavailable</AlertTitle>
          <AlertDescription>{logs.error.message}</AlertDescription>
        </Alert>
      )}

      <Card className="gap-0 py-0">
        <CardContent className="px-0">
          <RequestTable rows={rows} loading={logs.isPending} onSelect={setSelected} pageSize={PAGE_SIZE} />
        </CardContent>
        {(offset > 0 || hasNext) && !logs.isPending && (
          <div className="flex items-center justify-between gap-3 border-t border-border px-4 py-3">
            <span className="text-xs text-muted-foreground">
              Showing {offset + 1}–{offset + rows.length}
            </span>
            <div className="flex gap-2">
              <Button
                variant="outline"
                size="sm"
                disabled={page === 1}
                onClick={() => navigate({ search: { page: page === 2 ? undefined : page - 1 } })}
              >
                <ChevronLeft className="size-4" />
                Previous
              </Button>
              <Button
                variant="outline"
                size="sm"
                disabled={!hasNext}
                onClick={() => navigate({ search: { page: page + 1 } })}
              >
                Next
                <ChevronRight className="size-4" />
              </Button>
            </div>
          </div>
        )}
      </Card>

      <RequestDetail log={selected} onClose={() => setSelected(null)} />
    </div>
  )
}
