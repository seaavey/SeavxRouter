import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { KeyRound, Plus, Trash2 } from 'lucide-react'
import { PageHeader } from '@/components/page-header'
import { Card, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
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
    <>
      <PageHeader
        title="API keys"
        description="Tenant credentials that call the gateway, with their monthly limits."
        actions={
          <Button size="sm" onClick={() => setCreating(true)} disabled={!available}>
            <Plus className="size-4" />
            Create key
          </Button>
        }
      />

      <Card className="py-0">
        <CardContent className="px-0">
          {tenantKeys.isPending
            ? <div className="grid gap-2 p-4">{Array.from({ length: 4 }, (_, index) => <Skeleton key={index} className="h-9 w-full" />)}</div>
            : tenantKeys.isError
              ? (
                <Empty className="border-0 py-12">
                  <EmptyHeader>
                    <EmptyMedia variant="icon"><KeyRound /></EmptyMedia>
                    <EmptyTitle>Key management is disabled</EmptyTitle>
                    <EmptyDescription>{tenantKeys.error.message}Start the router with <code className="font-mono text-xs">-api-key</code> to enable tenant keys.</EmptyDescription>
                  </EmptyHeader>
                </Empty>
              )
              : rows.length === 0
                ? (
                  <Empty className="border-0 py-12">
                    <EmptyHeader>
                      <EmptyMedia variant="icon"><KeyRound /></EmptyMedia>
                      <EmptyTitle>No tenant keys yet</EmptyTitle>
                      <EmptyDescription>Create a key to let a client call the gateway with its own budget.</EmptyDescription>
                    </EmptyHeader>
                  </Empty>
                )
                : (
                  <Table>
                    <TableHeader>
                      <TableRow className="hover:bg-transparent">
                        {['Name', 'Prefix', 'Budget', 'Token limit', 'Created', 'Status', ''].map(head => (
                          <TableHead key={head} className="px-4 text-xs font-normal text-muted-foreground">{head}</TableHead>
                        ))}
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {rows.map(key => (
                        <TableRow key={key.id} className="group/row">
                          <TableCell className="px-4 py-2 font-medium">{key.name}</TableCell>
                          <TableCell className="px-4 py-2"><code className="font-mono text-xs text-muted-foreground">{key.prefix}…</code></TableCell>
                          <TableCell className="px-4 py-2 font-mono text-xs tabular text-muted-foreground">{format.budget(key.budget_usd)}</TableCell>
                          <TableCell className="px-4 py-2 font-mono text-xs tabular text-muted-foreground">{format.limit(key.limit_token)}</TableCell>
                          <TableCell className="px-4 py-2 font-mono text-xs tabular text-muted-foreground">{format.fullTime(key.created_at)}</TableCell>
                          <TableCell className="px-4 py-2">
                            <Badge variant={key.revoked_at ? 'outline' : 'secondary'} className="font-normal">
                              {key.revoked_at ? 'Revoked' : 'Active'}
                            </Badge>
                          </TableCell>
                          <TableCell className="px-4 py-2 text-right">
                            {!key.revoked_at && (
                              <Button
                                variant="ghost"
                                size="icon-xs"
                                aria-label={`Revoke ${key.name}`}
                                className="opacity-0 transition-opacity group-hover/row:opacity-100 focus-visible:opacity-100"
                                onClick={() => setRevoking(key)}
                              >
                                <Trash2 className="size-3.5" />
                              </Button>
                            )}
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
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
    </>
  )
}
