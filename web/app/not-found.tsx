import Link from "next/link";

import { Button } from "@/components/ui/button";
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty";

/** The default Next 404 page is English, which is jarring in a Chinese shell. */
export default function NotFound() {
  return (
    <div className="flex min-h-svh items-center justify-center p-6">
      <Empty className="max-w-md border border-dashed border-border">
        <EmptyHeader>
          <EmptyMedia variant="icon">404</EmptyMedia>
          <EmptyTitle>页面不存在</EmptyTitle>
          <EmptyDescription>
            这个地址没有对应的页面。可能是会话或项目被删除了，也可能是链接拼错了。
          </EmptyDescription>
        </EmptyHeader>
        <Button render={<Link href="/" />} size="sm" variant="outline">
          回到工作台
        </Button>
      </Empty>
    </div>
  );
}