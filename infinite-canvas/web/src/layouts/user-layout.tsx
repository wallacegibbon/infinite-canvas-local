import type { ReactNode } from "react";

import { AppConfigModal } from "@/components/layout/app-config-modal";
import { ResourceSetupGate } from "@/components/layout/resource-setup-gate";

/**
 * The only layout left.
 *
 * There used to be a top bar here with a brand link and three tabs — canvases, assets,
 * settings. All three became panels over the canvas, so the bar had nothing to hold. What
 * remains is the two things that are genuinely global and belong outside any one canvas:
 * the settings dialog and the first-run resource prompt.
 */
export default function UserLayout({ children }: { children: ReactNode }) {
    return (
        <div className="flex h-dvh overflow-hidden bg-background text-foreground">
            <div className="min-w-0 flex-1 overflow-hidden">{children}</div>
            <AppConfigModal />
            <ResourceSetupGate />
        </div>
    );
}
