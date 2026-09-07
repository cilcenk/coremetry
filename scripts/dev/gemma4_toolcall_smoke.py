#!/usr/bin/env python3
"""
gemma4_toolcall_smoke.py — Gemma 4 (google/gemma-4-31b-it) tool-calling
doğrulaması, OpenAI-uyumlu /v1/chat/completions ucu üzerinden.

NEDEN: Gemma 4 function calling'i özel token'lı kendi protokolünü kullanıyor;
OpenAI-uyumlu sunucularda (vLLM, SGLang, llama.cpp…) parser eksikse çağrı
`tool_calls` yerine `content` içinde METİN olarak kalır ve yanıt sessizce
"tool çağırmadı" gibi görünür. Qwen3'te düşünme fazının max_tokens'ı yiyip
boş içerik döndürmesi de aynı sınıf. Varsaymıyoruz, ölçüyoruz.

BAĞIMLILIK: yalnız `requests` (yoksa stdlib urllib ile aynı istekler atılır;
ek paket gerekmez). Python ≥ 3.9.

ORTAM:
  GEMMA_BASE_URL   zorunlu — ör. http://vllm:8000/v1  (COREMETRY_EVAL_BASE_URL de okunur)
  GEMMA_MODEL      varsayılan google/gemma-4-31b-it   (COREMETRY_EVAL_MODEL de okunur)
  GEMMA_API_KEY    isteğe bağlı Bearer
  GEMMA_TIMEOUT    saniye, varsayılan 120
  GEMMA_MAX_TOKENS varsayılan 1024
  GEMMA_LOW_MAX_TOKENS düşük bütçe deneyi, varsayılan 48
  GEMMA_INSECURE=1 TLS doğrulamasını atla (öz-imzalı)

KULLANIM:
  python3 scripts/dev/gemma4_toolcall_smoke.py [--only a,b,c,d,e,f] [--json-out out.json]

ÇIKIŞ: 0 tüm testler koştu · 1 en az bir HTTP/transport hatası · 2 ortam eksik.
Ham yanıtlar olduğu gibi basılır (kısaltılmaz); sonda markdown tablo.
"""
from __future__ import annotations

import argparse
import json
import os
import re
import sys
import time
from typing import Any

try:  # bağımlılık: requests; yoksa stdlib
    import requests  # type: ignore

    def _post(url: str, headers: dict, body: dict, timeout: float, insecure: bool) -> tuple[int, str]:
        r = requests.post(url, headers=headers, json=body, timeout=timeout, verify=not insecure)
        return r.status_code, r.text
except ImportError:  # pragma: no cover
    import ssl
    import urllib.error
    import urllib.request

    def _post(url: str, headers: dict, body: dict, timeout: float, insecure: bool) -> tuple[int, str]:
        data = json.dumps(body).encode("utf-8")
        req = urllib.request.Request(url, data=data, headers={**headers, "Content-Type": "application/json"}, method="POST")
        ctx = ssl._create_unverified_context() if insecure else None
        try:
            with urllib.request.urlopen(req, timeout=timeout, context=ctx) as resp:
                return resp.status, resp.read().decode("utf-8", "replace")
        except urllib.error.HTTPError as e:
            return e.code, e.read().decode("utf-8", "replace")


# ── ortam ────────────────────────────────────────────────────────────────
BASE = (os.getenv("GEMMA_BASE_URL") or os.getenv("COREMETRY_EVAL_BASE_URL") or "").rstrip("/")
MODEL = os.getenv("GEMMA_MODEL") or os.getenv("COREMETRY_EVAL_MODEL") or "google/gemma-4-31b-it"
API_KEY = os.getenv("GEMMA_API_KEY") or os.getenv("COREMETRY_EVAL_API_KEY") or ""
TIMEOUT = float(os.getenv("GEMMA_TIMEOUT") or "120")
MAX_TOKENS = int(os.getenv("GEMMA_MAX_TOKENS") or "1024")
LOW_MAX_TOKENS = int(os.getenv("GEMMA_LOW_MAX_TOKENS") or "48")
INSECURE = os.getenv("GEMMA_INSECURE") == "1"

