/**
 * Minimal delimited-text parser for the CSV/TSV preview.
 *
 * Not a full RFC 4180 implementation: it handles quoted fields (including
 * escaped quotes and embedded delimiters/newlines) and stops at a row/column
 * budget so a 200MB export cannot freeze the tab. Anything it does not handle
 * is handled by the "查看原文" toggle next to the table.
 */

export interface ParsedTable {
  header: string[];
  rows: string[][];
  /** True when rows or columns were dropped by the budget. */
  clipped: boolean;
  /** Total rows seen, including the ones that were dropped. */
  totalRows: number;
}

export function delimiterFor(path: string, mime?: string): string {
  if (mime?.includes("tab-separated")) return "\t";
  if (path.toLowerCase().endsWith(".tsv")) return "\t";
  return ",";
}

export function parseDelimited(
  text: string,
  delimiter = ",",
  maxRows = 200,
  maxColumns = 12,
): ParsedTable {
  const records: string[][] = [];
  let field = "";
  let record: string[] = [];
  let quoted = false;
  let totalRows = 0;

  const pushField = () => {
    record.push(field);
    field = "";
  };

  const pushRecord = () => {
    pushField();
    totalRows++;
    // The budget counts data rows; the header record is always kept.
    if (records.length < maxRows + 1) records.push(record);
    record = [];
  };

  for (let index = 0; index < text.length; index++) {
    const char = text[index];

    if (quoted) {
      if (char === '"') {
        if (text[index + 1] === '"') {
          field += '"';
          index++;
        } else {
          quoted = false;
        }
      } else {
        field += char;
      }
      continue;
    }

    if (char === '"') {
      quoted = true;
    } else if (char === delimiter) {
      pushField();
    } else if (char === "\n") {
      pushRecord();
    } else if (char === "\r") {
      if (text[index + 1] === "\n") index++;
      pushRecord();
    } else {
      field += char;
    }
  }

  if (field.length > 0 || record.length > 0) pushRecord();

  const header = (records[0] ?? []).slice(0, maxColumns);
  const rows = records.slice(1).map((row) => row.slice(0, maxColumns));

  const clipped =
    totalRows > maxRows + 1 ||
    records.some((row) => row.length > maxColumns) ||
    (records[0]?.length ?? 0) > maxColumns;

  return { header, rows, clipped, totalRows };
}
