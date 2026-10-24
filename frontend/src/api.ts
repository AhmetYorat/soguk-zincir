// Thin fetch wrapper: bearer token, transparent refresh on 401, the API's
// error format turned into ApiError, optional Idempotency-Key.

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public details?: Record<string, unknown>,
  ) {
    super(message)
  }
}

export type User = {
  id: number
  email: string
  full_name: string
  role: Role
  customer_id: number | null
  permissions: string[]
}

export type Role =
  | 'warehouse_operator'
  | 'driver'
  | 'quality_manager'
  | 'operations_manager'
  | 'customer'
  | 'auditor'

export const roleLabel: Record<Role, string> = {
  warehouse_operator: 'Depo operatörü',
  driver: 'Şoför',
  quality_manager: 'Kalite sorumlusu',
  operations_manager: 'Operasyon müdürü',
  customer: 'Müşteri (market)',
  auditor: 'Denetçi',
}

type Session = { access: string; refresh: string; user: User }

const KEY = 'sz.session'
let session: Session | null = load()
const listeners = new Set<(u: User | null) => void>()

function load(): Session | null {
  try {
    const raw = localStorage.getItem(KEY)
    return raw ? (JSON.parse(raw) as Session) : null
  } catch {
    return null
  }
}

function save(s: Session | null) {
  session = s
  try {
    if (s) localStorage.setItem(KEY, JSON.stringify(s))
    else localStorage.removeItem(KEY)
  } catch {
    /* storage may be unavailable; the session then lives in memory only */
  }
  listeners.forEach((l) => l(s?.user ?? null))
}

export function currentUser(): User | null {
  return session?.user ?? null
}

export function onAuthChange(fn: (u: User | null) => void): () => void {
  listeners.add(fn)
  return () => listeners.delete(fn)
}

export function can(perm: string): boolean {
  return !!session?.user.permissions.includes(perm)
}

async function parse(res: Response): Promise<unknown> {
  if (res.status === 204) return null
  const text = await res.text()
  if (!text) return null
  try {
    return JSON.parse(text)
  } catch {
    return text
  }
}

function toError(status: number, body: unknown): ApiError {
  const e = (body as { error?: { code: string; message: string; details?: Record<string, unknown> } })?.error
  if (e) return new ApiError(status, e.code, e.message, e.details)
  return new ApiError(status, 'http_' + status, 'Beklenmeyen yanıt (' + status + ')')
}

export async function login(email: string, password: string): Promise<User> {
  const res = await fetch('/api/v1/auth/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ email, password }),
  })
  const body = (await parse(res)) as { access_token: string; refresh_token: string; user: User }
  if (!res.ok) throw toError(res.status, body)
  save({ access: body.access_token, refresh: body.refresh_token, user: body.user })
  return body.user
}

export async function logout() {
  const s = session
  save(null)
  if (s) {
    await fetch('/api/v1/auth/logout', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ refresh_token: s.refresh }),
    }).catch(() => undefined)
  }
}

let refreshing: Promise<boolean> | null = null

async function refresh(): Promise<boolean> {
  if (!session) return false
  // share one refresh between concurrent 401s: the token rotates
  refreshing ??= (async () => {
    const res = await fetch('/api/v1/auth/refresh', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ refresh_token: session!.refresh }),
    })
    if (!res.ok) {
      save(null)
      return false
    }
    const body = (await res.json()) as { access_token: string; refresh_token: string; user: User }
    save({ access: body.access_token, refresh: body.refresh_token, user: body.user })
    return true
  })().finally(() => {
    refreshing = null
  })
  return refreshing
}

export type RequestOptions = { idempotencyKey?: string; signal?: AbortSignal }

export async function api<T = unknown>(
  method: string,
  path: string,
  body?: unknown,
  opts: RequestOptions = {},
  retried = false,
): Promise<T> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json' }
  if (session) headers.Authorization = 'Bearer ' + session.access
  if (opts.idempotencyKey) headers['Idempotency-Key'] = opts.idempotencyKey
  let res: Response
  try {
    res = await fetch('/api/v1' + path, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: opts.signal,
    })
  } catch (err) {
    if ((err as Error).name === 'AbortError') throw err
    throw new ApiError(0, 'network', 'Sunucuya ulaşılamıyor. Bağlantınızı kontrol edin.')
  }
  if (res.status === 401 && !retried && session && (await refresh())) {
    return api<T>(method, path, body, opts, true)
  }
  const data = await parse(res)
  if (!res.ok) throw toError(res.status, data)
  return data as T
}

export const get = <T>(path: string, signal?: AbortSignal) => api<T>('GET', path, undefined, { signal })
export const post = <T>(path: string, body?: unknown, idempotent = true) =>
  api<T>('POST', path, body, idempotent ? { idempotencyKey: crypto.randomUUID() } : {})

export type Page<T> = { items: T[]; next_cursor: string | null }

// ---- formatting helpers ----

export const fmtC = (cc: number | null | undefined) => (cc == null ? '—' : (cc / 100).toFixed(1) + ' °C')
export const fmtTemp = (c: number | null | undefined) => (c == null ? '—' : c.toFixed(1) + ' °C')

export function fmtTime(s: string | null | undefined): string {
  if (!s) return '—'
  return new Date(s).toLocaleString('tr-TR', { dateStyle: 'short', timeStyle: 'short' })
}

export function fmtDuration(seconds: number): string {
  if (!seconds) return '0 dk'
  const h = Math.floor(seconds / 3600)
  const m = Math.round((seconds % 3600) / 60)
  return h ? `${h} sa ${m} dk` : `${m} dk`
}

export const fmtKurus = (k: number) =>
  (k / 100).toLocaleString('tr-TR', { style: 'currency', currency: 'TRY' })
