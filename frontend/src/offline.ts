// Offline queue for the driver app. Events are written to local storage the
// moment the driver taps a button, with a client-generated UUID and the
// phone's clock. Sync sends the queue; the server answers per event and is
// idempotent on the UUID, so a sync that dies half way can simply be repeated.

export type DriverEvent = {
  client_event_id: string
  shipment_id: number
  kind: 'delivery' | 'temperature_check'
  recorded_at: string
  temp_cc?: number
  receiver_name?: string
  note?: string
}

export type SyncResult = {
  client_event_id: string
  outcome: 'applied' | 'already_synced' | 'duplicate_delivery' | 'rejected'
  reason?: string
}

export interface KV {
  getItem(k: string): string | null
  setItem(k: string, v: string): void
}

export class OfflineQueue {
  constructor(private store: KV, private key = 'sz.driver.queue') {}

  list(): DriverEvent[] {
    try {
      return JSON.parse(this.store.getItem(this.key) ?? '[]') as DriverEvent[]
    } catch {
      return []
    }
  }

  private write(evs: DriverEvent[]) {
    this.store.setItem(this.key, JSON.stringify(evs))
  }

  add(ev: Omit<DriverEvent, 'client_event_id' | 'recorded_at'> & Partial<DriverEvent>): DriverEvent {
    const full: DriverEvent = {
      client_event_id: ev.client_event_id ?? crypto.randomUUID(),
      recorded_at: ev.recorded_at ?? new Date().toISOString(),
      ...ev,
    } as DriverEvent
    this.write([...this.list(), full])
    return full
  }

  // apply removes every event the server gave a final answer for. All four
  // outcomes are final: retrying a rejected event would be rejected again.
  apply(results: SyncResult[]) {
    const done = new Set(results.map((r) => r.client_event_id))
    this.write(this.list().filter((e) => !done.has(e.client_event_id)))
  }
}

// sync sends the queue in one request. Network errors leave the queue intact.
export async function sync(
  queue: OfflineQueue,
  send: (events: DriverEvent[]) => Promise<SyncResult[]>,
): Promise<{ sent: number; results: SyncResult[] }> {
  const events = queue.list()
  if (events.length === 0) return { sent: 0, results: [] }
  const results = await send(events)
  queue.apply(results)
  return { sent: events.length, results }
}

export const memoryKV = (): KV => {
  const m = new Map<string, string>()
  return { getItem: (k) => m.get(k) ?? null, setItem: (k, v) => void m.set(k, v) }
}

// safeLocalStorage falls back to memory when storage is blocked.
export function safeLocalStorage(): KV {
  try {
    localStorage.setItem('sz.probe', '1')
    localStorage.removeItem('sz.probe')
    return localStorage
  } catch {
    return memoryKV()
  }
}
