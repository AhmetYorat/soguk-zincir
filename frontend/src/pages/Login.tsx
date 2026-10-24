import { FormEvent, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { ApiError, login } from '../api'
import { ErrorBox } from '../components/ui'

const demo = [
  ['depo@sogukzincir.test', 'Depo operatörü'],
  ['sofor@sogukzincir.test', 'Şoför'],
  ['kalite@sogukzincir.test', 'Kalite sorumlusu'],
  ['operasyon@sogukzincir.test', 'Operasyon müdürü'],
  ['market@sogukzincir.test', 'Müşteri (market)'],
  ['denetci@sogukzincir.test', 'Denetçi'],
]

export default function Login() {
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
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
      <form className="login-card" onSubmit={submit}>
        <div className="brand big">
          <span className="logo" aria-hidden>
            ❄
          </span>
          SoğukZincir
        </div>
        <p className="muted">Gıda dağıtımı soğuk zincir izleme ve uyum sistemi</p>
        <label className="field">
          <span>E-posta</span>
          <input type="email" autoComplete="username" value={email} onChange={(e) => setEmail(e.target.value)} required />
        </label>
        <label className="field">
          <span>Parola</span>
          <input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} required />
        </label>
        {error && <ErrorBox error={error} />}
        <button className="btn primary wide" disabled={pending}>
          {pending ? 'Giriş yapılıyor…' : 'Giriş yap'}
        </button>
        <div className="demo">
          <span className="muted small">Demo hesapları (parola: Demo1234!)</span>
          <div className="demo-grid">
            {demo.map(([mail, label]) => (
              <button
                type="button"
                key={mail}
                className="btn ghost small"
                onClick={() => {
                  setEmail(mail)
                  setPassword('Demo1234!')
                }}
              >
                {label}
              </button>
            ))}
          </div>
        </div>
      </form>
    </div>
  )
}
