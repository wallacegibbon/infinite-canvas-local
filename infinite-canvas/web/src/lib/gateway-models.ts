import { isToolWorkflow } from "@/lib/canvas/tool-workflows";
import type { GatewayWorkflow } from "@/services/api/workflows";
import type { ChannelModel, ModelCapability } from "@/stores/use-config-store";

/**
 * The gateway's workflows as model entries for the picker.
 *
 * A workflow is what runs; a model is what the user picks. One graph answers to several ids when it
 * offers more than one base model, and the gateway reports those ids because it is the side that
 * spells and resolves them (`img-zit-int8`). Everything else here is the client's own decision:
 * which media kinds are selectable, that a workflow the canvas drives from a button is not, and that a
 * disabled or unavailable workflow is left out rather than offered and then refused.
 */
const CAPABILITIES: ModelCapability[] = ["image", "video", "audio"];

/** The models one workflow contributes, in the order the gateway listed them. */
export function modelsOf(workflow: GatewayWorkflow): ChannelModel[] {
    const capability = workflow.type as ModelCapability;
    if (!CAPABILITIES.includes(capability)) return [];
    if (workflow.enabled === false || workflow.availability?.available === false) return [];
    if (isToolWorkflow(workflow.id)) return [];

    return (workflow.models || []).map((model) => ({
        name: model.id,
        capability,
        label: model.label || workflow.label || model.id,
        workflowId: workflow.id,
        baseModel: model.baseModel || undefined,
        baseModelLabel: model.baseModelLabel || undefined,
        variantModes: workflow.variantModes,
        variantLabels: workflow.variantLabels,
        imageInputs: model.imageInputs,
    }));
}

/** Every selectable model the gateway offers. */
export function gatewayModels(workflows: GatewayWorkflow[]) {
    return workflows.flatMap(modelsOf);
}

/**
 * A total order over the entries, so an unchanged list does not rewrite the config store every eight
 * seconds — including the fields that are only labels, because editing a workflow's label should reach
 * the picker.
 */
export function modelSignature(models: ChannelModel[]) {
    return JSON.stringify(models.map((model) => [model.name, model.capability, model.label || "", model.workflowId || "", model.baseModel || "", model.baseModelLabel || "", model.imageInputs ?? -1, model.variantModes || [], model.variantLabels || {}]));
}
