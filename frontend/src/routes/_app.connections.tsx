import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { Plug } from 'lucide-react'
import { PageHeader } from '@/components/page-header'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from '@/components/ui/empty'
import { Skeleton } from '@/components/ui/skeleton'
import { Input } from '@/components/ui/input'
import { Dot } from '@/components/status'
import { cn } from '@/lib/utils'
import { modelsQuery, providersQuery } from '@/lib/queries'
import { format } from '@/lib/format'
import type { ProviderQuota } from '@/lib/api'

export const Route = createFileRoute('/_app/connections')({ component: ConnectionsPage })

function ConnectionsPage() {
  const providers = useQuery(providersQuery)
  const models = useQuery(modelsQuery)
  const [filter, setFilter] = useState('')
  const query = filter.trim().toLowerCase()

  const rows = (providers.data ?? []).filter(provider => provider.provider.toLowerCase().includes(query))
  const visibleModels = (models.data ?? []).filter(model => `${model.id} ${model.owned_by ?? ''}`.toLowerCase().includes(query))

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title="Connections"
        description="The provider accounts this gateway routes to, and the models they expose."
      />

      {providers.isError && (
        <Alert variant="destructive">
          <AlertTitle>Connections unavailable</AlertTitle>
          <AlertDescription>{providers.error.message}</AlertDescription>
        </Alert>
      )}

      {providers.isPending
        ? <div className="grid gap-4 lg:grid-cols-2">{Array.from({ length: 4 }, (_, index) => <Skeleton key={index} className="h-40 rounded-xl" />)}</div>
        : rows.length === 0
          ? (
            <Card>
              <Empty>
                <EmptyHeader>
                  <EmptyMedia variant="icon"><Plug /></EmptyMedia>
                  <EmptyTitle>No provider accounts</EmptyTitle>
                  <EmptyDescription>Sign an account in so the gateway has somewhere to route requests.</EmptyDescription>
                </EmptyHeader>
              </Empty>
            </Card>
          )
          : <div className="grid gap-4 lg:grid-cols-2">{rows.map(provider => <ProviderCard key={provider.provider} provider={provider} />)}</div>}

      <Card className="gap-0 py-0">
        <CardHeader className="border-b border-border py-4">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="grid gap-1">
              <CardTitle>Available models</CardTitle>
              <CardDescription>Model IDs served on <code className="text-xs">/v1/models</code>.</CardDescription>
            </div>
            <Input
              value={filter}
              onChange={event => setFilter(event.target.value)}
              placeholder="Search models"
              aria-label="Search models"
              className="h-8 w-full sm:w-56"
            />
          </div>
        </CardHeader>
        <CardContent className="px-0">
          {models.isPending
            ? <div className="grid gap-2 p-4">{Array.from({ length: 5 }, (_, index) => <Skeleton key={index} className="h-8 w-full" />)}</div>
            : visibleModels.length === 0
              ? <p className="px-4 py-6 text-sm text-muted-foreground">{models.data?.length ? 'No models match that search.' : 'No models exposed yet.'}</p>
              : (
                <ul className="divide-y divide-border">
                  {visibleModels.map(model => (
                    <li key={model.id} className="flex items-center justify-between gap-4 px-4 py-2.5">
                      <span className="min-w-0 truncate text-sm">{model.id}</span>
                      <span className="shrink-0 text-xs text-muted-foreground">{model.owned_by || '—'}</span>
                    </li>
                  ))}
                </ul>
              )}
        </CardContent>
      </Card>
    </div>
  )
}

function ProviderCard({ provider }: { provider: ProviderQuota }) {
  const accounts = provider.accounts ?? []
  const ready = accounts.filter(account => account.status === 'ready').length
  const healthy = accounts.length > 0 && ready === accounts.length

  return (
    <Card className="gap-0 py-0">
      <CardHeader className="py-4">
        <div className="flex items-start justify-between gap-3">
          <div className="grid min-w-0 gap-1">
            <CardTitle className="capitalize">{provider.provider}</CardTitle>
            <CardDescription className="truncate text-xs">{provider.default_model || 'No default model'}</CardDescription>
          </div>
          <span className="inline-flex shrink-0 items-center gap-2 text-xs text-muted-foreground">
            <Dot tone={healthy ? 'positive' : ready > 0 ? 'warning' : 'muted'} />
            {ready} of {accounts.length || 0} ready
          </span>
        </div>
      </CardHeader>
      <CardContent className="border-t border-border px-0">
        {accounts.length === 0
          ? <p className="px-4 py-4 text-sm text-muted-foreground">No accounts signed in.</p>
          : (
            <ul className="divide-y divide-border">
              {accounts.map((account, index) => (
                <li key={account.account_id ?? index} className="flex items-center justify-between gap-4 px-4 py-2.5">
                  <div className="grid min-w-0">
                    <span className="truncate text-sm">{account.email || account.account_id || `Account ${index + 1}`}</span>
                    {(account.tier || account.access_token_expires_at) && (
                      <span className="truncate text-xs text-muted-foreground">
                        {[account.tier, account.access_token_expires_at && `expires ${format.fullTime(account.access_token_expires_at)}`]
                          .filter(Boolean)
                          .join(' · ')}
                      </span>
                    )}
                  </div>
                  <span className={cn('shrink-0 text-xs', account.status === 'ready' ? 'text-muted-foreground' : 'text-warning')}>
                    {account.status}
                  </span>
                </li>
              ))}
            </ul>
          )}
      </CardContent>
    </Card>
  )
}
