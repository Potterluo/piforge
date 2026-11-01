/**
 * The inline-comment budgets, mirrored from the server.
 *
 * `backend/internal/httpapi/review_comment.go` owns these caps (`maxReviewQuoteLines`,
 * `maxReviewQuoteRune`, `maxReviewBodyRune`) and cuts rather than refuses. The
 * composer needs the same numbers *before* the send to warn, so they live here
 * too; the server's counts remain the authority — the warning is an estimate,
 * the report in the response is the fact. Keep the two in sync.
 */

import type { ReviewTruncation } from "@/lib/types";

export const MAX_REVIEW_QUOTE_LINES = 24;
export const MAX_REVIEW_QUOTE_RUNE = 400;
export const MAX_REVIEW_BODY_RUNE = 4000;

/** Runes, not UTF-16 units: the server counts code points. */
function runeCount(text: string): number {
  return Array.from(text).length;
}

/**
 * reviewSelectionWarning explains, in the composer and before sending, what the
 * caps are about to cut. It returns null when the comment fits, so a normal
 * comment never carries an always-on warning nobody reads.
 */
export function reviewSelectionWarning(quote: string[], body: string): string | null {
  const problems: string[] = [];

  if (quote.length > MAX_REVIEW_QUOTE_LINES) {
    problems.push(
      `引用选了 ${quote.length} 行，只有前 ${MAX_REVIEW_QUOTE_LINES} 行会送达（${quote.length - MAX_REVIEW_QUOTE_LINES} 行被省略）`,
    );
  }
  const longLines = quote.filter((line) => runeCount(line) > MAX_REVIEW_QUOTE_RUNE).length;
  if (longLines > 0) {
    problems.push(`${longLines} 行引用超过 ${MAX_REVIEW_QUOTE_RUNE} 字，超出部分会被截断`);
  }
  // The server trims the body before measuring it, so the estimate does too.
  const bodyRunes = runeCount(body.trim());
  if (bodyRunes > MAX_REVIEW_BODY_RUNE) {
    problems.push(
      `正文 ${bodyRunes} 字，超出 ${MAX_REVIEW_BODY_RUNE} 字的 ${bodyRunes - MAX_REVIEW_BODY_RUNE} 字会被截断`,
    );
  }

  if (problems.length === 0) return null;
  return `发送前提醒：${problems.join("；")}。发送后转录里会写明实际送达了多少。`;
}

/**
 * describeReviewTruncation turns the server's counts into one sentence. It is
 * used where the response is rendered, so the numbers are the server's, not a
 * client-side re-estimate of them.
 */
export function describeReviewTruncation(truncation: ReviewTruncation): string {
  const parts: string[] = [];
  if (truncation.quote_lines_omitted > 0) {
    parts.push(
      `原始评论引用了 ${truncation.quote_lines_kept + truncation.quote_lines_omitted} 行，仅前 ${truncation.quote_lines_kept} 行送达（省略 ${truncation.quote_lines_omitted} 行）`,
    );
  }
  if (truncation.quote_line_runes_cut > 0) {
    parts.push(
      `${truncation.quote_lines_shortened} 行引用超长，共截断 ${truncation.quote_line_runes_cut} 字`,
    );
  }
  if (truncation.body_runes_cut > 0) {
    parts.push(
      `正文超出 ${MAX_REVIEW_BODY_RUNE} 字，截断 ${truncation.body_runes_cut} 字`,
    );
  }
  return parts.join("；");
}
