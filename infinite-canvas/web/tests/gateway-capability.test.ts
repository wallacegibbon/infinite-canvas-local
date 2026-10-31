import { describe, expect, it, vi } from "vitest";

/** The local-gateway sync must keep every capability the gateway serves, not just image and video. */
vi.mock("@/i18n", () => ({ default: { t: (key: string) => key } }));

const { gatewayModelCapability } = await import("@/lib/gateway-capability");

describe("gatewayModelCapability", () => {
    it("trusts the type the gateway declares", () => {
        expect(gatewayModelCapability("anything", "audio")).toBe("audio");
        expect(gatewayModelCapability("anything", "video")).toBe("video");
        expect(gatewayModelCapability("anything", "IMAGE")).toBe("image");
    });

    it("falls back to the workflow id prefix", () => {
        expect(gatewayModelCapability("audio-minimax-music-3")).toBe("audio");
        expect(gatewayModelCapability("image-z-image-turbo")).toBe("image");
        expect(gatewayModelCapability("video-h3-t2v")).toBe("video");
        expect(gatewayModelCapability("text-some-flow")).toBe("text");
    });

    it("uses the name heuristic only when neither source says anything", () => {
        expect(gatewayModelCapability("some-music-model")).toBe("audio");
        expect(gatewayModelCapability("sora-2")).toBe("video");
        expect(gatewayModelCapability("unknown-model")).toBeNull();
    });

    it("rejects empty ids", () => {
        expect(gatewayModelCapability("")).toBeNull();
        expect(gatewayModelCapability("   ")).toBeNull();
    });
});
