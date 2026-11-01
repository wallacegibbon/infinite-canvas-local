import type { ReactNode } from "react";

import { useLocalGatewaySync } from "@/hooks/use-local-gateway-sync";
import { useSkinUnlockPersistence } from "@/hooks/use-skin-unlock-persistence";

/**
 * The two things that have to run once, before the canvas is usable.
 *
 * It used to also read `?baseUrl=&apiKey=` from the address bar to import a channel. There is one
 * channel and its address is the page's own gateway (docs/decisions.md #71), so there is nothing left
 * to import.
 */
export function ClientRootInit({ children }: { children: ReactNode }) {
    useLocalGatewaySync();
    useSkinUnlockPersistence();
    return <>{children}</>;
}
