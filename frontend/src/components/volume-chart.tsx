import { Area, AreaChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import { bucketsFor, format } from '@/lib/format'
import type { Usage } from '@/lib/api'
import type { Range } from '@/lib/queries'

/**
 * Tokens handled per bucket, drawn as one area so the shape reads at a glance.
 * A column chart asks the reader to compare heights; a line shows the traffic
 * curve itself, and the tooltip carries the numbers a height can only hint at.
 */
export function VolumeChart({ usage, range }: { usage: Usage; range: Range }) {
  const buckets = bucketsFor(usage.data, range)
  const peak = Math.max(...buckets.map(bucket => bucket.tokens), 1)
  const total = buckets.reduce((sum, bucket) => sum + bucket.tokens, 0)
  const busiest = buckets.reduce((best, bucket) => (bucket.tokens > best.tokens ? bucket : best), buckets[0])
  const peakLabel = range.group === 'day' ? 'Busiest day' : range.group === 'hour' ? 'Busiest hour' : 'Busiest minute'
  // Recharts draws every tick it is handed, so thin them out ourselves.
  const tickInterval = Math.max(0, Math.ceil(buckets.length / 8) - 1)

  return (
    <div className="px-6 pt-5 pb-5">
      <div className="h-52 w-full">
        <ResponsiveContainer width="100%" height="100%">
          <AreaChart data={buckets} margin={{ top: 4, right: 28, bottom: 0, left: 0 }}>
            <defs>
              <linearGradient id="token-fill" x1="0" y1="0" x2="0" y2="1">
                <stop offset="0%" stopColor="var(--foreground)" stopOpacity={0.16} />
                <stop offset="100%" stopColor="var(--foreground)" stopOpacity={0} />
              </linearGradient>
            </defs>
            <CartesianGrid vertical={false} stroke="var(--border)" strokeDasharray="3 3" />
            <XAxis
              dataKey="label"
              interval={tickInterval}
              tickLine={false}
              axisLine={false}
              minTickGap={16}
              tick={{ fill: 'var(--muted-foreground)', fontSize: 11 }}
              tickMargin={8}
            />
            <YAxis
              width={46}
              domain={[0, peak]}
              tickLine={false}
              axisLine={false}
              tickCount={4}
              tick={{ fill: 'var(--muted-foreground)', fontSize: 11 }}
              tickFormatter={(value: number) => format.compact(value)}
            />
            <Tooltip
              cursor={{ stroke: 'var(--border)', strokeWidth: 1 }}
              content={({ active, payload }) => {
                if (!active || !payload?.length) return null
                const bucket = payload[0].payload as (typeof buckets)[number]
                return (
                  <div className="grid gap-1.5 rounded-lg border border-border bg-popover px-3 py-2 text-xs shadow-popover">
                    <span className="font-medium">{bucket.label}</span>
                    <span className="flex items-center justify-between gap-6 tabular">
                      <span className="text-muted-foreground">Tokens</span>
                      <span>{format.count(bucket.tokens)}</span>
                    </span>
                    <span className="flex items-center justify-between gap-6 tabular">
                      <span className="text-muted-foreground">Requests</span>
                      <span>{format.count(bucket.requests)}</span>
                    </span>
                  </div>
                )
              }}
            />
            <Area
              type="monotone"
              dataKey="tokens"
              stroke="var(--foreground)"
              strokeWidth={1.5}
              fill="url(#token-fill)"
              dot={false}
              activeDot={{ r: 3, fill: 'var(--foreground)', strokeWidth: 0 }}
              isAnimationActive={false}
            />
          </AreaChart>
        </ResponsiveContainer>
      </div>
      <p className="mt-3 text-xs text-muted-foreground">
        {format.compact(total)} tokens in total · {peakLabel.toLowerCase()} was {busiest?.label} with {format.compact(busiest?.tokens ?? 0)}
      </p>
    </div>
  )
}
