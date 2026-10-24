import { describe, expect, it } from 'vitest'
import { memoryKV, OfflineQueue, sync, SyncResult } from './offline'

describe('OfflineQueue', () => {
  it('keeps events across instances (survives a reload)', () => {
    const kv = memoryKV()
    new OfflineQueue(kv).add({ shipment_id: 1, kind: 'temperature_check', temp_cc: 410 })
    expect(new OfflineQueue(kv).list()).toHaveLength(1)
  })

  it('stamps a UUID and the device time when queued', () => {
    const q = new OfflineQueue(memoryKV())
    const ev = q.add({ shipment_id: 1, kind: 'delivery' })
    expect(ev.client_event_id).toMatch(/^[0-9a-f-]{36}$/)
    expect(Date.parse(ev.recorded_at)).toBeLessThanOrEqual(Date.now())
  })

  it('removes only events the server answered', async () => {
    const q = new OfflineQueue(memoryKV())
    const a = q.add({ shipment_id: 1, kind: 'temperature_check', temp_cc: 400 })
    q.add({ shipment_id: 1, kind: 'delivery' })
    await sync(q, async () => [{ client_event_id: a.client_event_id, outcome: 'applied' }])
    expect(q.list()).toHaveLength(1)
    expect(q.list()[0].kind).toBe('delivery')
  })

  it('keeps the queue when the network fails', async () => {
    const q = new OfflineQueue(memoryKV())
    q.add({ shipment_id: 1, kind: 'delivery' })
    await expect(sync(q, async () => { throw new Error('offline') })).rejects.toThrow()
    expect(q.list()).toHaveLength(1)
  })

  it('treats duplicate and rejected answers as final', async () => {
    const q = new OfflineQueue(memoryKV())
    const a = q.add({ shipment_id: 1, kind: 'delivery' })
    const b = q.add({ shipment_id: 2, kind: 'delivery' })
    const res: SyncResult[] = [
      { client_event_id: a.client_event_id, outcome: 'duplicate_delivery' },
      { client_event_id: b.client_event_id, outcome: 'rejected', reason: 'shipment not assigned to you' },
    ]
    await sync(q, async () => res)
    expect(q.list()).toHaveLength(0)
  })

  it('does nothing with an empty queue', async () => {
    let called = false
    await sync(new OfflineQueue(memoryKV()), async () => { called = true; return [] })
    expect(called).toBe(false)
  })
})
