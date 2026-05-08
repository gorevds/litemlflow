#!/usr/bin/env python3
"""Seed lmf.gorev.space with realistic demo data: experiments, runs, prompts,
versioned prompts with aliases, LLM traces, and an eval comparing two models.

The aim is a UI that's actually interesting to click around: every page should
have something to look at.

Usage:
    python scripts/demo/seed.py [--url https://lmf.gorev.space]
"""

from __future__ import annotations

import argparse
import json
import os
import random
import time

import requests

from litemlflow import Client


def seed(url: str) -> None:
    print(f"[seed] target: {url}")
    c = Client(url)
    assert c.health(), "server not healthy"

    # ---------------------------------------------------------------- prompts

    print("[seed] prompts ...")
    prompts = [
        ("rag.system", "You are a helpful assistant. Answer the user's question using ONLY the provided context. If the context doesn't contain the answer, say so honestly."),
        ("rag.system", "You are a precise research assistant. Cite the source paragraph for every fact. If a fact has no source, omit it."),
        ("rag.system", "You are a helpful assistant. Answer the user's question using ONLY the provided context. Be concise — three sentences maximum. Cite sources by [n]."),
        ("rag.qa", "Context:\n{context}\n\nQuestion: {question}\n\nAnswer:"),
        ("rag.qa", "Use the following context to answer.\n\n{context}\n\nQ: {question}\nA:"),
        ("classifier.intent", "Classify the user message into one of: SUPPORT, SALES, OFFTOPIC, ABUSE.\n\nMessage: {msg}\nLabel:"),
        ("classifier.intent", "Identify the intent. Possible labels: SUPPORT (technical issue), SALES (pricing or purchase), OFFTOPIC (irrelevant), ABUSE (harassment/spam).\nReturn ONLY the label.\n\nMessage: {msg}\nLabel:"),
        ("summary.v1", "Summarize the following document in 3 bullet points.\n\n{document}\n\nSummary:"),
    ]
    last_versions: dict[str, int] = {}
    for name, content in prompts:
        v = c.create_prompt(name, content, description=f"seeded {time.strftime('%Y-%m-%d')}")
        last_versions[name] = v
        print(f"  [{name}] -> v{v}")

    # Aliases: pin "production" to a specific version, "candidate" to latest.
    c.set_prompt_alias("rag.system", "production", 2)
    c.set_prompt_alias("rag.system", "candidate", last_versions["rag.system"])
    c.set_prompt_alias("rag.qa", "production", last_versions["rag.qa"])
    c.set_prompt_alias("classifier.intent", "production", last_versions["classifier.intent"])
    print("  aliases set")

    # ----------------------------------------------------------- experiments

    print("[seed] experiments + runs ...")
    suffix = int(time.time())

    # Experiment 1: RAG quality sweep
    rag_eid = c.create_experiment(f"demo-rag-quality-{suffix}",
                                  tags={"team": "search", "domain": "wikipedia",
                                        "lmf.project": "RAG"})

    rag_runs: list[str] = []
    for i, (model, k, prompt_v) in enumerate([
        ("gpt-4o-mini", 3, 1),
        ("gpt-4o-mini", 5, 2),
        ("gpt-4o-mini", 8, 2),
        ("claude-3-5-haiku-20241022", 5, 2),
        ("claude-3-5-sonnet-20241022", 5, 3),
    ]):
        with c.start_run(rag_eid, name=f"trial-{i+1}-{model.split('-')[0]}-k{k}") as run:
            run.log_param("model", model)
            run.log_param("k", str(k))
            run.log_param("rag.system_prompt_version", str(prompt_v))
            run.log_param("retriever", "bm25" if i % 2 == 0 else "embedding")

            # Faked but plausible metrics for the dashboard.
            recall = 0.6 + 0.05 * i + random.uniform(-0.02, 0.02)
            mrr = 0.5 + 0.04 * i + random.uniform(-0.02, 0.02)
            answer_quality = 0.5 + 0.07 * i + random.uniform(-0.03, 0.03)
            for step in range(20):
                run.log_metric("eval/recall@k", recall + random.uniform(-0.01, 0.01), step=step)
                run.log_metric("eval/mrr", mrr + random.uniform(-0.01, 0.01), step=step)
                run.log_metric("eval/answer_quality", answer_quality + random.uniform(-0.005, 0.005), step=step)
                run.log_metric("latency_ms", 350 + i * 40 + random.uniform(-30, 30), step=step)
                run.log_metric("tokens.total", 800 + i * 120 + random.uniform(-50, 50), step=step)

            # Trace: pipeline → retrieve → generate
            tid = c.start_trace()
            t0 = time.time_ns()
            pipeline = c.log_span(tid, "rag.pipeline", run_id=run.id,
                                  start_time_ns=t0,
                                  end_time_ns=t0 + 800_000_000,
                                  attrs={"k": k, "model": model})
            c.log_span(tid, "retrieve", run_id=run.id, parent_id=pipeline,
                       start_time_ns=t0 + 10_000_000,
                       end_time_ns=t0 + 90_000_000,
                       attrs={"top_k": k, "index": "wiki-2024-q1", "docs": k})
            c.log_span(tid, "rerank", run_id=run.id, parent_id=pipeline,
                       start_time_ns=t0 + 90_000_000,
                       end_time_ns=t0 + 170_000_000,
                       attrs={"reranker": "bge-reranker-v2-m3"})
            c.log_span(tid, "generate", run_id=run.id, parent_id=pipeline,
                       start_time_ns=t0 + 170_000_000,
                       end_time_ns=t0 + 790_000_000,
                       attrs={"model": model, "prompt_tokens": 800 + i * 120,
                              "completion_tokens": 220 + i * 30})

            rag_runs.append(run.id)
        print(f"  [rag] run {i+1}/{len(rag_runs) if rag_runs else 5}: {run.id[:8]} model={model} k={k}")

    # Experiment 2: intent classifier
    cls_eid = c.create_experiment(f"demo-intent-classifier-{suffix}",
                                  tags={"team": "support", "task": "classification",
                                        "lmf.project": "Classification"})
    cls_runs: list[str] = []
    for i, model in enumerate(["gpt-4o-mini", "claude-3-5-haiku-20241022", "gpt-4o"]):
        with c.start_run(cls_eid, name=f"intent-{model.split('-')[0]}") as run:
            run.log_param("model", model)
            run.log_param("prompt_version", "2")
            f1 = 0.78 + i * 0.04 + random.uniform(-0.01, 0.01)
            for step in range(10):
                run.log_metric("eval/f1_macro", f1 + random.uniform(-0.005, 0.005), step=step)
                run.log_metric("eval/accuracy", f1 + 0.03 + random.uniform(-0.005, 0.005), step=step)
                run.log_metric("latency_ms", 90 + i * 25 + random.uniform(-10, 10), step=step)
            cls_runs.append(run.id)
        print(f"  [cls] {model}: {run.id[:8]}")

    # Experiment 3: classic ML — sklearn-style hyperparam sweep, no LLM
    sk_eid = c.create_experiment(f"demo-sklearn-iris-{suffix}",
                                 tags={"team": "ml", "domain": "tabular",
                                       "lmf.project": "Tabular ML"})
    for i, c_param in enumerate([0.01, 0.1, 1.0, 10.0]):
        with c.start_run(sk_eid, name=f"lr-C={c_param}") as run:
            run.log_param("model", "LogisticRegression")
            run.log_param("C", str(c_param))
            run.log_param("max_iter", "200")
            for step in range(30):
                acc = 0.85 + 0.03 * i - 0.01 * (step / 30) ** 2 + random.uniform(-0.005, 0.005)
                loss = 0.4 - 0.06 * i + 0.2 * (1 - step / 30) + random.uniform(-0.01, 0.01)
                run.log_metric("acc", acc, step=step)
                run.log_metric("loss", max(loss, 0.01), step=step)
        print(f"  [sklearn] C={c_param}: {run.id[:8]}")

    # ---------------------------------------------------------------- evals

    print("[seed] eval run ...")
    with c.start_run(rag_eid, name="eval-2026-05-08") as eval_run:
        eval_run.log_param("dataset", "hf://allenai/squad/validation")
        eval_run.log_param("n", "500")
        c.create_eval(
            eval_run.id,
            target_run_ids=rag_runs[-2:],   # compare best two RAG runs
            dataset_ref="hf://allenai/squad/validation",
            score=0.79,
            metrics={
                "best_run_id": rag_runs[-1],
                "delta": 0.04,
                "exact_match": 0.71,
                "f1": 0.79,
            },
        )
    print(f"  eval -> {eval_run.id[:8]}")

    # ----------------------------------------------------------- summary

    print("\n[seed] DONE")
    print(f"  experiments: 3 (rag={rag_eid}, classifier={cls_eid}, sklearn={sk_eid})")
    print(f"  prompts:    8 versions across 4 names with 4 aliases")
    print(f"  rag runs:   {len(rag_runs)} (with traces)")
    print(f"  cls runs:   {len(cls_runs)}")
    print(f"  sklearn runs: 4")
    print(f"  eval runs:  1")
    print(f"\nVisit: {url}/")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--url", default=os.environ.get("LITEMLFLOW_URL", "https://lmf.gorev.space"))
    args = ap.parse_args()
    seed(args.url)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
