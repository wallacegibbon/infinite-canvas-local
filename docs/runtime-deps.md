# Runtime libraries must be installed by hand

A few things here are **not model files** but Python packages ComfyUI needs at runtime.
The workbench **will not install them for you**. It used to `pip install` into ComfyUI's
Python environment and regularly broke the venv (wrong version, overwritten torch
dependencies, half-installed builds after a compile failure), so this is now a command
you run yourself.

> Before installing, make sure you are installing into **the Python ComfyUI actually runs** —
> the workbench does not guess it: the commands below carry `/path/to/ComfyUI/venv/bin/python`
> as a placeholder for you to replace. `ps aux | grep main.py` shows the interpreter ComfyUI was
> started with. **Do not** use the system `python3`, and not the canvas's Node environment.

Restart ComfyUI after installing.

---

## H3 video / VDN (required)

```bash
"<ComfyUI's python>" -m pip install sageattention
```

SageAttention is a hard dependency of the H3 and VDN workflows. Without it ComfyUI reports:

```
sageattention is not new enough
cannot apply MiniMax H3 Memory Efficient Sage Attention Patch
```

It has to be compiled for your CUDA version, which can take a while. If pip cannot build it,
grab a prebuilt wheel for your CUDA / Python combination from the
[SageAttention releases page](https://github.com/woct0rdho/SageAttention/releases).

Verify:

```bash
"<ComfyUI's python>" -c "import sageattention; print(sageattention.__version__)"
```

## SeedVR2 upscaling

```bash
"<ComfyUI's python>" -m pip install diffusers omegaconf rotary_embedding_torch opencv-python einops
```

The SeedVR2 nodes need these packages to load. Without them ComfyUI's startup log shows
`IMPORT FAILED`, and the canvas reports "missing node SeedVR2LoadDiTModel".

Verify:

```bash
"<ComfyUI's python>" -c "import diffusers, omegaconf, rotary_embedding_torch, cv2, einops; print('ok')"
```

---

## Why automatic installation is gone

The old implementation ran `pip install` itself (complete with wheel-matrix selection,
rollback on failure, and site-packages backups). What went wrong:

- Picking the wrong wheel — CUDA version, Python version and torch version all have to line up
- Installs that reported success but failed on `import`, leaving a half-installed package
- Overwriting an existing torch / numpy and breaking ComfyUI itself
- A mandatory ComfyUI restart afterwards, which interrupts whatever is currently running

Handing you one command means the environment is yours to own; the workbench only has to
tell you what to install.
