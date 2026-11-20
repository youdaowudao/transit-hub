# Third-Party Notices

This project contains code, model references and captured model outputs from several sources. The MIT license in `LICENSE` applies to this project's code; it does not relicense third-party data, model names or dependencies.

## lm-detector numeric bank

The embedded numeric bank is derived from `data/unified_bank.json` of Ikaleio/lm-detector, the maintained successor of ModelTrace by the same author, at the commit recorded in `data_ref/provenance.json`. `data_ref/unified_bank.json` is that file with `models[].frequency_references` removed; `scripts/convert_bank.py` keeps the matrices verbatim and only maps Claude IDs to the dash names this project already used. The repository declares MIT; its notice is retained in `data_ref/LM_DETECTOR_LICENSE`. Each model's `sources` field records the third-party channel its reference answers came from; those channels are not independent proof of model identity.

## ModelTrace numeric baselines

The previous ModelTrace bank (commit `6cd4a8ff0f3f4e49d6d6969dd2fbadfa1569fde2`) is kept only as test data in `internal/fingerprint/testdata/modeltrace-unified-bank.json`, so the Go port is still checked against ModelTrace `fingerprint.py` output. It is no longer embedded.

## DeepSeek samples

The dated `DeepSeek-V4.1-Flash-0910` and `DeepSeek-V4.1-Flash-1017` captures were made through `https://api.deepseek.com` with the request name `deepseek-flash`. They remain under `data_ref/captures/` as capture records and are no longer embedded; the bank uses lm-detector's DeepSeek references. The dated names are capture labels, not an official version claim by the API. See `docs/deepseek-fingerprint-2026-10-05.md`.

## Grok samples

The Grok 4.6/4.7 captures under `data_ref/captures/` were made through a third-party gateway, not an official xAI endpoint. They are capture records only and are not embedded. See `docs/grok-fingerprints-2026-10-05.md`.

## Dependencies

Go dependencies are listed in `go.mod` / `go.sum`; frontend dependencies are listed in `web/package.json` / `web/package-lock.json`. Each dependency remains under its own license. Review those licenses before distributing a bundled release.

## Trademarks

Claude, GPT, OpenAI, DeepSeek, Grok, xAI, Gemini, GLM, Unity, Unreal and other names are trademarks of their respective owners. This project is not affiliated with or endorsed by those owners.

## No warranty

Model fingerprints, cache measurements, tool/web checks and scores are evidence, not guarantees of a provider's identity, quality or legality. See `LICENSE` and `SECURITY.md`.

## TransitHub distribution attribution

The referenced ModelTrace revision `d4131b30243dfa05e70180b5eedde742103f1d73` declares MIT. Its original copyright and permission notice is retained in `data_ref/MODELTRACE_LICENSE`; the lm-detector notice is retained in `data_ref/LM_DETECTOR_LICENSE`. This does not change the other source notices above or claim ownership of model outputs.

## Codex prompt references

The Codex prompts in `internal/hub/prefixcheck/library` come from OpenAI Codex (Apache License 2.0), via Sub2API revision `be00b83e3`. The manifest identifies the older Sub2API placeholder separately.

## Offline tokenizer

`github.com/pkoukk/tiktoken-go` and `github.com/pkoukk/tiktoken-go-loader` are MIT-licensed. The latter embeds OpenAI tiktoken vocabulary files.
