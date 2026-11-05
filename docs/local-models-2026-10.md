<!-- Research report produced by a sub-agent on 2026-10-12. Mixed-quality web sources; figures unverified unless the research log says otherwise. -->
# Local model and speed research (2026-10-12)

Caveat: most sources are aggregator/blog pages of mixed quality; vendor-reported numbers are marked. Nothing here was verified on our hardware. Items marked (?) are uncertain or from a single weak source.

## 1. Candidates (fit in ~38 GB at 4-bit)

Does NOT fit: GLM-5.3-Flash (320B total / 18B active, MIT, 1M ctx, released 2026-08-26; smallest useful GGUF 1-bit = 86.7 GiB, Q4_K_XL = 186 GiB). Qwen3.8-Flash-Next (~180B, 6B active, 2026-08-24), Mistral Small 4 (119B-A6B, Mar 2026), Nemotron 3 Ultra 550B, Kimi K3, DeepSeek V4.x: all too big. These are hosted-only for us.

| Model | Total / active | Context | License | 4-bit size | Notes vs Qwen3.8-27B | Builds |
|---|---|---|---|---|---|---|
| Qwen3.8-27B (baseline, 2026-08-05) | 27B dense | 262K (to 1M) | Apache 2.0 | 16.8-17.1 GB GGUF Q4_K_M; 16.1 GB MLX 4-bit | Artificial Analysis index v4.3.2: 33.70 | ggml-org GGUF (?), Ollama `qwen3.8:27b-mlx` (~18 GB) |
| Nemotron 3.5 Lightning 30B-A3B (2026-08-11) | 30B / ~3-3.6B, Mamba-2 hybrid MoE | up to 1M | OpenMDW-1.1 | ~20-25 GB (Q4); 23.7 GB reported | IFBench loose 71.88 vs Qwen3.6 63.71 (vendor); but loses 11 of 12 other benchmarks vs Qwen3.6-35B-A3B; llm-stats says Qwen3.8-27B wins IFBench vs it (conflicting). MMLU-Pro 81.9, SWE-bench V 51.6. ~4x faster generation on NVIDIA | `unsloth/NVIDIA-Nemotron-3.5-Lightning-30B-A3B-GGUF` (UD-Q4_K_XL); Ollama `nemotron-3.5-lightning:30b`; MLX not confirmed; community GGUFs warn of unused tensors (Mamba-2 support incomplete) (?) |
| Gemma 4 26B-A4B (2026-03-31, ~6.5 months; borderline) | 25.2B / 3.8B | 256K | Apache 2.0 | 14-15.6 GB | Billed as best doc/RAG model; 31B dense got multi-needle retrieval 13.5% -> 66.4% vs Gemma 3 (different family, no direct Qwen3.8 comparison found) | Ollama `gemma4:26b`; MLX community 4-bit 15.3 GB |
| Gemma 4 31B dense | 30.7B | 256K | Apache 2.0 | ~20 GB Q4_K_M | No IFEval/extraction numbers found; AA comparisons exist vs Qwen3.7 Max | Ollama `gemma4:31b`; MLX community |
| Qwen3.6-35B-A3B (Apr 2026, already have) | 35B / 3B | 256K | Apache 2.0 | ~20 GB | Lower quality per your tests | have it |
| Qwen3.6-27B (dense) | 27B | 262K | Apache 2.0 | ~17 GB | Predecessor; not worth testing | have |
| GLM-4.7-Flash (older, ~Jan 2026, outside 6-month window) | 30B / 3B | 128K | MIT (?) | ~16 GB | Only "small GLM" found; no GLM-5.x small variant found | MLX 6/8-bit lmstudio-community; GGUF unsloth |
| Laguna XS 2.1, Nemotron Cascade 2 30B | ~30B | ? | ? | ~32 GB at Q8 | Mentioned in one roundup as strong for JSON/structured output (?, unverified) | not checked |
| gpt-oss-20b | 21B / 3.6B | 128K | Apache 2.0 | ~12 GB | Old (Aug 2025), outside window | Ollama `gpt-oss:20b` |

No Phi, Kimi, or DeepSeek variant small enough was found in the window. No independent extraction-style or long-context-faithfulness benchmark comparing these was found; IFBench is the only relevant number, and it is vendor-reported.

## 2. Measured speeds on Apple Silicon