# ── tool tanımları (ilk dikey dilimin ikisi) ─────────────────────────────
TOOL_RESOLVE = {
    "type": "function",
    "function": {
        "name": "resolve_entity",
        "description": "Serbest metinden servis/namespace/workload/pod adayını çözer. Tek argüman: text.",
        "parameters": {
            "type": "object",
            "properties": {"text": {"type": "string", "description": "Operatörün yazdığı ad ya da parça"}},
            "required": ["text"],
        },
    },
}
TOOL_SEARCH = {
    "type": "function",
    "function": {
        "name": "search_traces",
        "description": "Trace araması. filters iç içe nesne dizisi, range iç içe nesne.",
        "parameters": {
            "type": "object",
            "properties": {
                "service": {"type": "string"},
                "filters": {
                    "type": "array",
                    "items": {
                        "type": "object",
                        "properties": {
                            "key": {"type": "string"},
                            "op": {"type": "string", "enum": ["=", "!=", "LIKE", "IN"]},
                            "value": {"type": "string"},
                        },
                        "required": ["key", "op", "value"],
                    },
                },
                "range": {
                    "type": "object",
                    "properties": {"from": {"type": "string"}, "to": {"type": "string"}},
                    "required": ["from", "to"],
                },
                "limit": {"type": "integer"},
            },
            "required": ["service"],
        },
    },
}
TOOLS = [TOOL_RESOLVE, TOOL_SEARCH]

SYSTEM = (
    "Sen Coremetry'nin telemetri asistanısın. Servis/attribute adı UYDURMA; "
    "gerekirse verilen tool'ları çağır. Tool çağırdığında argümanları JSON olarak ver."
)

# ── metin-gömülü tool çağrısı sezgileri (Gemma/Hermes/pythonic/JSON) ─────
TEXT_CALL_PATTERNS = [
    re.compile(r"<\|tool_call\|?>", re.I),
    re.compile(r"<start_function_call>|<end_function_call>", re.I),
    re.compile(r"<tool_call>", re.I),
    re.compile(r"```tool_code", re.I),
    re.compile(r"```json\s*\{\s*\"name\"\s*:", re.I),
    re.compile(r"\{\s*\"name\"\s*:\s*\"(resolve_entity|search_traces)\"", re.I),
    re.compile(r"\b(resolve_entity|search_traces)\s*\(", re.I),
]
THOUGHT_PATTERNS = [
    re.compile(r"<\|channel>thought", re.I),
    re.compile(r"<channel\|>", re.I),
    re.compile(r"<think>", re.I),
    re.compile(r"<\|thought\|>", re.I),
]


def chat(messages: list[dict], *, tools: list[dict] | None = None, max_tokens: int = MAX_TOKENS,
         extra: dict | None = None, label: str = "") -> dict[str, Any]:
    body: dict[str, Any] = {"model": MODEL, "messages": messages, "max_tokens": max_tokens, "temperature": 0}
    if tools:
        body["tools"] = tools
        body["tool_choice"] = "auto"
    if extra:
        body.update(extra)
    headers = {"Accept": "application/json"}
    if API_KEY:
        headers["Authorization"] = f"Bearer {API_KEY}"
    t0 = time.time()
    try:
        status, text = _post(f"{BASE}/chat/completions", headers, body, TIMEOUT, INSECURE)
    except Exception as e:  # transport
        return {"label": label, "transport_error": repr(e), "ms": int((time.time() - t0) * 1000), "request": body}
    ms = int((time.time() - t0) * 1000)
    try:
        js = json.loads(text)
    except Exception:
        js = {"_raw_text": text}
    return {"label": label, "status": status, "ms": ms, "request": body, "response": js}


def message_of(res: dict) -> dict:
    try:
        return res["response"]["choices"][0]["message"] or {}
    except Exception:
        return {}


def finish_reason(res: dict) -> str:
    try:
        return str(res["response"]["choices"][0].get("finish_reason"))
    except Exception:
        return "?"


