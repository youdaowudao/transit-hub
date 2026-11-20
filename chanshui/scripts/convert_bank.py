#!/usr/bin/env python3
"""Convert lm-detector unified_bank.json + LLMprobe v3e snapshot into bank.generated.json.

Field semantics are 1:1 verbatim — no matrix is recomputed here. Only model IDs
are mapped to the names this project already used, so saved reports and
requested Anthropic names keep matching.
Run from the repo root:  python scripts/convert_bank.py
"""
import copy
import json
import os
import sys
import shutil

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

NUMERIC_KEEP_MODEL = ("id", "display_name", "family", "family_name", "counts")
ROBUST_KEEP = ("model_order", "hellinger", "ordered_blocks")
HELLINGER_KEEP = ("feature_mean", "feature_scale", "nuisance_basis", "centroids")
ORDERED_KEEP = (
    "weight",
    "feature_mean",
    "feature_scale",
    "nuisance_basis",
    "centroids",
    "environment_centroids",
)
CALIBRATION_KEEP = ("beta", "cv_accuracy")
BEHAVIORAL_KEEP = ("modelId", "family", "displayName", "refusalLadder", "formatting", "uncertainty", "sampleSize", "updatedAt")

# lm-detector writes Claude versions with dots; Anthropic request names and
# the previous bank use dashes. Haiku 4.5 keeps its dated ID from the old bank.
ID_OVERRIDES = {"claude-haiku-4.5": "claude-haiku-4-5-20251001"}
# lm-detector merged these two because their numbers cannot be told apart.
EXTRA_ALIASES = {"gpt-6-astra": ["gpt-6.1-sol"]}


def local_id(model):
    upstream = model["id"]
    if upstream in ID_OVERRIDES:
        return ID_OVERRIDES[upstream]
    if model["family"] == "claude":
        return upstream.replace(".", "-")
    return upstream


def convert_model(model):
    out = {k: copy.deepcopy(model[k]) for k in NUMERIC_KEEP_MODEL}
    out["id"] = local_id(model)
    aliases = [model["id"]] if out["id"] != model["id"] else []
    aliases += EXTRA_ALIASES.get(model["id"], [])
    if aliases:
        out["aliases"] = aliases
    return out


def load(path):
    with open(path, encoding="utf-8") as fh:
        return json.load(fh)


def build():
    bank = load(os.path.join(ROOT, "data_ref", "unified_bank.json"))
    probe = load(os.path.join(ROOT, "data_ref", "probe_v3e_snapshot.json"))

    assert [m["id"] for m in bank["models"]] == bank["robust"]["model_order"], "model order"
    models = [convert_model(m) for m in bank["models"]]

    hell = {k: copy.deepcopy(bank["robust"]["hellinger"][k]) for k in HELLINGER_KEEP}
    ordered = {k: copy.deepcopy(bank["robust"]["ordered_blocks"][k]) for k in ORDERED_KEEP}

    calibration = {
        k: {kk: v[kk] for kk in CALIBRATION_KEEP}
        for k, v in bank["calibration"].items()
    }

    numeric = {
        "models": models,
        "robust": {"model_order": [m["id"] for m in models], "hellinger": hell, "ordered_blocks": ordered},
        "calibration": calibration,
        "method": copy.deepcopy(bank["method"]),
        "minimum_valid_numbers": bank["minimum_valid_numbers"],
    }

    behavioral_models = []
    for b in probe["baselines"]:
        behavioral_models.append({k: copy.deepcopy(b[k]) for k in BEHAVIORAL_KEEP if k in b})

    result = {"numeric": numeric, "behavioral": {"models": behavioral_models}}
    provenance = os.path.join(ROOT, "data_ref", "provenance.json")
    if os.path.exists(provenance):
        result["source"] = load(provenance)
    return result


def verify(out):
    num = out["numeric"]
    models = num["models"]
    assert len(models) >= 16, f"numeric models: {len(models)}"
    names = set()
    for m in models:
        assert len(m["counts"]) == 355, m["id"]
        for name in [m["id"], *m.get("aliases", [])]:
            assert name not in names, f"duplicate model name: {name}"
            names.add(name)
    assert num["robust"]["model_order"] == [m["id"] for m in models], "model order"
    hell = num["robust"]["hellinger"]
    ordered = num["robust"]["ordered_blocks"]
    assert len(hell["feature_mean"]) == 355
    assert len(hell["feature_scale"]) == 355
    assert len(hell["nuisance_basis"]) == 2 and len(hell["nuisance_basis"][0]) == 355
    assert len(hell["centroids"]) == len(models) and len(hell["centroids"][0]) == 355
    assert len(ordered["feature_mean"]) == 74
    assert len(ordered["feature_scale"]) == 74
    assert len(ordered["nuisance_basis"]) == 2 and len(ordered["nuisance_basis"][0]) == 74
    assert len(ordered["centroids"]) == len(models) and len(ordered["centroids"][0]) == 74
    assert ordered["environment_centroids"], "environment centroids"
    for env in ordered["environment_centroids"]:
        assert len(env) == len(models) and all(len(row) == 74 for row in env), "environment centroids"
    assert set(num["calibration"]) == {"1", "2", "3"}, num["calibration"].keys()
    behav = out["behavioral"]["models"]
    assert len(behav) == 34, f"behavioral models: {len(behav)}"
    for b in behav:
        assert "modelId" in b and "family" in b, b


def main():
    out = build()
    verify(out)
    dest = os.path.join(ROOT, "internal", "fingerprint", "bank.generated.json")
    os.makedirs(os.path.dirname(dest), exist_ok=True)
    if os.path.exists(dest):
        backup = os.path.join(ROOT, ".artifacts", "bank-before-conversion.json")
        os.makedirs(os.path.dirname(backup), exist_ok=True)
        shutil.copy2(dest, backup)
    temporary = dest + ".tmp"
    with open(temporary, "w", encoding="utf-8", newline="\n") as fh:
        json.dump(out, fh, ensure_ascii=False, separators=(",", ":"))
    os.replace(temporary, dest)
    print(f"wrote {dest} ({os.path.getsize(dest)} bytes)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
