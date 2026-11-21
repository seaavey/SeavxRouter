import { useQuery } from '@tanstack/react-query'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Skeleton } from '@/components/ui/skeleton'
import { Separator } from '@/components/ui/separator'
import { Figure } from '@/components/status'
import { logQuery } from '@/lib/queries'
import { format, statusTone } from '@/lib/format'
import type { RequestLog } from '@/lib/api'

/** One request in full: metadata first, then the raw bodies the router stored. */
export function RequestDetail({ log, onClose }: { log: RequestLog | null; onClose: () => void }) {
  const detail = useQuery({ ...logQuery(log?.id ?? ''), enabled: Boolean(log) })
  const body = detail.data

  return (
    <Dialog open={Boolean(log)} onOpenChange={open => { if (!open) onClose() }}>
      <DialogContent className="max-h-[calc(100dvh-4rem)] gap-0 overflow-hidden p-0 sm:max-w-2xl">
        <DialogHeader className="border-b border-border px-5 py-4">
          <DialogTitle className="font-mono text-sm">{log?.model || 'Request'}</DialogTitle>
          <DialogDescription>
            {log && `${log.endpoint || 'endpoint not recorded'} · ${format.fullTime(log.timestamp)}`}
          </DialogDescription>
        </DialogHeader>

        {log && (
          <>
            <dl className="grid grid-cols-2 gap-px bg-border sm:grid-cols-4">
              {[
                ['Status', String(log.status)],
                ['Latency', format.latency(log.duration_ms)],
                ['Tokens', format.count(log.usage.total_tokens)],
                ['Provider', log.provider || '—'],
              ].map(([label, value]) => (
                <div key={label} className="bg-popover px-4 py-3">
                  <dt className="text-[11px] text-muted-foreground">{label}</dt>
                  <dd className="mt-1 font-mono text-sm tabular">{value}</dd>
                </div>
              ))}
            </dl>

            <div className="max-h-[52vh] overflow-auto px-5 py-4">
              <div className="mb-3 flex items-center gap-3 text-[11px] text-muted-foreground">
                <Figure>prompt {format.count(log.usage.prompt_tokens)}</Figure>
                <Figure>completion {format.count(log.usage.completion_tokens)}</Figure>
                {log.usage.cached_tokens > 0 && <Figure>cached {format.count(log.usage.cached_tokens)}</Figure>}
                {log.status >= 400 && <Figure className="text-warning">{statusTone(log.status)}</Figure>}
              </div>
              <Separator className="mb-3" />
              {detail.isPending
                ? <div className="grid gap-2"><Skeleton className="h-4 w-2/3" /><Skeleton className="h-24 w-full" /></div>
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
  if (!text) return <p className="text-sm text-muted-foreground">No response body was recorded for this request.</p>
  return (
    <pre className="overflow-auto rounded-md bg-background p-3 font-mono text-xs leading-relaxed text-muted-foreground">
      {text}
    </pre>
  )
}
