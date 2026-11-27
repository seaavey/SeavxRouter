import { Link, createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { PageHeader } from '@/components/page-header'
import { Skeleton } from '@/components/ui/skeleton'
import { providersQuery } from '@/lib/queries'
import { accountCounts } from '@/features/connections/provider-data'
import { ProviderIdentity, providerName } from '@/features/connections/provider-identity'
import { ProviderModels } from '@/features/connections/provider-models'
import { format } from '@/lib/format'

export const Route = createFileRoute('/_app/connections/$providerId')({ component: ProviderPage })

function ProviderPage() {
  const { providerId } = Route.useParams()
  const providers = useQuery(providersQuery)
  const provider = providers.data?.find(row => row.provider === providerId)
  if (providers.isPending) return <Skeleton className="h-64 w-full" />
  if (providers.isError) return <p role="alert" className="text-sm text-destructive">{providers.error.message}</p>
  if (!provider) return <div className="grid gap-4"><h1 className="text-xl">Provider not found</h1><Link to="/connections" className="text-sm underline">Back to connections</Link></div>
  const counts = accountCounts(provider.accounts)

  return (
    <div className="flex flex-col gap-6">
      <nav aria-label="Provider breadcrumb" className="flex gap-2 text-sm text-muted-foreground"><Link to="/connections" className="hover:text-foreground">Connections</Link><span>/</span><span className="text-foreground"><ProviderIdentity provider={providerId} baseURL={provider.base_url} /></span></nav>
      <PageHeader title={providerName(providerId)} description={`${counts.total} accounts · ${counts.active} active · ${counts.unavailable} unavailable`} />
      <section className="overflow-hidden rounded-xl border border-border bg-card" aria-labelledby="accounts-title">
        <h2 id="accounts-title" className="border-b border-border px-5 py-4 text-sm font-medium">Accounts</h2>
        {(provider.accounts ?? []).map((account, index) => (
          <div key={`${account.account_id ?? account.email ?? index}-${index}`} className="flex flex-wrap items-center justify-between gap-3 border-b border-border px-5 py-4 last:border-0">
            <div className="grid min-w-0 gap-1">
              <span className="truncate text-sm">{account.email || account.account_id || `Account ${index + 1}`}</span>
              {(account.tier || account.access_token_expires_at) && <span className="text-xs text-muted-foreground">{[account.tier, account.access_token_expires_at && `Expires ${format.fullTime(account.access_token_expires_at)}`].filter(Boolean).join(' · ')}</span>}
            </div>
            <span className="text-xs text-muted-foreground">{account.status}</span>
          </div>
        ))}
        {!counts.total && <p className="p-5 text-sm text-muted-foreground">No accounts available</p>}
      </section>
      <ProviderModels provider={providerId} />
    </div>
  )
}