def usage(res: dict) -> dict:
    try:
        return res["response"].get("usage") or {}
    except Exception:
        return {}


def classify_calls(res: dict) -> tuple[str, list[dict], str]:
    """(structured|text_embedded|none, calls, content)"""
    m = message_of(res)
    tc = m.get("tool_calls")
    content = m.get("content") or ""
    if isinstance(tc, list) and tc:
        calls = []
        for c in tc:
            fn = (c or {}).get("function") or {}
            args_raw = fn.get("arguments")
            parsed: Any = None
            if isinstance(args_raw, str):
                try:
                    parsed = json.loads(args_raw)
                except Exception:
                    parsed = {"_unparsed": args_raw}
            elif isinstance(args_raw, dict):
                parsed = args_raw
            calls.append({"name": fn.get("name"), "arguments": parsed})
        return "structured", calls, content
    for p in TEXT_CALL_PATTERNS:
        if p.search(content):
            return "text_embedded", [], content
    return "none", [], content


def has_thought(res: dict) -> dict:
    m = message_of(res)
    content = m.get("content") or ""
    fields = {k: (len(m[k]) if isinstance(m.get(k), str) else None) for k in ("reasoning_content", "reasoning", "thinking") if k in m}
    inline = any(p.search(content) for p in THOUGHT_PATTERNS)
    return {"inline_thought_block": inline, "reasoning_fields": fields, "content_chars": len(content)}


def dump(res: dict) -> None:
    print("--- request ---")
    print(json.dumps(res.get("request"), ensure_ascii=False, indent=1))
    print("--- response (raw) ---")
    if "transport_error" in res:
        print("TRANSPORT ERROR:", res["transport_error"])
    else:
        print("HTTP", res.get("status"), f"{res.get('ms')} ms")
        print(json.dumps(res.get("response"), ensure_ascii=False, indent=1))
    print()


# ── testler ──────────────────────────────────────────────────────────────
def t_a(rows: list, lang: str = "en") -> dict:
    q = "Resolve the service named 'checkout'." if lang == "en" else "'ödeme-servisi' adlı servisi çöz."
    res = chat([{"role": "system", "content": SYSTEM}, {"role": "user", "content": q}], tools=TOOLS, label=f"a-{lang}")
    dump(res)
    kind, calls, content = classify_calls(res)
    ok = kind == "structured" and calls and calls[0]["name"] == "resolve_entity"
    uni = ""
    if lang == "tr" and calls:
        uni = "ok" if calls[0]["arguments"].get("text", "").find("ödeme") >= 0 else "BOZUK"
    rows.append({"test": f"a ({lang})", "amaç": "tek argümanlı çağrı → tool_calls dolu mu",
                 "sonuç": kind, "detay": f"calls={json.dumps(calls, ensure_ascii=False)} finish={finish_reason(res)} unicode={uni or '-'}",
                 "geçti": bool(ok)})
    return res


def t_b(rows: list, lang: str = "en") -> dict:
    q = ("Search traces of service 'checkout' in the last 15 minutes where http.status_code = 500 and http.route LIKE '/pay/%', limit 20. "
         "Use range from '2026-09-08T09:00:00Z' to '2026-09-08T09:15:00Z'."
         if lang == "en" else
         "'ödeme-servisi' servisinde 2026-09-08T09:00:00Z ile 2026-09-08T09:15:00Z arasında http.status_code = 500 ve "
         "şube = 'İstanbul/Şişli' olan trace'leri ara, en çok 20.")
    res = chat([{"role": "system", "content": SYSTEM}, {"role": "user", "content": q}], tools=TOOLS, label=f"b-{lang}")
    dump(res)
    kind, calls, _ = classify_calls(res)
    nested_ok = False
    uni = "-"
    if calls:
        a = calls[0]["arguments"] or {}
        nested_ok = isinstance(a.get("filters"), list) and isinstance(a.get("range"), dict) and all(isinstance(f, dict) for f in a.get("filters", []))
        if lang == "tr":
            blob = json.dumps(a, ensure_ascii=False)
            uni = "ok" if ("İstanbul" in blob or "Şişli" in blob) else "BOZUK/eksik"
    rows.append({"test": f"b ({lang})", "amaç": "çok argümanlı + iç içe nesne (filters[], range{})",
                 "sonuç": kind, "detay": f"nested_ok={nested_ok} calls={json.dumps(calls, ensure_ascii=False)} unicode={uni}",
                 "geçti": kind == "structured" and nested_ok})
    return res


