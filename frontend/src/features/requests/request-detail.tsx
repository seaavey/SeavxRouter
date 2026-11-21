import { useQuery } from '@tanstack/react-query'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Skeleton } from '@/components/ui/skeleton'
import { Separator } from '@/components/ui/separator'
import { logQuery } from '@/lib/queries'
import { format, statusLabel } from '@/lib/format'
import type { RequestLog } from '@/lib/api'

/** One request in full: what ran, who served it, and the raw body it returned. */
export function RequestDetail({ log, onClose }: { log: RequestLog | null; onClose: () => void }) {
  const detail = useQuery({ ...logQuery(log?.id ?? ''), enabled: Boolean(log) })
  const body = detail.data

  return (
    <Dialog open={Boolean(log)} onOpenChange={open => { if (!open) onClose() }}>
      <DialogContent className="max-h-[calc(100dvh-4rem)] gap-0 overflow-hidden p-0 sm:max-w-xl">
        <DialogHeader className="border-b border-border px-5 py-4">
          <DialogTitle>{log?.model || 'Request detail'}</DialogTitle>
          <DialogDescription>{log && format.fullTime(log.timestamp)}</DialogDescription>
        </DialogHeader>

        {log && (
          <>
            <dl className="grid grid-cols-2 gap-x-6 gap-y-4 px-5 py-4 sm:grid-cols-3">
              {[
                ['Result', statusLabel(log.status)],
                ['Latency', format.latency(log.duration_ms)],
                ['Provider', log.provider || '—'],
                ['Endpoint', log.endpoint || '—'],
                ['Total tokens', format.count(log.usage.total_tokens)],
                ['Input, output', `${format.count(log.usage.prompt_tokens)} · ${format.count(log.usage.completion_tokens)}`],
              ].map(([label, value]) => (
                <div key={label} className="grid gap-0.5">
                  <dt className="text-xs text-muted-foreground">{label}</dt>
                  <dd className="text-sm">{value}</dd>
                </div>
              ))}
            </dl>

            <Separator />
            <div className="max-h-[50vh] overflow-auto px-5 py-4">
              <h3 className="mb-2 text-sm font-medium">Response</h3>
              {detail.isPending
                ? <div className="grid gap-2"><Skeleton className="h-4 w-2/3" /><Skeleton className="h-20 w-full" /></div>
                : detail.isError
                  ? <p className="text-sm text-muted-foreground">{detail.error.message}</p>
                  : <Body value={body?.response} />}
            </div>
          </>
        )}
      </DialogContent>
    </Dialog>
  )
}

function Body({ value }: { value: unknown }) {
  const text = typeof value === 'string' ? value : value == null ? '' : JSON.stringify(value, null, 2)
  if (!text) return <p className="text-sm text-muted-foreground">No response body was recorded.</p>
  return (
    <pre className="overflow-auto rounded-lg bg-muted/50 p-3 font-mono text-xs leading-relaxed text-muted-foreground">
      {text}
    </pre>
  )
}
