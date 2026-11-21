import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Separator } from '@/components/ui/separator'
import { format, statusLabel } from '@/lib/format'
import type { RequestLog } from '@/lib/api'

/**
 * One request in full. Bodies are no longer stored, so this shows what is kept:
 * who served it, how long it took, and the token breakdown.
 */
export function RequestDetail({ log, onClose }: { log: RequestLog | null; onClose: () => void }) {
  const facts = log ? [
    ['Result', `${statusLabel(log.status)} (${log.status})`],
    ['Latency', format.latency(log.duration_ms)],
    ['Provider', log.provider || '—'],
    ['Endpoint', log.endpoint || '—'],
    ['Account', log.account || '—'],
    ['Streamed', log.stream ? 'Yes' : 'No'],
  ] : []

  const tokens = log ? [
    ['Input', format.count(log.usage.prompt_tokens)],
    ['Output', format.count(log.usage.completion_tokens)],
    ['Thinking', log.usage.thinking_tokens > 0 ? format.count(log.usage.thinking_tokens) : '—'],
    ['Cached', log.usage.cached_tokens > 0 ? format.count(log.usage.cached_tokens) : '—'],
    ['Total', format.count(log.usage.total_tokens)],
  ] : []

  return (
    <Dialog open={Boolean(log)} onOpenChange={open => { if (!open) onClose() }}>
      <DialogContent className="gap-0 overflow-hidden p-0 sm:max-w-xl">
        <DialogHeader className="border-b border-border px-5 py-4">
          <DialogTitle>{log?.model || 'Request detail'}</DialogTitle>
          <DialogDescription>{log && format.fullTime(log.timestamp)}</DialogDescription>
        </DialogHeader>

        {log && (
          <>
            <dl className="grid grid-cols-2 gap-x-6 gap-y-4 px-5 py-4 sm:grid-cols-3">
              {facts.map(([label, value]) => (
                <div key={label} className="grid gap-0.5">
                  <dt className="text-xs text-muted-foreground">{label}</dt>
                  <dd className="truncate text-sm" title={value}>{value}</dd>
                </div>
              ))}
            </dl>

            <Separator />
            <div className="px-5 py-4">
              <h3 className="mb-3 text-xs text-muted-foreground">Tokens</h3>
              <dl className="grid grid-cols-2 gap-x-6 gap-y-3 sm:grid-cols-5">
                {tokens.map(([label, value]) => (
                  <div key={label} className="grid gap-0.5">
                    <dt className="text-xs text-muted-foreground">{label}</dt>
                    <dd className="text-sm tabular">{value}</dd>
                  </div>
                ))}
              </dl>
              <p className="mt-3 text-xs text-muted-foreground">
                Input excludes the cached prefix; cached tokens are counted separately.
              </p>
            </div>

            {log.error && (
              <>
                <Separator />
                <div className="px-5 py-4">
                  <h3 className="mb-2 text-xs text-muted-foreground">Error</h3>
                  <p className="rounded-lg bg-destructive/10 px-3 py-2 text-sm text-destructive">{log.error}</p>
                </div>
              </>
            )}
          </>
        )}
      </DialogContent>
    </Dialog>
  )
}
