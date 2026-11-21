import { useEffect, useRef, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { keyPrefix, keys } from '@/lib/queries'

/**
 * Tails the router's SSE log feed and refreshes what it can stale. Bursts are
 * batched so a fast request stream does not refetch on every event, and the
 * event count lets the UI react to something actually arriving.
 */
export function useLiveLogs(enabled = true) {
  const queryClient = useQueryClient()
  const [events, setEvents] = useState(0)
  const dirty = useRef(false)

  useEffect(() => {
    if (!enabled) return
    const source = new EventSource('/dashboard/api/logging/stream')

    source.addEventListener('ready', () => {
      // The router does not replay events missed while disconnected.
      dirty.current = true
      setEvents(count => count + 1)
    })

    source.addEventListener('log', () => {
      dirty.current = true
      setEvents(count => count + 1)
    })

    source.onerror = () => {
      if (source.readyState === EventSource.CLOSED) {
        void queryClient.invalidateQueries({ queryKey: keys.session })
      }
    }

    const timer = window.setInterval(() => {
      if (!dirty.current) return
      dirty.current = false
      void queryClient.invalidateQueries({ queryKey: keyPrefix.logs })
      void queryClient.invalidateQueries({ queryKey: keyPrefix.usage })
    }, 2000)

    return () => {
      source.close()
      window.clearInterval(timer)
    }
  }, [enabled, queryClient])

  return { events }
}

/**
 * Ids that were not in the previous render. Used to animate rows in, so a new
 * request visibly lands instead of the table quietly reshuffling.
 */
export function useNewRowIds(ids: string[]) {
  const seen = useRef<Set<string> | null>(null)
  const key = ids.join('\u0000')
  const [fresh, setFresh] = useState<Set<string>>(() => new Set())

  useEffect(() => {
    const next = new Set(ids)
    if (seen.current === null) {
      // First paint: everything is already there, nothing to announce.
      seen.current = next
      setFresh(new Set())
      return
    }
    const added = ids.filter(id => !seen.current!.has(id))
    seen.current = next
    if (added.length) setFresh(new Set(added))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key])

  return fresh
}
