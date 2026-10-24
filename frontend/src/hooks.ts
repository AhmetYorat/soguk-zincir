import { useCallback, useEffect, useRef, useState } from 'react'
import { ApiError, get } from './api'

export type Async<T> = {
  data: T | undefined
  error: ApiError | undefined
  loading: boolean
  reload: () => void
}

// useAsync runs fn when deps change and optionally polls. Earlier requests are
// aborted so a slow response never overwrites a newer one.
export function useAsync<T>(fn: (signal: AbortSignal) => Promise<T>, deps: unknown[], pollMs?: number): Async<T> {
  const [data, setData] = useState<T>()
  const [error, setError] = useState<ApiError>()
  const [loading, setLoading] = useState(true)
  const [tick, setTick] = useState(0)
  const fnRef = useRef(fn)
  fnRef.current = fn

  useEffect(() => {
    const ctrl = new AbortController()
    let first = true
    const run = () => {
      if (first) setLoading(true)
      fnRef
        .current(ctrl.signal)
        .then((d) => {
          setData(d)
          setError(undefined)
        })
        .catch((e) => {
          if ((e as Error).name !== 'AbortError') setError(e as ApiError)
        })
        .finally(() => {
          if (!ctrl.signal.aborted) setLoading(false)
          first = false
        })
    }
    run()
    const timer = pollMs ? setInterval(run, pollMs) : undefined
    return () => {
      ctrl.abort()
      if (timer) clearInterval(timer)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, tick, pollMs])

  const reload = useCallback(() => setTick((t) => t + 1), [])
  return { data, error, loading, reload }
}

// useAction wraps a mutation with pending/error state.
export function useAction<A extends unknown[], R>(fn: (...args: A) => Promise<R>) {
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<ApiError>()
  const run = useCallback(
    async (...args: A): Promise<R | undefined> => {
      setPending(true)
      setError(undefined)
      try {
        return await fn(...args)
      } catch (e) {
        setError(e as ApiError)
        return undefined
      } finally {
        setPending(false)
      }
    },
    [fn],
  )
  return { run, pending, error, clearError: () => setError(undefined) }
}

export function useOnline(): boolean {
  const [online, setOnline] = useState(navigator.onLine)
  useEffect(() => {
    const on = () => setOnline(true)
    const off = () => setOnline(false)
    window.addEventListener('online', on)
    window.addEventListener('offline', off)
    return () => {
      window.removeEventListener('online', on)
      window.removeEventListener('offline', off)
    }
  }, [])
  return online
}

// usePaged loads a keyset-paginated list and appends pages on demand.
export function usePaged<T>(path: string, deps: unknown[] = []) {
  const [extra, setExtra] = useState<T[]>([])
  const [cursor, setCursor] = useState<string | null | undefined>()
  const [moreLoading, setMoreLoading] = useState(false)
  const sep = path.includes('?') ? '&' : '?'
  const first = useAsync(
    async (signal) => {
      const page = await get<{ items: T[]; next_cursor: string | null }>(path, signal)
      setExtra([])
      setCursor(page.next_cursor)
      return page.items
    },
    [path, ...deps],
  )
  const more = async () => {
    if (!cursor) return
    setMoreLoading(true)
    try {
      const page = await get<{ items: T[]; next_cursor: string | null }>(path + sep + 'cursor=' + encodeURIComponent(cursor))
      setExtra((e) => [...e, ...page.items])
      setCursor(page.next_cursor)
    } finally {
      setMoreLoading(false)
    }
  }
  const items = first.data ? [...first.data, ...extra] : undefined
  return { state: { ...first, data: items } as Async<T[]>, cursor, more, moreLoading }
}
