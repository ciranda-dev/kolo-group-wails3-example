import { avatarColor, initial } from './avatars.js'

/** A round avatar: the glyph (an emoji a person chose), else the first letter of the name, on a color of the seed. */
export function Avatar({ name, seed, glyph = '', size = 36, className = '' }) {
  return (
    <span className={`disc ${className}`} style={{ width: size, height: size, background: avatarColor(seed || name), fontSize: Math.round(size * 0.42) }} aria-hidden="true">
      {glyph || initial(name)}
    </span>
  )
}
