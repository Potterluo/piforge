import { describe, expect, it } from "vitest";

import {
  MAX_COMPOSER_IMAGES,
  imageLimitNotice,
  planImageAttachments,
} from "@/lib/session/attachments";

describe("planImageAttachments", () => {
  it("accepts a paste that fits", () => {
    expect(planImageAttachments(3, 0)).toEqual({ accepted: 3, dropped: 0 });
  });

  it("counts the images a single paste drops", () => {
    expect(planImageAttachments(6, 0)).toEqual({ accepted: 4, dropped: 2 });
  });

  it("counts what is dropped because images are already attached", () => {
    expect(planImageAttachments(3, 3)).toEqual({ accepted: 1, dropped: 2 });
  });

  it("drops the whole paste when the composer is already full", () => {
    expect(planImageAttachments(2, MAX_COMPOSER_IMAGES)).toEqual({ accepted: 0, dropped: 2 });
  });

  it("names the count, so the drop is not silent", () => {
    expect(imageLimitNotice(2)).toContain("4 张");
    expect(imageLimitNotice(2)).toContain("另外 2 张没有加入");
  });
});