- stared/benching-local-llms-on-apple-silicon (M5 Max 128 GB, 2026-06-14; Qwen3.6 not 3.8): 35B-A3B llama.cpp 93 (105 with MTP) vs MLX 85; 27B dense llama.cpp 18 (32 with MTP) vs MLX 17. llama.cpp beat MLX in that test by 10-24%.
- contextstudios Qwen3.8-27B guide: M5 Pro/Max GGUF 28-31 tok/s; MLX conversion ~40 tok/s on M5 Pro (+40%); M5 Max 4k prefill baseline 834 tok/s. Conflicts with the stared result, so engine winner is unsettled (MLX seems ahead for 3.8).
- Qwen3.5-27B on MLX, M5 Pro 20-core 48 GB: prefill 437.6 tok/s, decode 18.2 at 1k ctx (an MLX benchmark; so even MLX prefill is only ~440 on your chip).
- Unreleased Ollama MLX port: 43.67 tok/s on M5 Pro for Qwen3.8-27B (single source, (?)).
- Gemma 4 31B Q4: M3/M4 Pro 36 GB 20-35 tok/s; M4 Max 40-50.
- Nemotron 3.5 Lightning: no Apple Silicon numbers found (~42 t/s claimed on a 48 GB machine in one roundup (?)).
- Note: your 20 tok/s with MTP is below the 28-32 reported on M5 Max (more bandwidth; M5 Pro ~300 GB/s is plausible for 20-28).

## 3. Speed techniques

