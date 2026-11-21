import { createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { Boxes } from 'lucide-react'
import { PageHeader } from '@/components/page-header'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from '@/components/ui/empty'
import { Skeleton } from '@/components/ui/skeleton'
import { Dot, Figure } from '@/components/status'
import { providersQuery } from '@/lib/queries'
import { format } from '@/lib/format'
import type { ProviderQuota } from '@/lib/api'

export const Route = createFileRoute('/_app/providers')({ component: ProvidersPage })

function ProvidersPage() {
  const providers = useQuery(providersQuery)
  const rows = providers.data ?? []

  return (
    <>
      <PageHeader title="Providers" description="Upstream accounts the gateway can route to, and whether they are serving." />

      {providers.isError && (
        <Alert variant="destructive">
          <AlertTitle>Quota unavailable</AlertTitle>
          <AlertDescription>{providers.error.message}</AlertDescription>
        </Alert>
      )}

      {providers.isPending
        ? <div className="grid gap-3 md:grid-cols-2">{Array.from({ length: 4 }, (_, index) => <Skeleton key={index} className="h-36 w-full rounded-xl" />)}</div>
        : rows.length === 0
          ? (
            <Card>
              <Empty>
                <EmptyHeader>
                  <EmptyMedia variant="icon"><Boxes /></EmptyMedia>
                  <EmptyTitle>No providers configured</EmptyTitle>
                  <EmptyDescription>Sign in a provider account so the gateway has somewhere to route.</EmptyDescription>
                </EmptyHeader>
              </Empty>
            </Card>
          )
          : (
            <div className="grid gap-3 md:grid-cols-2">
              {rows.map(provider => <ProviderCard key={provider.provider} provider={provider} />)}
            </div>
          )}
    </>
  )
}

function ProviderCard({ provider }: { provider: ProviderQuota }) {
  const accounts = provider.accounts ?? []
  const ready = accounts.filter(account => account.status === 'ready').length

  return (
    <Card className="gap-0 py-0">
      <CardHeader className="py-4">
        <div className="flex items-start justify-between gap-3">
          <div className="grid gap-1">
            <CardTitle className="font-mono text-sm">{provider.provider}</CardTitle>
            <CardDescription className="font-mono text-[11px]">{provider.default_model || 'no default model'}</CardDescription>
          </div>
          <span className="inline-flex items-center gap-2 text-xs text-muted-foreground">
            <Dot tone={ready === accounts.length && ready > 0 ? 'positive' : ready > 0 ? 'warning' : 'muted'} />
            {ready}/{accounts.length || 0} ready
          </span>
        </div>
      </CardHeader>
      <CardContent className="border-t border-border px-0">
        {accounts.length === 0
          ? <p className="px-5 py-4 text-sm text-muted-foreground">No accounts signed in.</p>
          : <ul className="divide-y divide-border">
              {accounts.map((account, index) => (
                <li key={`${account.account_id ?? index}`} className="flex items-center justify-between gap-4 px-5 py-3">
                  <div className="grid min-w-0 gap-0.5">
                    <span className="truncate text-sm">{account.email || account.account_id || `Account ${index + 1}`}</span>
                    <span className="flex flex-wrap items-center gap-x-3 gap-y-0.5">
                      {account.tier && <Figure>{account.tier}</Figure>}
                      {account.access_token_expires_at && <Figure>expires {format.fullTime(account.access_token_expires_at)}</Figure>}
                    </span>
                  </div>
                  <span className="flex shrink-0 items-center gap-1.5 text-xs text-muted-foreground">
                    <Dot tone={account.status === 'ready' ? 'positive' : 'warning'} />
                    {account.status}
                  </span>
                </li>
              ))}
            </ul>}
      </CardContent>
    </Card>
  )
}
