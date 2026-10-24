#!/usr/bin/env python3
"""Turns k6's --summary-export JSON into the markdown table used in README."""
import json
import sys

data = json.load(open(sys.argv[1]))
metrics = data["metrics"]

print("| Senaryo | İstek | p50 (ms) | p95 (ms) | p99 (ms) | maks (ms) | Hata oranı |")
print("|---|---:|---:|---:|---:|---:|---:|")
for sc in ["ingest", "read_24h", "dashboard", "trace"]:
    d = metrics.get(f"http_req_duration{{scenario:{sc}}}")
    f = metrics.get(f"http_req_failed{{scenario:{sc}}}")
    if not d:
        continue
    reqs = (f or {}).get("passes", 0) + (f or {}).get("fails", 0)
    rate = (f or {}).get("value", 0) * 100
    print(f"| {sc} | {reqs} | {d['med']:.1f} | {d['p(95)']:.1f} | {d['p(99)']:.1f} | {d['max']:.1f} | {rate:.2f} % |")
total = metrics["http_reqs"]
print(f"\nToplam {total['count']} istek, ortalama {total['rate']:.0f} istek/sn, "
      f"genel hata oranı {metrics['http_req_failed']['value'] * 100:.2f} %.")
