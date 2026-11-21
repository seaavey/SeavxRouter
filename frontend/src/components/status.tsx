import type { ReactNode } from 'react'
import { cn } from '@/lib/utils'

/** The one status vocabulary: a dot plus a quiet label, no pill chrome. */
type Tone = 'positive' | 'warning' | 'destructive' | 'signal' | 'muted'

const dotTone: Record<Tone, string> = {
  positive: 'bg-positive',
  warning: 'bg-warning',
  destructive: 'bg-destructive',
  signal: 'bg-signal',
  muted: 'bg-muted-foreground/60',
}

export function Dot({ tone = 'muted', pulse = false }: { tone?: Tone; pulse?: boolean }) {
  return (
    <span className="relative flex size-1.5 shrink-0">
      {pulse && <span className={cn('absolute inline-flex size-full animate-ping rounded-full opacity-60', dotTone[tone])} />}
      <span className={cn('relative inline-flex size-1.5 rounded-full', dotTone[tone])} />
    </span>
  )
}

export function StatusLabel({ children, tone = 'muted', pulse = false }: { children: ReactNode; tone?: Tone; pulse?: boolean }) {
  return (
    <span className="inline-flex items-center gap-2 text-xs text-muted-foreground">
      <Dot tone={tone} pulse={pulse} />
      {children}
    </span>
  )
}

/** A small mono figure for meta rows: tokens, latency, byte counts. */
export function Figure({ children, className }: { children: ReactNode; className?: string }) {
  return <span className={cn('font-mono text-xs tabular text-muted-foreground', className)}>{children}</span>
}
