import { resets, usageTone, usedPercent } from "../lib/format";
import type { AccountView } from "../lib/types";

// UsageMeter is the account's period: a bar of the share used, amber past
// 80% and red once it is all used, and when it starts again. It is never
// in dollars; an admin account has no allowance, so no bar. slim is the
// sidebar's, the words over a hairline.
export function UsageMeter({ account, slim }: { account: AccountView; slim?: boolean }) {
  if (account.admin) return null;
  const used = usedPercent(account.used);
  const tone = usageTone(account.used);
  const words = [`${used}% used`, account.resetsAt ? resets(account.resetsAt) : ""].filter(Boolean).join(" · ");
  return (
    <div className={`usage${slim ? " slim" : ""}${tone ? ` ${tone}` : ""}`}>
      <div className="usage-bar" role="meter" aria-label="Usage" aria-valuenow={used} aria-valuemin={0} aria-valuemax={100}>
        <div style={{ width: `${used}%` }} />
      </div>
      <span>{words}</span>
    </div>
  );
}
