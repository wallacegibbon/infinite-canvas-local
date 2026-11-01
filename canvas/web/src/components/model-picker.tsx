import { useEffect, useId, useMemo, useState } from "react";
import { Cpu, Layers, ScanText } from "lucide-react";
import { useTranslation } from "react-i18next";

import i18n from "@/i18n";
import { Select, SelectContent, SelectItem, SelectTrigger } from "@/components/kit/select-primitive";
import { cn } from "@/lib/utils";
import { baseModelOptions, channelModelOf, modelOptionLabel, selectableModelsByCapability, workflowModelOptions, workflowModelValue, type AiConfig, type ModelCapability } from "@/stores/use-config-store";

type ModelPickerExtraOption = { value: string; label: string; disabled?: boolean };

type ModelPickerProps = {
    config: AiConfig;
    value?: string;
    onChange: (model: string) => void;
    capability?: ModelCapability;
    className?: string;
    fullWidth?: boolean;
    placeholder?: string;
    extraOptions?: ModelPickerExtraOption[];
    onMissingConfig?: () => void;
};

export function ModelPicker({ config, value, onChange, capability, className, fullWidth = false, placeholder, extraOptions = [], onMissingConfig }: ModelPickerProps) {
    const { t } = useTranslation();
    const pickerId = useId();
    const [open, setOpen] = useState(false);
    const options = useMemo(
        () => Array.from(new Set([...(!capability ? [value] : []), ...selectableModelsByCapability(config, capability), ...extraOptions.map((option) => option.value)].filter((model): model is string => Boolean(model)))),
        [capability, config, extraOptions, value],
    );
    const current = value || "";
    const pickerPlaceholder = placeholder || t("settingsPanels.model.select");
    const optionLabel = (model: string) => extraOptions.find((option) => option.value === model)?.label || modelOptionLabel(config, model);
    const currentExtra = extraOptions.find((option) => option.value === current);

    useEffect(() => {
        const closeOtherPicker = (event: Event) => {
            if ((event as CustomEvent<string>).detail !== pickerId) setOpen(false);
        };
        window.addEventListener("model-picker-open", closeOtherPicker);
        return () => window.removeEventListener("model-picker-open", closeOtherPicker);
    }, [pickerId]);

    return (
        <Select
            open={open}
            value={current}
            onOpenChange={(nextOpen) => {
                if (nextOpen && !options.length) onMissingConfig?.();
                if (nextOpen) window.dispatchEvent(new CustomEvent("model-picker-open", { detail: pickerId }));
                setOpen(nextOpen);
            }}
            onValueChange={onChange}
        >
            <SelectTrigger
                className={cn(
                    "canvas-composer-model-picker h-8 w-fit max-w-full gap-2 rounded-full border border-input bg-transparent px-3 text-sm font-normal shadow-sm transition-colors",
                    fullWidth ? "w-full min-w-0 justify-start" : "min-w-[9rem] justify-start",
                    "data-[state=open]:border-ring data-[state=open]:ring-2 data-[state=open]:ring-ring/20",
                    className,
                )}
                onMouseDown={(event) => event.stopPropagation()}
                onPointerDown={(event) => event.stopPropagation()}
                title={current ? optionLabel(current) : pickerPlaceholder}
            >
                {currentExtra ? <ScanText className="size-4 shrink-0 opacity-70" /> : <Cpu className="size-4 shrink-0 opacity-70" />}
                <span className="canvas-model-picker-text min-w-0 flex-1 truncate text-left">{current ? optionLabel(current) : pickerPlaceholder}</span>
            </SelectTrigger>
            <SelectContent
                data-canvas-no-zoom
                className="z-[1200] w-80 max-w-[calc(100vw-24px)] rounded-xl border border-border/70 bg-popover p-1 shadow-xl"
                position="popper"
                align="start"
                side="bottom"
                sideOffset={6}
                onPointerDown={(event) => event.stopPropagation()}
                onMouseDown={(event) => event.stopPropagation()}
            >
                {options.length ? (
                    options.map((model) => {
                        const extra = extraOptions.find((option) => option.value === model);
                        return (
                            <SelectItem key={model} value={model} textValue={optionLabel(model)} disabled={extra?.disabled}>
                                {extra ? (
                                    <span className="flex min-w-0 items-center gap-2">
                                        <ScanText className="size-4 shrink-0 opacity-70" />
                                        <span className="truncate">{extra.label}</span>
                                    </span>
                                ) : (
                                    <ModelLabel config={config} model={model} />
                                )}
                            </SelectItem>
                        );
                    })
                ) : (
                    <SelectItem value="__empty__" disabled>
                        {emptyModelLabel(config, capability)}
                    </SelectItem>
                )}
            </SelectContent>
        </Select>
    );
}

function emptyModelLabel(config: AiConfig, capability?: ModelCapability) {
    const label = capability ? i18n.t(`settingsPanels.model.capabilities.${capability}`) : "";
    if (capability && config.models.length) return i18n.t("settingsPanels.model.assign", { capability: label });
    return config.models.length ? i18n.t("settingsPanels.model.noMatch", { capability: label }) : i18n.t("settingsPanels.model.addFirst");
}

function ModelLabel({ config, model }: { config: AiConfig; model: string }) {
    return (
        <span className="flex min-w-0 items-center gap-2">
            <Cpu className="size-4 shrink-0 opacity-70" />
            <span className="truncate">{modelOptionLabel(config, model)}</span>
        </span>
    );
}

