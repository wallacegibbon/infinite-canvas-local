import i18n from "@/i18n";

/**
 * Chinese copy for the resource catalogue.
 *
 * `resources/index.json` is data and therefore English (docs/decisions.md #8, #66), but the resource
 * panel is UI: a Chinese reader should see Chinese. The gateway does not carry a second language, so
 * the translation lives here, keyed by the resource's stable id — exactly what `gateway-labels.ts`
 * does for workflow labels. A pack or resource this file has never heard of (something you added
 * yourself) keeps the English text from the index, which is the right answer for an unknown id.
 *
 * Ids contain dots (`qwen3.5_9b_int8_convrot`), so this is a plain map rather than an i18n path: a
 * dotted key would be read as a nested path and never resolve.
 */
type ResourceCopyField = "label" | "description" | "note" | "license";

type ResourceCopy = Partial<Record<ResourceCopyField, string>>;

const PACK_ZH: Record<string, ResourceCopy> = {
    core: { label: "画布与基础工作流", description: "画布、网关、启动器和基础工作流已随安装器内置，无需下载。" },
    moss: { description: "BF16 与 INT8 两套精度按显卡选择：大显存推荐 BF16（SFT 8/50、Balanced 8/50），小显存推荐 INT8（SFT 8/50）。TE 统一 INT8；按工作流安装，已具备的资源不会重复下载。" },
    qwen21: { label: "千问 2.1 全能工作流", description: "按工作流安装：生图/编辑（文生图、多图编辑、局部重绘、扩图）共用三件套；扩图另含 Outpaint LoRA，局部重绘/扩图节点随包安装；多角度从画布「多角度」进入。" },
    zimage: { description: "Z-Image Turbo 默认 BF16 底模、Qwen3-4B 文本编码器和 ae VAE；4–8 步蒸馏。底模可在画布底模下拉自选，小显存可另装 INT8 底模切换。" },
    h3: { label: "H3 视频生成（VDN / 无VDN / 角色替换）", description: "H3 视频生成：7 条工作流（VDN 三档 / 无VDN 三档 / 角色替换）。节点随包，权重按工作流下载。" },
    music: { label: "MiniMax Music 3 音乐生成", description: "MiniMax Music 3 文生音乐：风格描述 + 可选歌词，默认 fp16 底模、int8 文本编码器和 dav 音频 VAE；int8/fp32 底模与更大的文本编码器是选下内容。" },
    interp: { label: "RIFE 插帧", description: "把已有片段补帧到 2×/3×。节点从 GitHub 安装，权重由节点自己下载；只影响「插帧」按钮。" },
    upscale: { label: "超分", description: "快速放大图片（ESRGAN）。节点随包自动安装，权重按工作流下载。" },
};

