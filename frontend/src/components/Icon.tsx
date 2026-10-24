// One stroke-icon family (24×24 grid, 1.75 stroke, round joins) drawn for
// this app, so every glyph shares weight and corner style.
const paths: Record<string, string> = {
  snow: 'M12 2v20M4.9 6.5l14.2 11M4.9 17.5l14.2-11M9 3.5l3 2 3-2M9 20.5l3-2 3 2M3 9.2l1.2 3.3L2.6 15M21 9.2l-1.2 3.3 1.6 2.5',
  gauge: 'M12 14l3.5-3.5M4.5 18a8.5 8.5 0 1 1 15 0M7 18h10',
  boxes: 'M3 8.5L12 4l9 4.5-9 4.5L3 8.5zM3 8.5v7L12 20l9-4.5v-7M12 13v7',
  pallet: 'M3 15h18M5 15v4M12 15v4M19 15v4M3 19h18M6 6h12v9H6zM6 10.5h12',
  tag: 'M3 12V4h8l10 10-8 8L3 12zM7.5 7.5h.01',
  thermo: 'M14 14.8V5a2 2 0 0 0-4 0v9.8a4 4 0 1 0 4 0zM12 9v7',
  bell: 'M6 16V11a6 6 0 1 1 12 0v5l2 2H4l2-2zM10 21h4',
  check: 'M4 12.5l5 5L20 6.5',
  clipboard: 'M9 4h6v3H9zM7 5.5H5v15h14v-15h-2M8.5 12l2 2 4-4M8.5 17h7',
  truck: 'M2 6h11v10H2zM13 9h4.5l3.5 3.5V16h-8M6 19a2 2 0 1 0 0-.01M17 19a2 2 0 1 0 0-.01',
  route: 'M6 19a2 2 0 1 0 0-.01M18 7a2 2 0 1 0 0-.01M6 17V9a3 3 0 0 1 3-3h4M18 9v6a3 3 0 0 1-3 3h-4',
  shield: 'M12 3l8 3v6c0 4.5-3.4 8-8 9-4.6-1-8-4.5-8-9V6l8-3zM8.5 12l2.5 2.5 4.5-5',
  link: 'M10 14a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1 1M14 10a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1-1',
  scroll: 'M8 3h11v15a3 3 0 0 1-3 3H6a3 3 0 0 1-3-3v-2h11v2a3 3 0 0 0 3 3M8 3a3 3 0 0 0-3 3v10M11 8h5M11 12h5',
  phone: 'M7 2h10v20H7zM11 18.5h2',
  store: 'M4 10v10h16V10M3 10l2-6h14l2 6M3 10a3 3 0 0 0 6 0 3 3 0 0 0 6 0 3 3 0 0 0 6 0M10 20v-5h4v5',
  menu: 'M4 6h16M4 12h16M4 18h16',
  close: 'M6 6l12 12M18 6L6 18',
  logout: 'M15 4h4v16h-4M10 8l-4 4 4 4M6 12h11',
  sun: 'M12 16a4 4 0 1 0 0-8 4 4 0 0 0 0 8zM12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4',
  moon: 'M20 14.5A8 8 0 0 1 9.5 4 8 8 0 1 0 20 14.5z',
  monitor: 'M3 4h18v12H3zM8 20h8M12 16v4',
  alert: 'M12 3l10 18H2L12 3zM12 10v4.5M12 17.5h.01',
  info: 'M12 22a10 10 0 1 0 0-20 10 10 0 0 0 0 20zM12 11v6M12 7.5h.01',
  wifi: 'M2 8.5a15 15 0 0 1 20 0M5 12a10 10 0 0 1 14 0M8.5 15.5a5 5 0 0 1 7 0M12 19h.01',
  wifiOff: 'M3 3l18 18M8.5 15.5a5 5 0 0 1 7 0M5 12a10 10 0 0 1 4-2.4M14.5 9.7A10 10 0 0 1 19 12M2 8.5a15 15 0 0 1 4.3-2.9M12 19h.01',
  battery: 'M3 7h15v10H3zM21 10v4M6 10v4M9.5 10v4',
  arrowRight: 'M5 12h14M13 6l6 6-6 6',
  search: 'M11 18a7 7 0 1 0 0-14 7 7 0 0 0 0 14zM20 20l-4-4',
  refresh: 'M20 11a8 8 0 0 0-14.3-4.9L4 8M4 4v4h4M4 13a8 8 0 0 0 14.3 4.9L20 16M20 20v-4h-4',
  user: 'M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8zM4 21a8 8 0 0 1 16 0',
  lock: 'M5 11h14v10H5zM8 11V7a4 4 0 0 1 8 0v4',
  plus: 'M12 5v14M5 12h14',
  download: 'M12 4v11M7 10l5 5 5-5M4 20h16',
  pulse: 'M3 12h4l2.5-6 4 12 2.5-6H21',
}

export type IconName = keyof typeof paths

export function Icon({ name, size = 18, label, className }: { name: IconName; size?: number; label?: string; className?: string }) {
  return (
    <svg
      className={'icon ' + (className ?? '')}
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.75}
      strokeLinecap="round"
      strokeLinejoin="round"
      role={label ? 'img' : undefined}
      aria-label={label}
      aria-hidden={label ? undefined : true}
      focusable="false"
    >
      <path d={paths[name]} />
    </svg>
  )
}
