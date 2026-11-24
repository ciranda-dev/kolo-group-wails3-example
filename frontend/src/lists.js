/** value when it is a list, else fallback: Go sends a nil slice as null, and an event reaches the window as Go sent it. */
export const list = (value, fallback = []) => (Array.isArray(value) ? value : fallback)
