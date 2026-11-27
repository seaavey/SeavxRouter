import { useState } from 'react'
import { Link, createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { ChevronRight, Plus } from 'lucide-react'
import { PageHeader } from '@/components/page-header'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { providersQuery } from '@/lib/queries'
import { ProviderIdentity } from '@/features/connections/provider-identity'
import { accountCounts } from '@/features/connections/provider-data'
import { AddProviderDialog } from '@/features/connections/add-provider-dialog'
import { sdkLabel } from '@/features/connections/provider-sdk'
import { cn } from '@/lib/utils'

export const Route = createFileRoute('/_app/connections/')({ component: ConnectionsPage })

function ConnectionsPage() {
  const providers = useQuery(providersQuery)
  const [search, setSearch] = useState('')
  const [adding, setAdding] = useState(false)
  const rows = (providers.data ?? []).filter(row => row.provider.toLowerCase().includes(search.trim().toLowerCase()))

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title="Connections"
        description="Provider accounts connected to this gateway"
        actions={<Button onClick={() => setAdding(true)}><Plus className="size-4" />Add provider</Button>}
      />
      <Input aria-label="Search providers" placeholder="Search providers" value={search} onChange={event => setSearch(event.target.value)} className="max-w-72" />
      {providers.isError && <p role="alert" className="text-sm text-destructive">{providers.error.message}</p>}
      <div className="overflow-hidden rounded-xl border border-border bg-card">
        <div className="grid grid-cols-[1fr_auto_auto] gap-6 border-b border-border px-5 py-3 text-xs text-muted-foreground sm:grid-cols-[1fr_8rem_8rem_1rem]">
          <span>Provider</span><span className="hidden text-right sm:block">Accounts</span><span className="text-right">Active</span><span aria-hidden />
        </div>
        {providers.isPending
          ? Array.from({ length: 5 }, (_, index) => <div key={index} className="border-b border-border p-5 last:border-0"><Skeleton className="h-6 w-full" /></div>)
          : rows.map(provider => {
            const counts = accountCounts(provider.accounts)
            return (
              <Link key={provider.provider} to="/connections/$providerId" params={{ providerId: provider.provider }} className="grid grid-cols-[1fr_auto_auto] items-center gap-6 border-b border-border px-5 py-4 transition-colors last:border-0 hover:bg-accent focus-visible:outline-2 focus-visible:outline-ring sm:grid-cols-[1fr_8rem_8rem_1rem]">
                <span className="flex min-w-0 items-center gap-3">
                  <ProviderIdentity provider={provider.provider} baseURL={provider.base_url} />
                  {provider.base_url && <span className="text-xs text-muted-foreground">{sdkLabel(provider.sdk)}</span>}
                </span>
                <span className="hidden text-right text-sm tabular text-muted-foreground sm:block">{counts.total}</span>
                <span className={cn('text-right text-sm whitespace-nowrap tabular', counts.active > 0 ? 'text-positive' : 'text-muted-foreground')}>{counts.active} active</span>
                <ChevronRight className="size-4 text-muted-foreground" />
              </Link>
            )
          })}
        {!providers.isPending && !providers.isError && rows.length === 0 && <p className="p-8 text-sm text-muted-foreground">{search ? 'No providers match that search' : 'No providers connected'}</p>}
      </div>
      <AddProviderDialog open={adding} onClose={() => setAdding(false)} />
    </div>
  )
}
