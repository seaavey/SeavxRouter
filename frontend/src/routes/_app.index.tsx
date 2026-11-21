import { useState } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { PageHeader } from '@/components/page-header'
import { RequestTable } from '@/components/request-table'
import { RequestDetail } from '@/features/requests/request-detail'
import { VolumeChart } from '@/components/volume-chart'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Skeleton } from '@/components/ui/skeleton'
import { useLiveLogs } from '@/hooks/use-live-logs'
import { logsQuery, usageQuery } from '@/lib/queries'
import { format } from '@/lib/format'
import type { RequestLog } from '@/lib/api'

export const Route = createFileRoute('/_app/')({
  validateSearch: (search: Record<string, unknown>): { days?: number } => ({
    days: Number(search.days) === 30 ? 30 : undefined,
  }),
  component: OverviewPage,
})

function OverviewPage() {
  const { days: selectedDays } = Route.useSearch()
  const days = selectedDays ?? 7
  const navigate = useNavigate({ from: Route.fullPath })
  const usage = useQuery(usageQuery(days))
  const logs = useQuery(logsQuery(0, 8))
  // Keeps the table fresh; the feed has no UI of its own.
  useLiveLogs()
  const [selected, setSelected] = useState<RequestLog | null>(null)

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title="Overview"
        description="What this gateway handled, and what it cost."
        actions={
          <Select
            value={String(days)}
            onValueChange={value => navigate({ search: { days: Number(value) === 30 ? 30 : undefined } })}
          >
            <SelectTrigger className="h-8 w-36" aria-label="Time range">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="7">Last 7 days</SelectItem>
              <SelectItem value="30">Last 30 days</SelectItem>
            </SelectContent>
          </Select>
        }
      />

      {usage.isError && (
        <Alert variant="destructive">
          <AlertTitle>Usage unavailable</AlertTitle>
          <AlertDescription>{usage.error.message}</AlertDescription>
        </Alert>
      )}

      <section aria-label="Totals" className="grid gap-4 sm:grid-cols-3">
        <Metric
          label="Requests"
          value={usage.data?.totals.requests}
          format={format.count}
          hint={`in the last ${days} days`}
          loading={usage.isPending}
        />
        <Metric
          label="Tokens"
          value={usage.data?.totals.total_tokens}
          format={format.compact}
          hint={usage.data ? `${format.compact(usage.data.totals.prompt_tokens)} in, ${format.compact(usage.data.totals.completion_tokens)} out` : undefined}
          loading={usage.isPending}
        />
        <Metric
          label="Estimated cost"
          value={usage.data?.totals.cost_usd}
          format={format.moneyShort}
          hint="Priced from the model catalog"
          loading={usage.isPending}
        />
      </section>

      <Card className="gap-0 py-0">
        <CardHeader className="border-b border-border py-4">
          <CardTitle>Request volume</CardTitle>
          <CardDescription>Daily totals across every provider, in UTC.</CardDescription>
        </CardHeader>
        <CardContent className="px-0">
          {usage.data
            ? <VolumeChart usage={usage.data} days={days} />
            : <div className="p-6"><Skeleton className="h-48 w-full" /></div>}
        </CardContent>
      </Card>

      <Card className="gap-0 py-0">
        <CardHeader className="border-b border-border py-4">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="grid gap-1">
              <CardTitle>Latest requests</CardTitle>
              <CardDescription>The newest traffic this gateway recorded.</CardDescription>
            </div>
            <Button variant="outline" size="sm" asChild>
              <Link to="/requests">View all</Link>
            </Button>
          </div>
        </CardHeader>
        <CardContent className="px-0">
          <RequestTable rows={logs.data ?? []} loading={logs.isPending} onSelect={setSelected} pageSize={6} />
        </CardContent>
      </Card>

      <RequestDetail log={selected} onClose={() => setSelected(null)} />
    </div>
  )
}

function Metric({ label, value, format: render, hint, loading }: {
  label: string
  value?: number
  format: (value: number) => string
  hint?: string
  loading?: boolean
}) {
  return (
    <div className="rounded-xl border border-border bg-card p-5">
      <p className="text-sm text-muted-foreground">{label}</p>
      {loading
        ? <Skeleton className="mt-2 h-9 w-28" />
        : <p className="mt-1 text-3xl leading-tight font-medium tabular tracking-tight">
            {value === undefined ? '—' : render(value)}
          </p>}
      {hint && <p className="mt-1 text-xs text-muted-foreground">{hint}</p>}
    </div>
  )
}
