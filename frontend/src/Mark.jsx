/** The Kolo mark, drawn with the colors of the theme. */
export function Mark({ size = 32, className = '' }) {
  return (
    <svg className={className} viewBox="-1 -1 28 28" width={size} height={size} aria-hidden="true">
      <circle cx="13" cy="13" r="10" fill="none" stroke="var(--text)" strokeWidth="2" />
      <circle cx="13" cy="3" r="2.8" fill="var(--accent)" />
      <circle cx="22.7" cy="15.8" r="2.8" fill="var(--text)" />
      <circle cx="7" cy="21.2" r="2.8" fill="var(--text)" />
      <g transform="translate(13 13) scale(1.08)" fill="var(--accent)">
        <rect x="-4.7" y="-4.3" width="9.4" height="6.6" rx="1.9" />
        <path d="M-3.6 1.6v4.3l4.4-4.3z" />
      </g>
    </svg>
  )
}
