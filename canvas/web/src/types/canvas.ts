export type Position = {
    x: number;
    y: number;
};

export type ViewportTransform = {
    x: number;
    y: number;
    k: number;
};

export enum CanvasNodeType {
    Image = "image",
    Text = "text",
    Config = "config",
    Video = "video",
    Audio = "audio",
    Group = "group",
}

// Node types are open strings: built-ins use CanvasNodeType and plugins use "<pluginId>:<name>".
export type CanvasNodeTypeId = CanvasNodeType | (string & {});

type CanvasNodeStatus = "idle" | "success" | "loading" | "error";
export type CanvasGenerationMode = "text" | "image" | "video" | "audio";
export type CanvasImageGenerationType = "generation" | "edit";

export type CanvasNodeImage = {
    id: string;
    status: CanvasNodeStatus;
    errorDetails?: string;
    content: string;
    storageKey?: string;
    naturalWidth: number;
    naturalHeight: number;
    bytes: number;
    mimeType: string;
};

export type CanvasNodeText = {
    id: string;
    status: CanvasNodeStatus;
    errorDetails?: string;
    content: string;
};

export type CanvasMossLayoutRegion = {
    id: string;
    label: "object" | "character" | "text";
    caption: string;
    text?: string;
    x: number;
    y: number;
    width: number;
    height: number;
};

export type CanvasMossLayout = {
    summary: string;
    regions: CanvasMossLayoutRegion[];
    width?: number;
    height?: number;
};

export type CanvasNodeMetadata = {
    content?: string;
    composerContent?: string;
    prompt?: string;
    status?: CanvasNodeStatus;
    errorDetails?: string;
    fontSize?: number;
    generationMode?: CanvasGenerationMode;
    generationType?: CanvasImageGenerationType;
    model?: string;
    size?: string;
    quality?: string;
    background?: string;
    count?: number;
    textCount?: number;
    texts?: CanvasNodeText[];
    primaryTextId?: string;
    seconds?: string;
    vquality?: string;
    generateAudio?: string;
    watermark?: string;
    videoMode?: string;
    videoSelfLiftMode?: string;
    frameMode?: "first" | "last" | "both";
    imageMode?: "t2i" | "edit" | "inpaint" | "outpaint" | "layout";
    captionStatus?: "running" | "done" | "error";
    captionPrompt?: string;
    captionError?: string;
    /** The prompt that was actually sent, when it is not the text the user typed (context appended). */
    sentPrompt?: string;
    promptOptimizeStatus?: "running" | "done" | "error";
    promptOptimizeKind?: "h3" | "image";
    promptOptimizePrompt?: string;
    promptOptimizeSource?: string;
    promptOptimizeError?: string;
    outpaint?: { left: number; top: number; right: number; bottom: number; width: number; height: number };
    layout?: CanvasMossLayout;
    audioVoice?: string;
    audioFormat?: string;
    audioSpeed?: string;
    audioInstructions?: string;
    references?: string[];
    /**
     * The mask this node stands for, when it is a mask annotation.
     *
     * The node's own picture is the mask drawn over the source, which is what a person needs to see;
     * what a workflow takes as `media.mask` is a different file — binary for the inpaint workflows,
     * the drawn-over picture for the ones that accept it. Binding that file here is what lets the
     * node survive a storage cleanup (`collectImageStorageKeys` keeps anything with a `storageKey`)
     * and lets an inpaint run take the mask from whichever node is connected.
     */
    mask?: { storageKey: string; mimeType?: string };
    naturalWidth?: number;
    naturalHeight?: number;
    freeResize?: boolean;
    images?: CanvasNodeImage[];
    primaryImageId?: string;
    storageKey?: string;
    mimeType?: string;
    bytes?: number;
    durationMs?: number;
    videoTaskId?: string;
    /** The gateway run that is producing this node's audio, kept so a reload can resume polling. */
    audioRunId?: string;
    /** The gateway run that is producing this node's images, kept so a reload can resume polling. */
    imageRunId?: string;
    previewUrl?: string;
    previewMime?: string;
    previewStep?: number;
    previewTotal?: number;
    stepProgress?: { step: number; total: number; complete?: boolean };
    seed?: string;
    seedFixed?: string;
    aspectRatio?: string;
    groupId?: string;
    interactive?: boolean; // Plugin node interaction/move state; see CanvasNodeDefinition.interactionToggle.
    params?: Record<string, unknown>; // Per-node model params (e.g. LoRA stack).
};

export type CanvasNodeData = {
    id: string;
    type: CanvasNodeTypeId;
    /** The stored title: a user-typed name, or a snapshot of the translated default. */
    title: string;
    /**
     * The translation key a generated title came from. When present the title is rendered from it, so
     * a canvas saved in one language reads correctly in the other (docs/decisions.md #70).
     */
    titleKey?: string;
    titleParams?: Record<string, string | number>;
    position: Position;
    width: number;
    height: number;
    metadata?: CanvasNodeMetadata;
};

export type CanvasConnection = {
    id: string;
    fromNodeId: string;
    toNodeId: string;
};

/**
 * What an asset becomes when it is put on a canvas. The asset library describes a picture, a video or a
 * piece of text; a canvas holds nodes. This is the sentence one turns into the other, and the only thing
 * the library and the canvas have to agree about.
 */
export type InsertAssetPayload =
    | { kind: "text"; content: string; title: string }
    | { kind: "image"; dataUrl: string; title: string; storageKey?: string }
    | { kind: "video"; url: string; title: string; storageKey?: string; width?: number; height?: number };

export type ConnectionHandle = {
    nodeId: string;
    handleType: "source" | "target";
};

export type SelectionBox = {
    startWorldX: number;
    startWorldY: number;
    currentWorldX: number;
    currentWorldY: number;
    additive: boolean;
    initialSelectedNodeIds: string[];
};

export type ContextMenuState =
    | {
          type: "node";
          x: number;
          y: number;
          nodeId: string;
      }
    | {
          type: "connection";
          x: number;
          y: number;
          connectionId: string;
      };
