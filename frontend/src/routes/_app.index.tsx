import { useState } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { Activity, CircleDollarSign, Coins } from 'lucide-react'
import { PageHeader } from '@/components/page-header'
import { RequestTable } from '@/components/request-table'
import { RequestDetail } from '@/features/requests/request-detail'
import { VolumeChart } from '@/components/volume-chart'
import { StatusLabel } from '@/components/status'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Skeleton } from '@/components/ui/skeleton'
import { useLiveLogs } from '@/hooks/use-live-logs'
import { logsQuery, usageQuery } from '@/lib/queries'
import { format } from '@/lib/format'
import type { RequestLog } from '@/lib/api'
import type { StreamStatus } from '@/hooks/use-live-logs'

export const Route = createFileRoute('/_app/')({
  validateSearch: (search: Record<string, unknown>): { days?: number } => ({
    days: Number(search.days) === 30 ? 30 : undefined,
  }),
  component: OverviewPage,
})

const streamCopy: Record<StreamStatus, { tone: 'positive' | 'muted' | 'warning' | 'destructive'; label: string }> = {
  connecting: { tone: 'muted', label: 'Connecting' },
  live: { tone: 'positive', label: 'Live' },
  reconnecting: { tone: 'warning', label: 'Reconnecting' },
  unavailable: { tone: 'destructive', label: 'Live updates unavailable' },
}

function OverviewPage() {
  const { days: selectedDays } = Route.useSearch()
  const days = selectedDays ?? 7
  const navigate = useNavigate({ from: Route.fullPath })
  const usage = useQuery(usageQuery(days))
  const logs = useQuery(logsQuery(0, 8))
  const stream = streamCopy[useLiveLogs()]
  const [selected, setSelected] = useState<RequestLog | null>(null)

  return (
    <>
      <PageHeader
        title="Overview"
        description="Request volume, token usage, and spend recorded by this gateway."
        actions={
          <Select
            value={String(days)}
            onValueChange={value => navigate({ search: { days: Number(value) === 30 ? 30 : undefined } })}
          >
            <SelectTrigger className="h-8 w-36" aria-label="Usage period">
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

      <section aria-label="Totals" className="grid gap-px overflow-hidden rounded-xl bg-border sm:grid-cols-3">
        <Metric
          label="Requests"
          value={usage.data && format.count(usage.data.totals.requests)}
          hint={`Handled in the last ${days} days`}
          icon={<Activity className="size-3.5" />}
          loading={usage.isPending}
        />
        <Metric
          label="Tokens"
          value={usage.data && format.compact(usage.data.totals.total_tokens)}
          hint={usage.data ? `${format.count(usage.data.totals.prompt_tokens)} in · ${format.count(usage.data.totals.completion_tokens)} out` : undefined}
          icon={<Coins className="size-3.5" />}
          loading={usage.isPending}
        />
        <Metric
          label="Estimated cost"
          value={usage.data && format.moneyShort(usage.data.totals.cost_usd)}
          hint="Priced from the model catalog"
          icon={<CircleDollarSign className="size-3.5" />}
          loading={usage.isPending}
          accent
        />
      </section>

      <Card className="py-0">
        <CardHeader className="border-b border-border py-4">
          <CardTitle>Request volume</CardTitle>
          <CardDescription>Daily requests across every provider, in UTC.</CardDescription>
        </CardHeader>
        <CardContent className="px-0">
          {usage.data
            ? <VolumeChart usage={usage.data} days={days} />
            : <div className="grid gap-2 p-5"><Skeleton className="h-44 w-full" /></div>}
        </CardContent>
      </Card>

      <Card className="py-0">
        <CardHeader className="border-b border-border py-4">
          <div className="flex items-start justify-between gap-3">
            <div className="grid gap-1">
              <CardTitle>Latest requests</CardTitle>
              <CardDescription>The newest traffic the gateway recorded.</CardDescription>
            </div>
            <div className="flex items-center gap-3">
              <StatusLabel tone={stream.tone} pulse={stream.tone === 'positive'}>{stream.label}</StatusLabel>
              <Button variant="ghost" size="sm" asChild>
                <Link to="/requests">View all</Link>
              </Button>
            </div>
          </div>
        </CardHeader>
        <CardContent className="px-0">
          <RequestTable rows={logs.data ?? []} loading={logs.isPending} onSelect={setSelected} pageSize={6} />
        </CardContent>
      </Card>

      <RequestDetail log={selected} onClose={() => setSelected(null)} />
    </>
  )
}

function Metric({ label, value, hint, icon, loading, accent }: {
  label: string
  value?: string | false
  hint?: string
  icon: React.ReactNode
  loading?: boolean
  accent?: boolean
}) {
  return (
    <div className="bg-card p-5">
      <div className="flex items-center gap-2 text-xs text-muted-foreground">
        {icon}
        {label}
      </div>
      {loading
        ? <Skeleton className="mt-3 h-8 w-28" />
        : <p className={`mt-3 font-heading text-3xl leading-none font-medium tabular tracking-tight ${accent ? 'text-signal' : ''}`}>
            {value || '—'}
          </p>}
      {hint && <p className="mt-2 font-mono text-[11px] text-muted-foreground/70">{hint}</p>}
    </div>
  )
}
