#!/usr/bin/env bash
# PostToolUse hook — Claude bir dosyayı düzenledikten sonra hızlı doğrulama.
#
# Mekanizma (Claude Code 2.x): hook payload'ı stdin'e JSON olarak gelir
#   { "tool_name": "Edit", "tool_input": { "file_path": "/abs/yol" } }
# env değişkeni OLARAK GELMEZ — bu yüzden eski $CLAUDE_TOOL_INPUT_FILE_PATH
# hiç dolmuyordu ve hook sessizce hiçbir şey yapmıyordu. Yolu stdin'den
# çıkarıyoruz. Hata olursa çıktıyı STDERR'e basıp EXIT 2 ile dönüyoruz:
# PostToolUse'da exit 2 stderr'i Claude'a geri besler, Claude da düzeltir
# (edit'i geri alamaz ama düzeltmeyi tetikler). Exit 0 = sessiz geç.
set -uo pipefail

# jq yoksa sessiz no-op olur — bunu görünür kıl ama edit'i bloklama.
if ! command -v jq >/dev/null 2>&1; then
  echo "post-edit-check: jq bulunamadı, hook atlandı" >&2
  exit 0
fi

payload=$(cat)
file=$(printf '%s' "$payload" | jq -r '.tool_input.file_path // empty')
[ -z "$file" ] && exit 0

cd "$CLAUDE_PROJECT_DIR" || exit 0

if [[ "$file" == *.go ]]; then
  if ! out=$(go build ./... 2>&1); then
    { echo "go build ./... başarısız:"; echo "$out" | head -40; } >&2
    exit 2
  fi
elif [[ "$file" =~ /frontend/.*\.tsx?$ ]]; then
  if ! out=$(cd frontend && npx tsc --noEmit 2>&1); then
    { echo "tsc --noEmit başarısız:"; echo "$out" | head -40; } >&2
    exit 2
  fi
fi
exit 0