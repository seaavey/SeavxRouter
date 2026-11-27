import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { KeyRound, Plus, Trash2 } from 'lucide-react'
import { PageHeader } from '@/components/page-header'
import { Card, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from '@/components/ui/empty'
import { Skeleton } from '@/components/ui/skeleton'
import { Badge } from '@/components/ui/badge'
import { CreateKeyDialog } from '@/features/keys/create-key-dialog'
import { RevokeKeyDialog } from '@/features/keys/revoke-key-dialog'
import { keys, tenantKeysQuery } from '@/lib/queries'
import { format } from '@/lib/format'
import { api, type TenantKey } from '@/lib/api'

export const Route = createFileRoute('/_app/keys')({ component: KeysPage })

function KeysPage() {
  const queryClient = useQueryClient()
  const tenantKeys = useQuery(tenantKeysQuery)
  const [creating, setCreating] = useState(false)
  const [revoking, setRevoking] = useState<TenantKey | null>(null)

  const rows = tenantKeys.data ?? []
  const available = !tenantKeys.isError

  const revoke = useMutation({
    mutationFn: (id: number) => api(`/dashboard/api/keys/${id}`, { method: 'DELETE' }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: keys.tenantKeys })
      setRevoking(null)
    },
  })

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title="API keys"
        description="Credentials your clients use to call the gateway, each with its own monthly limits."
        actions={
          <Button size="sm" onClick={() => setCreating(true)} disabled={!available}>
            <Plus className="size-4" />
            Create key
          </Button>
        }
      />

      <Card className="gap-0 py-0">
        <CardContent className="px-0">
          {tenantKeys.isPending
            ? <div className="grid gap-2 p-4">{Array.from({ length: 4 }, (_, index) => <Skeleton key={index} className="h-14 w-full" />)}</div>
            : tenantKeys.isError
              ? (
                <Empty className="py-12">
                  <EmptyHeader>
                    <EmptyMedia variant="icon"><KeyRound /></EmptyMedia>
                    <EmptyTitle>Key management is disabled</EmptyTitle>
                    <EmptyDescription>
                      Start the router with <code className="text-xs">-api-key</code> to manage tenant keys here.
                    </EmptyDescription>
                  </EmptyHeader>
                </Empty>
              )
              : rows.length === 0
                ? (
                  <Empty className="py-12">
                    <EmptyHeader>
                      <EmptyMedia variant="icon"><KeyRound /></EmptyMedia>
                      <EmptyTitle>No keys yet</EmptyTitle>
                      <EmptyDescription>Create a key so a client can call the gateway with its own budget.</EmptyDescription>
                    </EmptyHeader>
                  </Empty>
                )
                : (
                  <ul className="divide-y divide-border">
                    {rows.map(key => (
                      <li key={key.id} className="group/row flex flex-wrap items-center gap-x-6 gap-y-2 px-4 py-3">
                        <div className="grid min-w-40 flex-1">
                          <div className="flex items-center gap-2">
                            <span className="text-sm font-medium">{key.name}</span>
                            <Badge variant={key.revoked_at ? 'outline' : 'secondary'} className="font-normal">
                              {key.revoked_at ? 'Revoked' : 'Active'}
                            </Badge>
                          </div>
                          <code className="text-xs text-muted-foreground">{key.prefix}…</code>
                        </div>

                        <div className="grid gap-0.5">
                          <span className="text-xs text-muted-foreground">Monthly budget</span>
                          <span className="text-sm tabular">{format.budget(key.budget_usd)}</span>
                        </div>
                        <div className="grid gap-0.5">
                          <span className="text-xs text-muted-foreground">Token limit</span>
                          <span className="text-sm tabular">{format.limit(key.limit_token)}</span>
                        </div>
                        <div className="hidden grid-cols-1 gap-0.5 sm:grid">
                          <span className="text-xs text-muted-foreground">Created</span>
                          <span className="text-sm">{format.fullTime(key.created_at)}</span>
                        </div>

                        {!key.revoked_at && (
                          <Button
                            variant="ghost"
                            size="icon-sm"
                            aria-label={`Revoke ${key.name}`}
                            className="ml-auto text-muted-foreground opacity-0 transition-opacity group-hover/row:opacity-100 focus-visible:opacity-100"
                            onClick={() => setRevoking(key)}
                          >
                            <Trash2 className="size-4" />
                          </Button>
                        )}
                      </li>
                    ))}
                  </ul>
                )}
        </CardContent>
      </Card>

      <CreateKeyDialog open={creating} onClose={() => setCreating(false)} />
      <RevokeKeyDialog
        keyName={revoking?.name}
        pending={revoke.isPending}
        error={revoke.error?.message}
        onClose={() => setRevoking(null)}
        onConfirm={() => { if (revoking) revoke.mutate(revoking.id) }}
      />
    </div>
  )
}
