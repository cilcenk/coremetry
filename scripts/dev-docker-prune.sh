#!/usr/bin/env bash
# dev-docker-prune.sh — geliştirici makinesinde Docker disk hijyeni.
#
# v0.9.1275 (operatör isteği: "bir job yaz periyodik olarak imageları
# prune etsin" + "disk dolmasın sürekli"). 2026-08-23 ENOSPC olayının
# otomasyonu: bir günde ~19 `make image` sonrası build cache 139GB'a,
# Docker.raw 299G'a şişti, daemon disk yüzünden kalkamaz oldu.
#
# launchd her gün çağırır (com.coremetry.docker-prune); elle koşmak da
# güvenlidir: scripts/dev-docker-prune.sh
#
# NE YAPMAZ: `docker volume prune` YOK ve eklenmeyecek — minikube
# (docker sürücüsü) kümeyi bir volume'da tutar; volume prune lokal
# ClickHouse verisiyle birlikte kümeyi siler.
set -uo pipefail
PATH="/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:$PATH"

log() { echo "[docker-prune $(date '+%F %T')] $*"; }

# Daemon kapalıysa sessizce çık. `docker info` takılı daemon'da ASILIR
# (2026-08-23: Bash'i 2 dk kilitledi) — sokete zaman aşımıyla vurulur.
SOCK="$HOME/.docker/run/docker.sock"
if ! curl -s --max-time 5 --unix-socket "$SOCK" http://localhost/_ping 2>/dev/null | grep -q '^OK$'; then
  log "daemon kapalı ya da açılıyor — atlandı"
  exit 0
fi

# Aktif bir imaj build'i varken builder prune o build'in katmanlarıyla
# yarışır — atla, yarın yine gelinecek.
if pgrep -f "docker buildx build|docker build|make image" >/dev/null 2>&1; then
  log "aktif build var — atlandı"
  exit 0
fi

log "önce: $(df -h / | awk 'NR==2{print $4 " boş"}')"

# 1) Build cache: 72 saatten eski katmanlar gider. Son release'lerin
#    katmanları kalır ki `make image` hızlı kalsın; asıl şişkinlik
#    (139GB'lık sınıf) hep eski katmanlardır.
docker builder prune -af --filter until=72h 2>&1 | tail -1

# 2) coremetry* imajları: repo başına en yeni 5 sürüm tag'i + latest
#    kalır, gerisi silinir. Sürüm sırası tag'e göre (-V, vX.Y.Z doğal
#    sıralanır). Kullanımdaki imajı `docker rmi` zaten reddeder.
docker images --format '{{.Repository}}:{{.Tag}}' --filter 'reference=coremetry*' 2>/dev/null \
  | grep -v ':latest$' | grep -v '<none>' \
  | sort -t: -k2 -V -r \
  | awk -F: '{ if (++keep[$1] > 5) print }' \
  | while read -r img; do
      docker rmi "$img" >/dev/null 2>&1 && log "silindi: $img"
    done

# 3) Etiketsiz (dangling) imajlar.
docker image prune -f 2>&1 | tail -1

# 4) minikube İÇ daemon'ı: `minikube image load` kopyaları orada da
#    birikir (feedback-docker-disk-hygiene). Küme koşmuyorsa atla.
#    Koşan pod'ların imajları referanslı olduğundan silinmez;
#    pullPolicy:Never güvende.
if command -v minikube >/dev/null 2>&1 && minikube status >/dev/null 2>&1; then
  minikube ssh -- docker image prune -af --filter until=168h 2>&1 | tail -1
fi

log "sonra: $(df -h / | awk 'NR==2{print $4 " boş"}')"
