import { Images, Settings2 } from "lucide-react";

// Only the entries this build can actually serve. The cloud interfaces (image,
// video and prompt workbenches) were removed, so their slugs are gone rather than
// filtered out at render time.
export const navigationTools = [
    {
        slug: "assets",
        icon: Images,
    },
    {
        slug: "config",
        icon: Settings2,
    },
] as const;

export type NavigationToolSlug = (typeof navigationTools)[number]["slug"];