const RESOURCE_ZH: Record<string, ResourceCopy> = {
    "qwen3.5_9b_int8_convrot": { label: "Qwen3.5-9B int8 convrot 文本编码器" },
    moss_balanced_s100: { label: "MOSS Balanced 融合模型（本地烘焙）" },
    "qwen_image_2.1_turbo_int8_convrot": { note: "Comfy-Org 官方 int8 ConvRot，与本机实测文件哈希一致。" },
    qwen3vl_8b_int8_convrot: { label: "Qwen3VL-8B int8 convrot 文本编码器", note: "Comfy-Org 官方 int8 ConvRot，与本机实测文件哈希一致。" },
    qwenmultiangle: { label: "Qwen Multiangle Camera 节点", note: "安装后需重启 ComfyUI；主源内置 zip，备源公开 GitHub 归档。" },
    qwen21_outpaint_lora: { label: "Qwen 2.1 扩图 LoRA（Outpaint V2）", note: "扩图变体使用；公开源 ausboss / bozoyan，权重与本机一致。" },
    inpaint_cropandstitch: { label: "Inpaint Crop & Stitch（局部重绘节点）", note: "局部重绘所需；安装后需重启 ComfyUI。" },
    kjnodes: { label: "KJNodes（扩图蒙版节点）", note: "仅扩图的 DrawMaskOnImage 使用；无需额外 pip 依赖；安装后需重启 ComfyUI。" },
    moss_image_sft_8steps_lora: { label: "MOSS-Image SFT 8步 LoRA" },
    moss_image_sft_int8_convrot: { label: "MOSS-Image SFT int8 convrot（低显存）" },
    z_image_turbo_bf16: { note: "默认底模；与 int8/fp8 版本在画布底模下拉里自选。" },
    qwen_3_4b: { label: "Qwen3-4B 文本编码器" },
    z_image_turbo_int8_convrot: { label: "Z-Image Turbo int8（低显存）", note: "选下内容；小显存用，装好后在画布底模下拉里选 int8 版本。" },
    h3_unet_singularity_ref2va_v13_int8: { label: "H3 Singularity Ref2VA v1.3 int8（剪枝）", note: "H3 主模型；T2V/I2V/Ref2V/V2V 通用。" },
    h3_te_qwen3vl_32b_int8_convrot: { label: "Qwen3-VL-32B H3 文本编码器 int8 ConvRot", note: "H3 共用 TE；约 27.1GB，加载占显存较大。" },
    h3_audio_vae_fp32: { label: "H3 音频 VAE fp32" },
    h3_video_vae_fp16: { label: "H3 视频 VAE fp16", note: "所有 H3 视频工作流都通过 VAELoader 读取它（和音频 VAE 同一目录）。" },
    h3_latent_upscaler_3d_fp16: { note: "SelfLift 高清二采使用；魔搭同内容文件（文件名带 3d_conv_v1_fp16），另有 hf-mirror 备源。" },
    h3_character_swap_lora: { note: "角色替换入口使用。" },
    h3_vdn_stage_turbo_config: { note: "魔搭仓内文件名为 adapter_spec.json（内容与本地 adapter_config.json 一致），下载后落到 adapter_config.json。" },
    h3_vdn_stage_default_config: { note: "魔搭仓内文件名为 adapter_spec.json（内容与本地 adapter_config.json 一致），下载后落到 adapter_config.json。" },
    h3_vdn_node: { note: "预解压目录随包复制，不跑 pip；节点依赖按 README 处理。" },
    h3_audio_t8_node: { note: "预解压目录随包复制，不跑 pip；节点依赖按 README 处理。" },
    h3_yc_nodes: { note: "预解压目录随包复制，不跑 pip；节点依赖按 README 处理。" },
    h3_selflift_node: { note: "预解压目录随包复制，不跑 pip；节点依赖按 README 处理。", license: "未声明 LICENSE" },
    h3_vhs_node: { note: "预解压目录随包复制，不跑 pip；节点依赖按 README 处理。" },
    h3_tiny_vae: { label: "H3 预览用 TAESD (taeh3)", note: "Kijai 的 H3 2D 微型解码器（MiniMax-H3-TAE，Apache-2.0），张量与上游一致。" },
    frame_interpolation_node: { note: "RIFE 插帧节点，从 GitHub 安装（不随包复制）；权重 rife49.pth 由该节点包在首次运行时自行下载到自己的 ckpts 目录，不走 models/，所以没有单独的模型条目。装完重启 ComfyUI。" },
    esrgan_x2plus: { label: "RealESRGAN x2plus（快速放大）", note: "ESRGAN 快速放大默认模型。" },
    esrgan_anime6b: { label: "RealESRGAN anime 6B（动漫放大）", note: "面向动漫/插画的 4x 放大模型；不是快速放大工作流的默认模型，可在 ComfyUI 中替换。" },
    esrgan_x4plus: { note: "4x 快速放大；输出尺寸较大。" },
    minimax_music3_dit_fp16: { note: "音乐生成默认底模，约 4.9GB。" },
    minimax_music3_dit_int8_convrot: { note: "低显存底模，约 2.5GB；在模型选择里手动切换。" },
    minimax_music3_dit_fp32: { note: "全精度底模，约 9.8GB，显存占用最大。" },
    minimax_music3_te_pruned_int8: { label: "MiniMax Music 3 文本编码器 pruned int8 ConvRot", note: "音乐默认文本编码器，约 9.2GB。" },
    minimax_music3_te_pruned_bf16: { label: "MiniMax Music 3 文本编码器 pruned bf16", note: "非量化文本编码器，约 16.7GB。" },
    minimax_music3_te_bf16: { label: "MiniMax Music 3 文本编码器 bf16（完整）", note: "完整文本编码器，约 18.5GB，一般用不到。" },
    minimax_music3_vae: { label: "MiniMax Music 3 音频 VAE", note: "音频解码 VAE，约 207MB。" },
};

function overlay(table: Record<string, ResourceCopy>, id: string, field: ResourceCopyField) {
    // Only the Chinese UI gets the overlay; `i18n.resolvedLanguage` is what the labels helper uses
    // too, and it is the reason an English UI never shows Chinese.
    if (i18n.resolvedLanguage !== "zh-CN") return "";
    return table[id]?.[field] || "";
}

/** Chinese for one pack field, or "" when there is none (the caller keeps the English text). */
export function packZh(id: string, field: ResourceCopyField) {
    return overlay(PACK_ZH, id, field);
}

/** Chinese for one resource field, or "" when there is none. */
export function resourceZh(id: string, field: ResourceCopyField) {
    return overlay(RESOURCE_ZH, id, field);
}
