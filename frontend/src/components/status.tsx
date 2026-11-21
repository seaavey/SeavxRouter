import type { ReactNode } from 'react'
import { cn } from '@/lib/utils'

/** Status vocabulary: a dot carries the state, the label stays neutral. */
type Tone = 'positive' | 'warning' | 'negative' | 'muted'

const dotTone: Record<Tone, string> = {
  positive: 'bg-positive',
  warning: 'bg-warning',
  negative: 'bg-negative',
  muted: 'bg-muted-foreground/50',
}

export function Dot({ tone = 'muted' }: { tone?: Tone }) {
  return <span className={cn('size-1.5 shrink-0 rounded-full', dotTone[tone])} aria-hidden />
}

export function StatusLabel({ children, tone = 'muted' }: { children: ReactNode; tone?: Tone }) {
  return (
    <span className="inline-flex items-center gap-2 text-xs whitespace-nowrap text-muted-foreground">
      <Dot tone={tone} />
      {children}
    </span>
  )
}

/** Quiet meta text for secondary detail: token counts, byte sizes, timestamps. */
export function Meta({ children, className }: { children: ReactNode; className?: string }) {
  return <span className={cn('text-xs tabular text-muted-foreground', className)}>{children}</span>
}