def t_c(rows: list) -> dict:
    q = "Resolve BOTH services 'checkout' and 'payments' — call resolve_entity once for each, in the same turn."
    res = chat([{"role": "system", "content": SYSTEM}, {"role": "user", "content": q}], tools=TOOLS, label="c")
    dump(res)
    kind, calls, content = classify_calls(res)
    n = len(calls) if kind == "structured" else len(re.findall(r"resolve_entity", content))
    rows.append({"test": "c", "amaç": "aynı turda iki tool çağrısı (paralel)",
                 "sonuç": f"{kind} n={n}", "detay": json.dumps(calls, ensure_ascii=False),
                 "geçti": kind == "structured" and n >= 2})
    return res


def t_d(rows: list, first: dict | None) -> dict:
    # a'daki çağrıyı kullan; yoksa sentetik bir tool_calls mesajı kur.
    calls = []
    if first:
        kind, calls, _ = classify_calls(first)
    if calls:
        m = message_of(first)
        assistant = {"role": "assistant", "content": m.get("content") or None, "tool_calls": m.get("tool_calls")}
        call_id = (m.get("tool_calls") or [{}])[0].get("id") or "call_0"
    else:
        assistant = {"role": "assistant", "content": None, "tool_calls": [
            {"id": "call_0", "type": "function", "function": {"name": "resolve_entity", "arguments": json.dumps({"text": "checkout"})}}]}
        call_id = "call_0"
    tool_result = {"role": "tool", "tool_call_id": call_id, "name": "resolve_entity",
                   "content": json.dumps({"matches": [{"kind": "service", "name": "checkout", "cluster": "prod-eu", "namespace": "shop", "score": 0.98}]})}
    msgs = [{"role": "system", "content": SYSTEM},
            {"role": "user", "content": "Resolve the service named 'checkout' and tell me which cluster/namespace it runs in."},
            assistant, tool_result]
    res = chat(msgs, tools=TOOLS, label="d")
    dump(res)
    kind, calls2, content = classify_calls(res)
    ok = res.get("status") == 200 and kind == "none" and ("prod-eu" in content or "shop" in content)
    rows.append({"test": "d", "amaç": "tool sonucu geri besleme → ikinci tur (tool rolü)",
                 "sonuç": f"HTTP {res.get('status')} kind={kind} finish={finish_reason(res)}",
                 "detay": f"content[:200]={content[:200]!r}", "geçti": bool(ok)})
    return res


