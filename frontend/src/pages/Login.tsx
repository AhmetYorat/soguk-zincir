import { FormEvent, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { ApiError, login } from '../api'
import { ErrorBox } from '../components/ui'
import { Icon, IconName } from '../components/Icon'

const demo: [string, string, IconName][] = [
  ['depo@sogukzincir.test', 'Depo operatörü', 'pallet'],
  ['sofor@sogukzincir.test', 'Şoför', 'truck'],
  ['kalite@sogukzincir.test', 'Kalite sorumlusu', 'shield'],
  ['operasyon@sogukzincir.test', 'Operasyon müdürü', 'route'],
  ['market@sogukzincir.test', 'Müşteri (market)', 'store'],
  ['denetci@sogukzincir.test', 'Denetçi', 'scroll'],
]

// A stylised 24-hour temperature trace with one excursion above the dashed
// limit: the product in one picture.
function Trace() {
  return (
    <svg className="trace" viewBox="0 0 800 180" preserveAspectRatio="none" aria-hidden>
      <defs>
        <linearGradient id="tg" x1="0" x2="1">
          <stop offset="0" stopColor="#6aa9ef" />
          <stop offset=".55" stopColor="#4fd18f" />
          <stop offset=".72" stopColor="#f2bd57" />
          <stop offset=".8" stopColor="#ff8278" />
          <stop offset="1" stopColor="#4fd18f" />
        </linearGradient>
      </defs>
      <line className="t-limit" x1="0" x2="800" y1="58" y2="58" />
      <path
        className="t-line"
        d="M0 120 C40 110 70 130 110 118 S170 104 210 116 S280 128 320 112 S390 100 430 114 S490 126 520 110 C545 96 560 30 590 32 C615 34 620 96 650 112 S720 124 760 114 L800 116"
      />
    </svg>
  )
}

export default function Login() {
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [show, setShow] = useState(false)
  const [error, setError] = useState<ApiError>()
  const [pending, setPending] = useState(false)
  const navigate = useNavigate()

  async function submit(e: FormEvent) {
    e.preventDefault()
    setPending(true)
    setError(undefined)
    try {
      await login(email, password)
      navigate('/', { replace: true })
    } catch (err) {
      setError(err as ApiError)
    } finally {
      setPending(false)
    }
  }

  return (
    <div className="login">
      <section className="login-art">
        <div className="brand big" style={{ color: '#fff' }}>
          <span className="logo" aria-hidden>
            <Icon name="snow" size={24} />
          </span>
          SoğukZincir
        </div>
        <div>
          <Trace />
          <div className="trace-caption">
            <span>son 24 saat · soğuk oda 002</span>
            <span>— — üst limit 8 °C</span>
          </div>
        </div>
        <div>
          <h2>Soğuk zincir kırıldığında ürünün akıbeti kanıtlanabilir olsun.</h2>
          <p>Depo ve araç sensörleri, parti izlenebilirliği, HACCP kayıtları ve değiştirilemez sapma raporları tek yerde.</p>
          <div className="login-stats">
            <div><b>2 000</b><span>sensör · 30 sn</span></div>
            <div><b>&lt; 1 sn</b><span>geri çağırma izi</span></div>
            <div><b>SHA-256</b><span>rapor zinciri</span></div>
          </div>
        </div>
      </section>
      <section className="login-pane">
        <form className="login-card" onSubmit={submit} noValidate>
          <div>
            <div className="eyebrow">Giriş</div>
            <h1>Hesabınıza girin</h1>
            <p className="muted" style={{ marginTop: 6 }}>Gıda dağıtımı soğuk zincir izleme ve uyum sistemi</p>
          </div>
          <label className="field">
            <span>E-posta</span>
            <input type="email" autoComplete="username" inputMode="email" value={email} onChange={(e) => setEmail(e.target.value)} required />
          </label>
          <label className="field">
            <span>Parola</span>
            <span className="row">
              <input style={{ flex: 1 }} type={show ? 'text' : 'password'} autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} required />
              <button type="button" className="btn ghost small" onClick={() => setShow(!show)} aria-pressed={show}>
                {show ? 'Gizle' : 'Göster'}
              </button>
            </span>
          </label>
          {error && <ErrorBox error={error} />}
          <button className="btn primary wide" disabled={pending}>
            {pending ? <><span className="spinner" aria-hidden /> Giriş yapılıyor…</> : <><Icon name="lock" /> Giriş yap</>}
          </button>
          <div className="demo">
            <span className="muted small">Demo hesapları — parola <code>Demo1234!</code></span>
            <div className="demo-grid">
              {demo.map(([mail, label, icon]) => (
                <button
                  type="button"
                  key={mail}
                  className="btn ghost small"
                  onClick={() => {
                    setEmail(mail)
                    setPassword('Demo1234!')
                  }}
                >
                  <Icon name={icon} size={16} /> {label}
                </button>
              ))}
            </div>
          </div>
        </form>
      </section>
    </div>
  )
}
