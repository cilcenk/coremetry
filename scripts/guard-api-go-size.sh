#!/usr/bin/env bash
# guard-api-go-size.sh — v0.10.625. internal/api/api.go BÜYÜMEZ (CLAUDE.md,
# /api-route): yeni route/handler kendi <domain>.go dosyasına gider
# (route_registry.go init() defteri), api.go yalnız küçülür. Bu script tek
# sayı tutar: .claude/baselines/api_go_lines (ratchet, yalnız aşağı iner).
#
# Kullanım: guard-api-go-size.sh [--worktree|--staged]
#   --worktree (varsayılan) çalışma ağacındaki dosyayı sayar (hook, CI)
#   --staged   index'teki hâlini sayar (git pre-commit)
# Çıkış: 0 tamam · 2 api.go tabanı AŞTI · 2 (yalnız GUARD_STRICT_RATCHET=1)
#   api.go küçüldü ama taban indirilmedi — CI'da bayat taban PR'ı düşürür;
#   hook'ta yalnız uyarı (düzenleme ortasında bloklamaz), go test
#   (api_go_size_test.go) commit anında aynı sözleşmeyi zorlar.
# Kaçış kapısı: taban YÜKSELTMEK = .claude/baselines/ için CODEOWNERS onayı
# + commit gövdesinde "Api-Go-Baseline: <gerekçe>". Lokalde API_GO_GUARD=skip
# yalnız acil durum; CI okumaz.
set -u
cd "${CLAUDE_PROJECT_DIR:-$(git rev-parse --show-toplevel)}" || exit 0
[[ "${API_GO_GUARD:-}" == "skip" ]] && { echo "guard-api-go-size: API_GO_GUARD=skip (yalnız lokal)" >&2; exit 0; }

f=internal/api/api.go
b=.claude/baselines/api_go_lines
[[ -f "$b" ]] || { echo "guard-api-go-size: taban dosyası yok: $b" >&2; exit 2; }
base=$(tr -d '[:space:]' < "$b")
case "${1:---worktree}" in
  --staged) now=$(git show ":$f" | wc -l | tr -d ' ') ;;
  *)        now=$(wc -l < "$f" | tr -d ' ') ;;
esac

if (( now > base )); then
  echo "api.go $now satır > taban $base. Yeni route/handler kendi internal/api/<domain>.go dosyasına (/api-route, registerRoutesExtra). Tabanı yükseltmek: CODEOWNERS onayı + 'Api-Go-Baseline: <gerekçe>' trailer'ı." >&2
  exit 2
fi
if (( now < base )); then
  echo "api.go küçüldü ($base → $now). Ratchet: 'echo $now > $b' ve aynı commit'e ekle." >&2
  [[ "${GUARD_STRICT_RATCHET:-0}" == "1" ]] && exit 2
fi
exit 0