type LevelOption = { value: string; label: string; title?: string };

type LevelPickerProps = {
    config: AiConfig;
    value: string;
    onChange: (model: string) => void;
    className?: string;
    fullWidth?: boolean;
    placeholder?: string;
    onMissingConfig?: () => void;
};

/** Top-level dropdown: pick a workflow only; variants live under the workflow they belong to. */
export function WorkflowModelPicker({ config, value, onChange, capability, className, fullWidth = false, placeholder, onMissingConfig }: LevelPickerProps & { capability?: ModelCapability }) {
    const options = useMemo<LevelOption[]>(
        () =>
            workflowModelOptions(config, capability)
                .filter((model) => channelModelOf(config, model)?.model.workflowId !== "video-h3-fast")
                .map((model) => ({ value: model, label: modelOptionLabel(config, model) })),
        [config, capability],
    );
    const selected = useMemo(() => workflowModelValue(config, value) || value, [config, value]);
    return (
        <LevelSelect value={selected} options={options} onChange={onChange} className={className} fullWidth={fullWidth} placeholder={placeholder || i18n.t("settingsPanels.model.select")} onMissingConfig={onMissingConfig} icon="workflow" width="w-72" />
    );
}

/** Second-level dropdown: switch between the default base model and its variants within one workflow. */
export function BaseModelPicker({ config, value, onChange, className, fullWidth = false, placeholder }: LevelPickerProps) {
    const { t } = useTranslation();
    const options = useMemo<LevelOption[]>(
        () =>
            baseModelOptions(config, value).map((option) => ({
                value: option.value,
                label: option.isDefault ? t("settingsPanels.model.defaultBase") : option.label,
                title: option.name,
            })),
        [config, value, t],
    );
    if (options.length < 2) return null;
    return <LevelSelect value={value} options={options} onChange={onChange} className={className} fullWidth={fullWidth} placeholder={placeholder || t("settingsPanels.model.select")} icon="base" width="w-64" />;
}

function LevelSelect({
    value,
    options,
    onChange,
    className,
    fullWidth,
    placeholder,
    onMissingConfig,
    icon,
    width,
}: {
    value: string;
    options: LevelOption[];
    onChange: (model: string) => void;
    className?: string;
    fullWidth?: boolean;
    placeholder: string;
    onMissingConfig?: () => void;
    icon: "workflow" | "base";
    width: string;
}) {
    const pickerId = useId();
    const [open, setOpen] = useState(false);
    const current = options.find((option) => option.value === value);
    const label = current?.label || placeholder;

    useEffect(() => {
        const closeOtherPicker = (event: Event) => {
            if ((event as CustomEvent<string>).detail !== pickerId) setOpen(false);
        };
        window.addEventListener("model-picker-open", closeOtherPicker);
        return () => window.removeEventListener("model-picker-open", closeOtherPicker);
    }, [pickerId]);

    return (
        <Select
            open={open}
            value={value}
            onOpenChange={(nextOpen) => {
                if (nextOpen && !options.length && onMissingConfig) onMissingConfig();
                if (nextOpen) window.dispatchEvent(new CustomEvent("model-picker-open", { detail: pickerId }));
                setOpen(nextOpen);
            }}
            onValueChange={onChange}
        >
            <SelectTrigger
                className={cn(
                    "canvas-composer-model-picker h-8 w-fit max-w-full gap-2 rounded-full border border-input bg-transparent px-3 text-sm font-normal shadow-sm transition-colors",
                    fullWidth ? "w-full min-w-0 justify-start" : "min-w-[8.5rem] justify-start",
                    "data-[state=open]:border-ring data-[state=open]:ring-2 data-[state=open]:ring-ring/20",
                    className,
                )}
                onMouseDown={(event) => event.stopPropagation()}
                onPointerDown={(event) => event.stopPropagation()}
                title={label}
            >
                {icon === "workflow" ? <Cpu className="size-3.5 shrink-0 opacity-70" /> : <Layers className="size-3.5 shrink-0 opacity-70" />}
                <span className="canvas-model-picker-text min-w-0 flex-1 truncate text-left">{label}</span>
            </SelectTrigger>
            <SelectContent
                data-canvas-no-zoom
                className={cn("z-[1200] max-w-[calc(100vw-24px)] rounded-xl border border-border/70 bg-popover p-1 shadow-xl", width)}
                position="popper"
                align="start"
                side="bottom"
                sideOffset={6}
                onPointerDown={(event) => event.stopPropagation()}
                onMouseDown={(event) => event.stopPropagation()}
            >
                {options.length ? (
                    options.map((option) => (
                        <SelectItem key={option.value} value={option.value} textValue={option.label}>
                            <span className="flex min-w-0 items-center gap-2" title={option.title}>
                                {icon === "workflow" ? <Cpu className="size-3.5 shrink-0 opacity-70" /> : <Layers className="size-3.5 shrink-0 opacity-70" />}
                                <span className="truncate">{option.label}</span>
                            </span>
                        </SelectItem>
                    ))
                ) : (
                    <SelectItem value="__empty__" disabled>
                        {placeholder}
                    </SelectItem>
                )}
            </SelectContent>
        </Select>
    );
}
