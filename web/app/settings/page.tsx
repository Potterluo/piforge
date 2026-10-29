import { redirect } from "next/navigation";

/** /settings has no index of its own; 模型与凭据 is where the sidebar used to land. */
export default function SettingsIndexPage() {
  redirect("/settings/providers");
}