**DFlash2 drafter** (block-diffusion drafter, 5 layers, z-lab / incoai):
- HF: `z-lab/Qwen3.8-27B-DFlash2` (mirror of `incoai/Qwen3.8-27B-DFlash2`); GGUF `z-lab/Qwen3.8-27B-DFlash2-GGUF` (Q8_0 2.06 GB; Q4_K_M 1.1 GB). One source warns incoai conversions give broken output in llama.cpp. Target `ggml-org/Qwen3.8-27B-GGUF` (?).
- llama.cpp: merged ~2026-08-27 (PR #27342, then #27816; first tag b10658). Flags: `--spec-type draft-dflash -hfd <draft> --spec-draft-n-max 7` (5 reported optimal on CUDA). Your "0.6.0" version string does not look like a b-number; check you have >= b10658.
- Speedups: SGLang/vLLM concurrency 1: 2.7-3.4x; concurrency 8: 2.27-2.85x; concurrency 32: 1.01-1.45x. llama.cpp CUDA (RTX PRO 6000): 2.26x on real coding, concurrency 1 only. Only M5 Pro data point: 1.81x (10.42 -> 18.89 tok/s) from one blog (?). A search snippet claims no speedup on Metal for MTP on M1 Max; for DFlash on Metal this is unclear.
- Accuracy: greedy is lossless in theory; not re-verified by third parties for 3.8 llama.cpp. Only greedy requests drafted in vllm-metal.
- DFlash2 MLX variant exists: `cfontes/qwen3.8-27b-mlx-dflash2` (not read; page redirected).
- DSpark: not supported in llama.cpp. EAGLE: nothing specific found for 3.8.
- Dense 27B at batch 8 is still bandwidth-bound by weights, so the gain from spec decode shrinks as batch grows; your 8-way 40 tok/s aggregate is likely near the point where DFlash adds little. Test at n=1,4,8.

**MLX servers:**
- vllm-metal (docs.vllm.ai/projects/vllm-metal): spec decode supports Gemma4 MTP and plain draft-model only; docs explicitly exclude hybrid GDN targets, so Qwen3.8 speculative decoding is NOT supported there. Requires `--no-async-scheduling`. Structured output via xgrammar/guidance in vLLM generally (Metal specifics unverified).
- vllm-mlx (waybarrios; arXiv 2601.19139): continuous batching, 21-87% over llama.cpp, 4.3x aggregate at 16 concurrent; license and Qwen3.8/DFlash support not confirmed.
- oMLX (jundot/omlx, Apache 2.0, DMG or source, macOS 15+): continuous batching (4.14x at 8 concurrent), paged SSD KV cache, any MLX model; spec decode and structured output not documented.

**Why prefill is ~250 tok/s on llama.cpp (diagnosis, not confirmed on your box):**
1. Metal 4 tensor API (M5 Neural Accelerators) not enabled. In LM Studio's bundled llama.cpp it is disabled because binaries are built with minos 14 / SDK 14.5, so shaders compile as Metal 3.2; upstream release binaries enable it and get 2-2.4x faster prefill (gpt-oss-120b 877 -> 1833 tok/s) with unchanged decode (lmstudio-bug-tracker #2040). Check the llama.cpp startup log for "tensor API ... disabling"; build from source on macOS 26 SDK, or use a recent upstream release.
2. Batch size: with Qwen3.6-27B a 4096 batch dropped prefill ~620 -> ~299 tok/s; reducing `-b`/`-ub` to 1024 fixed it (llama.cpp discussion #24711; was on AMD, but worth a try; try -ub 512/1024/2048).
3. Hybrid GatedDeltaNet recurrent layers are slow to prefill relative to pure attention; a Metal GDN kernel was added to llama.cpp, so use a current build. Also consider `-fa on`, avoid quantized KV cache on Metal, and cache the 30k brief prefix (`--cache-reuse`, slot prompt cache) since the brief is shared across excerpts: this removes most prefill cost regardless.
4. MLX prefill is also only ~440 tok/s on M5 Pro 48 GB for a 27B dense, so expect at most ~2x gain; prefix caching matters more.

## 4. Cheap screening (Baseten Model APIs, docs.baseten.co, fetched today)

Listed: DeepSeek V4 Pro/Flash/V4.1 Flash(+Fast), GLM 5.2 (+Fast), GLM 5.3 (+Fast), `zai-org/GLM-5.3-Flash`, Kimi K3, Nemotron 3 Ultra, gpt-oss-120b. All support structured outputs/JSON mode. Qwen3.8, Nemotron 3.5 Lightning and Gemma 4 are NOT on the fetched current list (a search snippet claimed Gemma 4 / Qwen3.6 27B exist in the library; may be dedicated deployments). Screening options for the local candidates: NVIDIA NIM (`nvidia-nemotron-3-5-lightning-30b-a3b`, docs.api.nvidia.com), OpenRouter (lists qwen3.8-27b, nemotron-3.5-lightning, gemma-4-31b-it), Gemma 4 on OpenRouter ($0.09/$0.34 per M). Caveat: hosted APIs run different quants than a 4-bit local build.

## Ranked shortlist to test

1. Qwen3.8-27B MLX 4-bit (already have) via oMLX or mlx-lm with prefix caching: same quality, test for speed (decode ~40 tok/s, better batching).
2. Nemotron 3.5 Lightning 30B-A3B (unsloth UD-Q4_K_XL): best IFBench claim, MoE speed, 1M context; risk is extraction quality and immature llama.cpp Mamba-2 support.
3. Gemma 4 31B dense (Q4/MLX 4-bit): strong document/RAG reputation, dense so ~20-35 tok/s; best bet for precise number/speaker reading among non-Qwen.
4. Gemma 4 26B-A4B (MLX 4-bit, ~15 GB): fast MoE alternative, can run 8+ streams with headroom.
5. Qwen3.8-27B at higher precision (Q6/Q8 or MLX 6/8-bit, ~22-29 GB): cheaper way to cut wrong/invented items than switching families; fits 38 GB with KV cache for 8 slots only tightly.
6. (Optional) GLM-5.3-Flash on Baseten as a quality ceiling check only; it cannot run locally.

## Three most promising speed changes

1. Fix prefill: confirm Metal tensor API is active (startup log), use a current upstream llama.cpp build on macOS 26 SDK, set `-ub/-b` 512-1024, and reuse the shared 30k-token brief via prompt/KV cache across excerpts (largest win; can eliminate repeated prefill).
2. Try the Qwen3.8 MLX 4-bit build (~40 tok/s single stream per one source) on oMLX or vllm-mlx for continuous batching at 8 streams (reported 4x aggregate); validate JSON output reliability (use schema/grammar or post-validate).
3. Add DFlash2 drafter in llama.cpp (`--spec-type draft-dflash`, GGUF drafter ~1-2 GB, n-max 5-7), replacing or compared with MTP; likely helps most at 1-4 streams and little at 8; measure on Metal before adopting.

Key sources: stared/benching-local-llms-on-apple-silicon (GitHub); github.com/lmstudio-ai/lmstudio-bug-tracker/issues/2040; github.com/ggml-org/llama.cpp/discussions/24711; github.com/lukaLLM/DFlash2_Qwen3.8_3.6_27B_LlamaCPP; huggingface.co/z-lab/Qwen3.8-27B-DFlash2; runaihome.com DFlash2 guide; docs.vllm.ai/projects/vllm-metal/en/latest/speculative_decoding; omlx.ai; arxiv.org/abs/2601.19139; docs.baseten.co/inference/model-apis/overview; codersera.com (Qwen3.8 lineup, GLM-5.3-Flash); toolhalla.ai GLM-5.3-Flash local; unsloth.ai/docs nemotron-3.5; aireiter.com Nemotron guide; sudoall.com Gemma 4 guide; contextstudios.ai Qwen3.8 hardware guide.

## Addendum 2026-11-05: Qwen3.6-35B-A3B variants for Live

Desk research, not measured here; figures are uploader- or community-reported. Question: is there a faster or lighter build of qwen3.6:35b-a3b for Live suggestions (installed today: Ollama Q4_K_M, 22 GB, llama.cpp engine)? It has never been tested for Live; only for extraction (research log, 2026-10-11).

What matters for Live: a call is ~520 tokens in and a short reply out, so time to first token (prefill) matters more than decode speed. Speculative add-ons (MTP, DFlash) mostly help long outputs.

| Variant | Size | Engine | Claim |
|---|---|---|---|
| Ollama `qwen3.6:35b-a3b-nvfp4` / `qwen3.6:35b-mlx` | 23 GB | Ollama MLX engine | Ollama reported 58 → 112 tok/s decode for the similar Qwen3.5-35B-A3B; reportedly text-only on this engine |
| mlx-community 4-bit / 4-bit DWQ, unsloth UD-MLX-4bit | 20–22 GB | MLX | oMLX community runs on M5 Pro: ~70–77 tok/s decode, ~2,100 tok/s prefill |
| `Jundot/Qwen3.6-35B-A3B-oQ4e-mtp` | 22 GB | oMLX | Sensitivity-based mixed precision plus MTP head; re-quantized 2026-10-29 |
| MTPLX-optimized 4-bit | ~21 GB | MLX | ~98–102 tok/s decode on M5 Pro (MTPLX gained nothing on our 27B) |
| **`mlx-community/Qwen3.6-35B-A3B-OptiQ-4bit-REAP-19B`** | 13.8 GB (11.6 GB peak) | MLX / mlx-optiq | Half the experts pruned (REAP); decode 54.7 vs 47; IFEval +4.3, MMLU −21.4 |
| `unsloth/Qwen3.6-35B-A3B-UD-MLX-3bit` | 17.4 GB | MLX | Unsloth dynamic 3-bit |
| `mudler/Qwen3.6-35B-A3B-APEX-GGUF` I-Compact / I-Mini | 17 / 14 GB | llama.cpp | Per-expert mixed precision; I-Compact KL ≈ UD-Q3_K_M |
| `byteshape/Qwen3.6-35B-A3B-GGUF` IQ3_S / IQ2_S | 13 / 9.4 GB | llama.cpp | Learned per-tensor datatypes; benchmarked on NVIDIA only |
| `chtisgit/Qwen3.6-35B-A3B-2Bit-GSQ-hybrid-GGUF` | 12.2 GB | llama.cpp (Q2_0, recent builds) | DASLab GSQ 2-bit experts; Metal support not stated |
| Drafters: `z-lab/...-DFlash`, `mlx-community/...-DFlash2-4bit`, `RedHatAI/...-speculator.dspark` | 0.5–2 GB | MLX / llama.cpp | DFlash on MLX ~2.2x at 1k context, ~1.3x at 8k; others report 1.05x on chat |

Ruled out: Escha-W2 (NVIDIA/Linux only); a pruned GGUF repo that bundles prebuilt llama.cpp binaries from an unknown uploader; a 50%-pruned build calibrated on code; uncensored/abliterated and distilled fine-tunes (change behaviour). MTP heads via Ollama were slower in one Metal test (34.6 vs 46.2 tok/s).

Shortlist for the 40-prompt Live test (vs qwen3:8b and the installed Q4_K_M): Ollama nvfp4 (no code change), OptiQ REAP-19B (lighter and faster; check the knowledge loss on query quality), unsloth UD-MLX-3bit. The MLX builds need a separate server (mlx-lm or optiq), not Ollama.

Network note: Hugging Face downloads work through the corporate proxy (~7.7 MB/s on 2026-11-05) but range requests are ignored, so interrupted downloads restart.

Sources: ollama.com/library/qwen3.6/tags; omlx.ai community benchmarks; huggingface.co model cards for each repo named above; levelup.gitconnected.com (Ollama MLX 58 → 112 tok/s); huggingface.co/datasets/RaynarDM/apple-silicon-llm-benchmarks (MTP on Metal); github.com/bstnxbt/dflash-mlx.
