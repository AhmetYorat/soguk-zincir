// k6 load test. Runs inside the compose network (make load) for 2 minutes:
//
//   ingest     135 signed device messages/s  (2x the real 2 000 sensors / 30 s)
//   read_24h    40 req/s  last-24-hours series of a random sensor
//   dashboard   20 req/s  live counters + live alarms
//   trace        5 req/s  recursive recall query
//
// The simulator is stopped meanwhile so device sequence numbers do not clash.
import http from 'k6/http'
import crypto from 'k6/crypto'
import { check } from 'k6'

const API = __ENV.API_URL || 'http://localhost:8080'
const SECRET = __ENV.DEVICE_PROVISION_SECRET
const SENSORS = Number(__ENV.SENSOR_COUNT || 2000)
const DURATION = __ENV.DURATION || '2m'

const rate = (r, vus) => ({ executor: 'constant-arrival-rate', rate: r, timeUnit: '1s', duration: DURATION, preAllocatedVUs: vus, maxVUs: vus * 4 })

export const options = {
  scenarios: {
    ingest: { ...rate(135, 40), exec: 'ingest' },
    read_24h: { ...rate(40, 30), exec: 'read24h' },
    dashboard: { ...rate(20, 10), exec: 'dashboard' },
    trace: { ...rate(5, 5), exec: 'trace' },
  },
  summaryTrendStats: ['avg', 'med', 'p(95)', 'p(99)', 'max'],
  thresholds: {
    'http_req_failed': ['rate<0.01'],
    'http_req_duration{scenario:ingest}': ['p(95)<100'],
    'http_req_duration{scenario:read_24h}': ['p(95)<100'],
    'http_req_duration{scenario:dashboard}': ['p(95)<200'],
    'http_req_duration{scenario:trace}': ['p(95)<1000'],
    'http_req_failed{scenario:ingest}': ['rate<0.01'],
    'http_req_failed{scenario:read_24h}': ['rate<0.01'],
    'http_req_failed{scenario:dashboard}': ['rate<0.01'],
    'http_req_failed{scenario:trace}': ['rate<0.01'],
  },
}

export function setup() {
  const res = http.post(`${API}/api/v1/auth/login`, JSON.stringify({ email: 'kalite@sogukzincir.test', password: 'Demo1234!' }), {
    headers: { 'Content-Type': 'application/json' },
  })
  const token = res.json('access_token')
  const lots = http.get(`${API}/api/v1/lots?q=LOT-TRACE-001`, { headers: { Authorization: `Bearer ${token}` } })
  return { token, traceLot: lots.json('items.0.id') }
}

const auth = (d) => ({ headers: { Authorization: `Bearer ${d.token}` } })

// device keys are derived exactly like internal/domain/devicesig.DeriveKey
const keys = {}
function deviceKey(code) {
  if (!keys[code]) keys[code] = crypto.hmac('sha256', SECRET, 'device-key/v1/' + code, 'binary')
  return keys[code]
}

let seq = 0
export function ingest() {
  const n = 1 + Math.floor(Math.random() * SENSORS)
  const code = 'S-' + String(n).padStart(5, '0')
  const body = JSON.stringify({ battery_pct: 80, readings: [{ ts: new Date().toISOString(), temp_cc: 350 + Math.floor(Math.random() * 80), humidity_pm: 840 }] })
  // per-VU monotonic sequence anchored to wall-clock microseconds
  seq = Math.max(seq + 1, Date.now() * 1000 + __VU)
  const s = String(seq)
  const ts = String(Date.now())
  const msg = ['v1', code, s, ts, crypto.sha256(body, 'hex')].join('\n')
  const sig = crypto.hmac('sha256', deviceKey(code), msg, 'hex')
  const res = http.post(`${API}/api/v1/ingest/readings`, body, {
    headers: { 'Content-Type': 'application/json', 'X-Device-Id': code, 'X-Seq': s, 'X-Timestamp': ts, 'X-Signature': sig },
  })
  check(res, { 'ingest 202': (r) => r.status === 202 })
}

export function read24h(d) {
  const id = 1 + Math.floor(Math.random() * SENSORS)
  const res = http.get(`${API}/api/v1/sensors/${id}/readings`, { ...auth(d), tags: { name: 'readings_24h' } })
  check(res, { 'readings 200': (r) => r.status === 200 })
}

export function dashboard(d) {
  const res = http.batch([
    ['GET', `${API}/api/v1/live`, null, auth(d)],
    ['GET', `${API}/api/v1/alarms?status=live&limit=8`, null, auth(d)],
  ])
  check(res[0], { 'live 200': (r) => r.status === 200 })
}

export function trace(d) {
  const res = http.get(`${API}/api/v1/trace/lots/${d.traceLot}`, auth(d))
  check(res, { 'trace 200': (r) => r.status === 200 })
}
