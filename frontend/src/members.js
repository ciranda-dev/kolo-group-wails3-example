/** How many members one request reads, and one "Show more" adds: a page of the list on screen, never a limit of the room. */
export const MEMBER_PAGE = 100

/** Whether the room has members that are not on screen yet. */
export const hasMore = (total, admins, shown) => total > admins + shown

/** The list after a page: the page when it starts at the beginning, else the page added to what was shown. */
export function addPage(shown, page, offset) {
  return offset === 0 ? page : [...shown.slice(0, offset), ...page]
}
