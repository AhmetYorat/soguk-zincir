// Theme preference: follow the system, or force light / dark. The choice is
// a per-viewer convenience kept in localStorage; the page works without it.
export type ThemePref = 'system' | 'light' | 'dark'

const KEY = 'sz.theme'

export function loadTheme(): ThemePref {
  try {
    const v = localStorage.getItem(KEY)
    if (v === 'light' || v === 'dark') return v
  } catch {
    /* storage blocked */
  }
  return 'system'
}

export function applyTheme(pref: ThemePref) {
  const root = document.documentElement
  if (pref === 'system') delete root.dataset.theme
  else root.dataset.theme = pref
  try {
    if (pref === 'system') localStorage.removeItem(KEY)
    else localStorage.setItem(KEY, pref)
  } catch {
    /* storage blocked */
  }
}

export const nextTheme: Record<ThemePref, ThemePref> = { system: 'light', light: 'dark', dark: 'system' }
