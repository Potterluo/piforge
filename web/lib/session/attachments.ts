/**
 * The composer's image budget.
 *
 * The cap itself is a product decision (a message carries a handful of images,
 * not a paste of a folder). What is not allowed is dropping them silently: a
 * paste of six images that keeps four and says nothing leaves the user believing
 * the agent saw all six. `planImageAttachments` counts what fits, and the
 * composer turns `dropped` into a visible warning before sending.
 */

export const MAX_COMPOSER_IMAGES = 4;

/** How many of `incoming` images fit next to `already` attached ones. */
export function planImageAttachments(
  incoming: number,
  already: number,
): { accepted: number; dropped: number } {
  const room = Math.max(0, MAX_COMPOSER_IMAGES - Math.max(0, already));
  const accepted = Math.max(0, Math.min(incoming, room));
  return { accepted, dropped: Math.max(0, incoming - accepted) };
}

/** The sentence the composer shows when some images did not fit. */
export function imageLimitNotice(dropped: number): string {
  return `一条消息最多 ${MAX_COMPOSER_IMAGES} 张图片，另外 ${dropped} 张没有加入。`;
}
