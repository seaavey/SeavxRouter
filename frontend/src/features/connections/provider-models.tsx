import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Copy, Plus, Trash2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { api } from '@/lib/api'
import { keys } from '@/lib/queries'

type ManagedModel = { id: string; disabled?: boolean }

export function ProviderModels({ provider }: { provider: string }) {
  const client = useQueryClient()
  const [search, setSearch] = useState('')
  const [adding, setAdding] = useState(false)
  const [model, setModel] = useState('')
  const [removing, setRemoving] = useState<string | null>(null)
  const [feedback, setFeedback] = useState('')
  const path = `/dashboard/api/connections/${encodeURIComponent(provider)}/models`
  const queryKey = ['connection-models', provider] as const
  const query = useQuery({ queryKey, queryFn: ({ signal }) => api<{ data: ManagedModel[] }>(path, { signal }).then(value => value.data ?? []) })
  const refresh = async () => {
    await Promise.all([client.invalidateQueries({ queryKey }), client.invalidateQueries({ queryKey: keys.models })])
  }
  const add = useMutation({
    mutationFn: () => api(path, { method: 'POST', body: JSON.stringify({ model: model.trim() }) }),
    onSuccess: async () => { await refresh(); setAdding(false); setModel(''); setFeedback('Model verified and added') },
  })
  const remove = useMutation({
    mutationFn: (id: string) => api(path, { method: 'DELETE', body: JSON.stringify({ model: id }) }),
    onSuccess: async () => { await refresh(); setRemoving(null); setFeedback('Model removed from discovery and routing') },
  })
  const copy = async (id: string) => {
    try { await navigator.clipboard.writeText(`${provider}/${id}`); setFeedback('Model ID copied') }
    catch { setFeedback('Could not copy model ID') }
  }
  const visible = (query.data ?? []).filter(row => !row.disabled && row.id.toLowerCase().includes(search.trim().toLowerCase()))

  return (
    <section className="overflow-hidden rounded-xl border border-border bg-card" aria-labelledby="models-title">
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-border px-5 py-4">
        <h2 id="models-title" className="text-sm font-medium">Models</h2>
        <Button size="sm" onClick={() => { add.reset(); setAdding(true) }}><Plus className="size-4" />Add model</Button>
      </div>
      <div className="border-b border-border px-5 py-3"><Input aria-label="Search provider models" placeholder="Search models" value={search} onChange={event => setSearch(event.target.value)} className="max-w-72" /></div>
      {feedback && <p role="status" className="px-5 py-3 text-xs text-muted-foreground">{feedback}</p>}
      {query.isError && <p role="alert" className="p-5 text-sm text-destructive">{query.error.message}</p>}
      {query.isPending ? <div className="grid gap-3 p-5">{Array.from({ length: 5 }, (_, index) => <Skeleton key={index} className="h-8 w-full" />)}</div>
        : visible.map(row => (
          <div key={row.id} className="flex items-center gap-2 border-b border-border px-5 py-3 last:border-0">
            <span className="min-w-0 flex-1 truncate text-sm" title={row.id}>{row.id}</span>
            <Button variant="ghost" size="icon-sm" aria-label={`Copy ${row.id}`} onClick={() => copy(row.id)}><Copy className="size-4" /></Button>
            <Button variant="ghost" size="icon-sm" aria-label={`Delete ${row.id}`} onClick={() => { remove.reset(); setRemoving(row.id) }}><Trash2 className="size-4" /></Button>
          </div>
        ))}
      {!query.isPending && !query.isError && !visible.length && <p className="p-5 text-sm text-muted-foreground">{search ? 'No models match that search' : 'No models available'}</p>}
      <Dialog open={adding} onOpenChange={open => { if (!add.isPending) setAdding(open) }}>
        <DialogContent>
          <DialogHeader><DialogTitle>Add model</DialogTitle><DialogDescription>A chat request will verify this model before it is saved. The provider may charge for this test.</DialogDescription></DialogHeader>
          <form className="grid gap-4" onSubmit={event => { event.preventDefault(); add.mutate() }}>
            <label className="grid gap-2 text-sm">Model ID<Input value={model} onChange={event => setModel(event.target.value)} required disabled={add.isPending} placeholder="Upstream model ID" /></label>
            {add.isError && <p role="alert" className="text-sm text-destructive">{add.error.message}</p>}
            <Button type="submit" disabled={!model.trim() || add.isPending}>{add.isPending ? 'Verifying…' : 'Verify and add'}</Button>
          </form>
        </DialogContent>
      </Dialog>
      <Dialog open={removing !== null} onOpenChange={open => { if (!open && !remove.isPending) setRemoving(null) }}>
        <DialogContent>
          <DialogHeader><DialogTitle>Delete model?</DialogTitle><DialogDescription>{removing} will disappear from the model list and stop accepting requests. The upstream model and provider account will not be deleted.</DialogDescription></DialogHeader>
          {remove.isError && <p role="alert" className="text-sm text-destructive">{remove.error.message}</p>}
          <div className="flex justify-end gap-2"><Button variant="outline" disabled={remove.isPending} onClick={() => setRemoving(null)}>Cancel</Button><Button variant="destructive" disabled={remove.isPending} onClick={() => { if (removing) remove.mutate(removing) }}>{remove.isPending ? 'Deleting…' : 'Delete model'}</Button></div>
        </DialogContent>
      </Dialog>
    </section>
  )
}