def t_e(rows: list) -> None:
    q = "Bir servisin p99 gecikmesi 300 ms'den 900 ms'ye çıktı ama hata oranı değişmedi. Üç olası neden say ve hangisini önce kontrol edeceğini gerekçelendir."
    base = [{"role": "system", "content": SYSTEM}, {"role": "user", "content": q}]
    r1 = chat(base, max_tokens=MAX_TOKENS, label="e-full")
    dump(r1)
    th1 = has_thought(r1)
    u1 = usage(r1)
    rows.append({"test": "e1", "amaç": "düşünme bloğu geliyor mu (tam bütçe)",
                 "sonuç": f"inline={th1['inline_thought_block']} fields={th1['reasoning_fields']}",
                 "detay": f"completion_tokens={u1.get('completion_tokens')} content_chars={th1['content_chars']} finish={finish_reason(r1)}",
                 "geçti": True})
    r2 = chat(base, max_tokens=LOW_MAX_TOKENS, label="e-low")
    dump(r2)
    th2 = has_thought(r2)
    empty = th2["content_chars"] == 0
    rows.append({"test": "e2", "amaç": f"düşük max_tokens={LOW_MAX_TOKENS}: boş içerik mi (Qwen3 sınıfı)",
                 "sonuç": f"content_empty={empty} finish={finish_reason(r2)} inline={th2['inline_thought_block']}",
                 "detay": f"completion_tokens={usage(r2).get('completion_tokens')} fields={th2['reasoning_fields']}",
                 "geçti": not empty})
    r3 = chat(base, max_tokens=LOW_MAX_TOKENS, extra={"chat_template_kwargs": {"enable_thinking": False}}, label="e-nothink")
    dump(r3)
    th3 = has_thought(r3)
    rows.append({"test": "e3", "amaç": "chat_template_kwargs.enable_thinking=false kabul ediliyor mu / düşünce kalkıyor mu",
                 "sonuç": f"HTTP {r3.get('status')} inline={th3['inline_thought_block']} content_chars={th3['content_chars']}",
                 "detay": f"finish={finish_reason(r3)} fields={th3['reasoning_fields']}",
                 "geçti": r3.get("status") == 200 and th3["content_chars"] > 0})
    r4 = chat([{"role": "system", "content": SYSTEM}, {"role": "user", "content": "Resolve the service named 'checkout'."}],
              tools=TOOLS, max_tokens=LOW_MAX_TOKENS, label="e-tool-low")
    dump(r4)
    kind4, calls4, _ = classify_calls(r4)
    rows.append({"test": "e4", "amaç": "düşük bütçede tool çağrısı kesiliyor mu",
                 "sonuç": f"kind={kind4} finish={finish_reason(r4)}",
                 "detay": json.dumps(calls4, ensure_ascii=False), "geçti": kind4 == "structured"})


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--only", default="a,b,c,d,e,f")
    ap.add_argument("--json-out", default="")
    args = ap.parse_args()
    if not BASE:
        print("ORTAM EKSİK: GEMMA_BASE_URL (ya da COREMETRY_EVAL_BASE_URL) verilmedi. Ör: GEMMA_BASE_URL=http://vllm:8000/v1", file=sys.stderr)
        return 2
    only = {s.strip() for s in args.only.split(",") if s.strip()}
    print(f"endpoint={BASE} model={MODEL} timeout={TIMEOUT}s max_tokens={MAX_TOKENS} low={LOW_MAX_TOKENS}\n")
    rows: list[dict] = []
    all_res: list[dict] = []
    first_a = None
    if "a" in only:
        first_a = t_a(rows, "en"); all_res.append(first_a)
    if "b" in only:
        all_res.append(t_b(rows, "en"))
    if "c" in only:
        all_res.append(t_c(rows))
    if "d" in only:
        all_res.append(t_d(rows, first_a))
    if "e" in only:
        t_e(rows)
    if "f" in only:
        all_res.append(t_a(rows, "tr"))
        all_res.append(t_b(rows, "tr"))

    print("\n## Özet\n")
    print("| test | amaç | sonuç | geçti | detay |")
    print("|---|---|---|---|---|")
    for r in rows:
        det = str(r["detay"]).replace("|", "\\|")
        if len(det) > 240:
            det = det[:240] + "…"
        son = str(r["sonuç"]).replace("|", "\\|")
        mark = "✅" if r["geçti"] else "❌"
        print(f"| {r['test']} | {r['amaç']} | {son} | {mark} | {det} |")
    structured = [r for r in rows if r["test"].startswith(("a", "b", "c")) and "structured" in str(r["sonuç"])]
    print("\nKARAR:", "tool_calls YAPILANDIRILMIŞ geliyor" if structured else
          "tool_calls yapılandırılmış GELMİYOR — metin-gömülü/none; fallback parser gerekir (Faz 2 kapsamı)")
    if args.json_out:
        with open(args.json_out, "w", encoding="utf-8") as f:
            json.dump({"rows": rows, "responses": all_res}, f, ensure_ascii=False, indent=1)
        print(f"ham yanıtlar: {args.json_out}")
    return 1 if any("transport_error" in r or r.get("status", 200) >= 400 for r in all_res) else 0


if __name__ == "__main__":
    sys.exit(main())
