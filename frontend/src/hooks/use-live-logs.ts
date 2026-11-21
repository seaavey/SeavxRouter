import { useEffect, useRef, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { keyPrefix, keys } from '@/lib/queries'

export type StreamStatus = 'connecting' | 'live' | 'reconnecting' | 'unavailable'

/**
 * Tails the router's SSE log feed and refreshes what it can stale. Bursts are
 * batched so a fast request stream does not refetch on every event.
 */
export function useLiveLogs(enabled = true) {
  const queryClient = useQueryClient()
  const [status, setStatus] = useState<StreamStatus>('connecting')
  const dirty = useRef(false)

  useEffect(() => {
    if (!enabled) return
    const source = new EventSource('/dashboard/api/logging/stream')

    source.addEventListener('ready', () => {
      setStatus('live')
      // The router does not replay events missed while disconnected.
      dirty.current = true
    })

    source.addEventListener('log', () => {
      dirty.current = true
    })

    source.onerror = () => {
      setStatus(source.readyState === EventSource.CLOSED ? 'unavailable' : 'reconnecting')
      void queryClient.invalidateQueries({ queryKey: keys.session })
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

  return status
}
