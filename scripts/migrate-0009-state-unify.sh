#!/usr/bin/env bash
#
# migrate-0009-state-unify.sh — migrations/0009_state_unify.sql'in
# kendini doğrulayan koşucusu.  v0.9.1310.
#
# NEDEN: göç 37 tablo × 3 adım = 111 elle ifade. Elle koşulduğunda
# sırayı kaçırmak, ADIM 2'nin INSERT'ünü iki replikada koşturmak (T1 —
# MergeTree tablolarında satırları İKİYE KATLAR) ya da bir tabloyu
# doğrulamadan geçmek KOLAY. Bu script yordamı DEĞİŞTİRMEZ; göç
# dosyasındaki ÜRETİCİ sorguyu dosyadan OKUR ve her tablodan sonra
# dosyanın ADIM 4 doğrulamasını uygular.
#
# OTORİTE: migrations/0009_state_unify.sql. Şema, tablo listesi ve sıra
# burada TEKRAR YAZILMAZ — ıraksamasın diye üretici sorgu dosyadan
# çekilir. Dosya değişirse script onunla birlikte değişir.
#
# VARSAYILAN KİP --dry-run: hiçbir şey yazılmaz, ne koşulacağı satır
# satır basılır. Yazmak için açık `--apply` gerekir. ADIM 5 (DROP) BU
# SCRIPT'İN İŞİ DEĞİLDİR — `_old` tabloları asla silinmez.
#
set -euo pipefail

SCRIPT_NAME="$(basename "$0")"
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# ---------------------------------------------------------------- ayar
CLUSTER="uptrace_all"
DATABASE=""
CH_HOST="127.0.0.1"          # koordinatör: ON CLUSTER ifadeleri buradan
CH_PORT="9000"
CH_USER="default"
CLIENT_BIN="clickhouse-client"
MIGRATION="${REPO_ROOT}/migrations/0009_state_unify.sql"
OUT_DIR="/tmp"
APP_URL=""
MIN_APP_VERSION="v0.9.1308"
MODE="dry-run"
RESUME=0
ASSUME_YES=0
VERIFY_TIMEOUT=180
ONLY_LIST=""
PASSWORD_PROMPT=0

INSERT_HOSTS=()
CH_ARGS=()

usage() {
  cat <<'USAGE'
migrate-0009-state-unify.sh — 0009 state birleştirme göçünü tek komutta
koşar, her tablodan sonra kendini doğrular.

KULLANIM
  scripts/migrate-0009-state-unify.sh --database <db> [seçenekler]

KİP (varsayılan --dry-run)
  --dry-run                Hiçbir şey yazma; koşulacak her ifadeyi bas.
  --apply                  GERÇEKTEN koş.
  --resume                 Yarım kalmış koşuya devam et: biten tablolar
                           atlanır, '_unified'i duran tablolarda ADIM 1
                           tekrar kurulmaz.
  --yes                    --apply onayını sorma (otomasyon).

BAĞLANTI
  --cluster <ad>           Küme adı (varsayılan: uptrace_all).
  --database <db>          ZORUNLU. Coremetry veritabanı.
  --host <host>            Koordinatör host — ON CLUSTER ifadeleri ve
                           küme geneli sorgular buradan koşar.
  --port <port>            Varsayılan 9000.
  --user <user>            Varsayılan default.
  --password-prompt        Parolayı terminalden sor (ekrana yazmaz).
  --client-bin <yol>       clickhouse-client yolu.
  --client-arg <arg>       clickhouse-client'a ek argüman (tekrarlanır).

  Parola KOMUT SATIRINDA İSTENMEZ. Sırayla bakılır:
    CH_PASSWORD → CLICKHOUSE_PASSWORD → --password-prompt
  Bulunan değer ortam değişkeni olarak geçirilir, argv'ye yazılmaz.

HEDEFLEME
  --insert-host <host>     ADIM 2'nin INSERT'ünün koşacağı host. HER
                           SHARD İÇİN BİR TANE, tekrarlanır. Verilmezse
                           system.clusters'tan replica_num=1 türetilir
                           ve her birine bağlanabilirlik sınanır.
  --only <t1,t2,...>       Yalnız bu tabloları işle (kısmi koşu /
                           yeniden deneme). Varsayılan: üreticinin
                           döndürdüğü tablo listesinin tamamı.

DİĞER
  --app-url <url>          Verilirse <url>/api/version yoklanır ve
                           imajın v0.9.1308+ olduğu DOĞRULANIR.
  --migration <yol>        Göç dosyası (varsayılan migrations/0009_...).
  --out-dir <dizin>        Fotoğraf dosyaları (varsayılan /tmp).
  --verify-timeout <sn>    Tablo başına replika yakınsama beklemesi.
  -h, --help               Bu metin.

ÖRNEK — prod (uptrace_all, 2 shard × 2 replica)
  CH_PASSWORD=... scripts/migrate-0009-state-unify.sh \
      --cluster uptrace_all --database coremetry \
      --host 172.31.240.15 --user default \
      --insert-host 172.31.240.15 --insert-host 172.31.240.30 \
      --app-url http://coremetry.internal
  # çıktıyı oku, sonra AYNI komuta --apply ekle.

ADIM 5 (yedekleri DROP) BU SCRIPT'TE YOKTUR — birkaç gün bekleyip göç
dosyasındaki DROP satırlarını elle koştur. Ondan öncesi geri alınabilir.
USAGE
}

# --------------------------------------------------------------- çıktı
c_red=""; c_grn=""; c_yel=""; c_dim=""; c_bld=""; c_off=""
if [ -t 1 ]; then
  c_red=$'\033[31m'; c_grn=$'\033[32m'; c_yel=$'\033[33m'
  c_dim=$'\033[2m';  c_bld=$'\033[1m';  c_off=$'\033[0m'
fi

say()  { printf '%s\n' "$*"; }
ok()   { printf '  %s✓%s %s\n' "$c_grn" "$c_off" "$*"; }
warn() { printf '  %s!%s %s\n' "$c_yel" "$c_off" "$*"; }
dim()  { printf '  %s%s%s\n' "$c_dim" "$*" "$c_off"; }
hdr()  { printf '\n%s%s%s\n' "$c_bld" "$*" "$c_off"; }

abort() {
  printf '\n%sABORT%s  %s\n' "$c_red" "$c_off" "$1" >&2
  shift || true
  while [ "$#" -gt 0 ]; do printf '       %s\n' "$1" >&2; shift; done
  printf '\n' >&2
  exit 2
}

# ----------------------------------------------------------- argümanlar
while [ "$#" -gt 0 ]; do
  case "$1" in
    --cluster)         CLUSTER="${2:?--cluster değeri eksik}"; shift 2 ;;
    --database)        DATABASE="${2:?--database değeri eksik}"; shift 2 ;;
    --host)            CH_HOST="${2:?--host değeri eksik}"; shift 2 ;;
    --port)            CH_PORT="${2:?--port değeri eksik}"; shift 2 ;;
    --user)            CH_USER="${2:?--user değeri eksik}"; shift 2 ;;
    --password-prompt) PASSWORD_PROMPT=1; shift ;;
    --client-bin)      CLIENT_BIN="${2:?--client-bin değeri eksik}"; shift 2 ;;
    --client-arg)      CH_ARGS[${#CH_ARGS[@]}]="${2:?--client-arg değeri eksik}"; shift 2 ;;
    --insert-host)     INSERT_HOSTS[${#INSERT_HOSTS[@]}]="${2:?--insert-host değeri eksik}"; shift 2 ;;
    --only)            ONLY_LIST="${2:?--only değeri eksik}"; shift 2 ;;
    --app-url)         APP_URL="${2:?--app-url değeri eksik}"; shift 2 ;;
    --migration)       MIGRATION="${2:?--migration değeri eksik}"; shift 2 ;;
    --out-dir)         OUT_DIR="${2:?--out-dir değeri eksik}"; shift 2 ;;
    --verify-timeout)  VERIFY_TIMEOUT="${2:?--verify-timeout değeri eksik}"; shift 2 ;;
    --dry-run)         MODE="dry-run"; shift ;;
    --apply)           MODE="apply"; shift ;;
    --resume)          RESUME=1; shift ;;
    --yes)             ASSUME_YES=1; shift ;;
    -h|--help)         usage; exit 0 ;;
    --password)
      abort "--password bilinçli olarak DESTEKLENMİYOR." \
            "Parola argv'de görünür (ps, shell geçmişi, /proc)." \
            "Bunun yerine:  CH_PASSWORD=... $SCRIPT_NAME ...   ya da --password-prompt"
      ;;
    *) abort "Bilinmeyen argüman: $1" "Kullanım için: $SCRIPT_NAME --help" ;;
  esac
done

[ -n "$DATABASE" ] || abort "--database zorunlu." \
  "Coremetry veritabanının adını ver (ör. --database coremetry)."

CH_PASS="${CH_PASSWORD:-${CLICKHOUSE_PASSWORD:-}}"
if [ "$PASSWORD_PROMPT" -eq 1 ]; then
  printf 'ClickHouse parolası (%s@%s): ' "$CH_USER" "$CH_HOST" >&2
  IFS= read -r -s CH_PASS
  printf '\n' >&2
fi
if [ -n "$CH_PASS" ]; then export CLICKHOUSE_PASSWORD="$CH_PASS"; fi
CH_PASS=""
unset CH_PASSWORD 2>/dev/null || true

TS="$(date +%Y%m%d-%H%M%S)"
BEFORE_FILE="${OUT_DIR}/0009-before-${TS}.tsv"
AFTER_FILE="${OUT_DIR}/0009-after-${TS}.tsv"
DDL_FILE="${OUT_DIR}/0009-ddl-${TS}.sql"

TMPDIR_RUN="$(mktemp -d "${TMPDIR:-/tmp}/mig0009.XXXXXX")"
cleanup() { rm -rf "$TMPDIR_RUN"; }
trap cleanup EXIT

# ------------------------------------------------------ ClickHouse kolu
# ch_run <host> <sql> <ne-yapıyordu> <çıktı-dosyası>
#
# Sonuç DOSYAYA yazılır, komut ikamesine DEĞİL: `abort` bir alt kabukta
# koşarsa `exit` yalnız o alt kabuğu öldürür ve script sessizce devam
# ederdi. Dosya kolu bu sınıfı tümden kapatır.
ch_run() {
  local host="$1" sql="$2" what="$3" out="$4" err rc
  err="${TMPDIR_RUN}/stderr.txt"
  set +e
  "$CLIENT_BIN" --host "$host" --port "$CH_PORT" --user "$CH_USER" \
    --database "$DATABASE" ${CH_ARGS[@]+"${CH_ARGS[@]}"} --query "$sql" \
    >"$out" 2>"$err"
  rc=$?
  set -e
  if [ "$rc" -ne 0 ]; then
    abort "ClickHouse hatası — $what (host $host, rc=$rc)" "$(head -c 2000 "$err")"
  fi
}

# ------------------------------------------------------------ yardımcı
# Tablo adları CH'den geliyor ama SQL'e enterpolasyon ediliyor; biçimi
# yine de doğrula.
safe_ident() {
  case "$1" in
    *[!A-Za-z0-9_]*|"") return 1 ;;
    *) return 0 ;;
  esac
}

in_list() { # in_list <iğne> <boşlukla ayrılmış liste>
  local needle="$1" hay=" $2 "
  case "$hay" in *" $needle "*) return 0 ;; *) return 1 ;; esac
}

# v0.9.1308 → 9001308 (ondalık; başta sıfır YOK — `test -ge` başta
# sıfırlı bir sayıyı sekizlik sanıp patlıyordu).
VNUM=""
vnum() {
  local v="${1#v}" a b c
  a="${v%%.*}"; v="${v#*.}"
  b="${v%%.*}"; v="${v#*.}"
  c="${v%%[!0-9]*}"
  case "${a}${b}${c}" in *[!0-9]*|"") return 1 ;; esac
  VNUM=$(( 10#$a * 1000000000 + 10#$b * 1000000 + 10#$c ))
  return 0
}

say ""
say "${c_bld}=====================================================${c_off}"
say "${c_bld} Coremetry 0009 — state birleştirme koşucusu${c_off}"
say "${c_bld}=====================================================${c_off}"
say "  Küme        : $CLUSTER"
say "  Veritabanı  : $DATABASE"
say "  Koordinatör : ${CH_HOST}:${CH_PORT} (kullanıcı: $CH_USER)"
say "  Göç dosyası : $MIGRATION"
if [ "$MODE" = "apply" ]; then
  say "  Kip         : ${c_red}${c_bld}APPLY — GERÇEKTEN YAZAR${c_off}"
else
  say "  Kip         : ${c_grn}DRY-RUN — hiçbir şey yazılmaz${c_off}"
fi
[ "$RESUME" -eq 1 ] && say "  Devam       : --resume (biten tablolar atlanır)"
[ -n "$ONLY_LIST" ] && say "  Kapsam      : --only $ONLY_LIST"

# =====================================================================
# ADIM 0 — ÖN KONTROL (hepsi assertion; biri tutmazsa DUR)
# =====================================================================
hdr "ADIM 0 — ÖN KONTROL"

# 0.1 göç dosyası + üretici sorgunun çıkarılması ---------------------
[ -r "$MIGRATION" ] || abort "Göç dosyası okunamıyor: $MIGRATION" \
  "--migration ile doğru yolu ver."

GEN_SQL="${TMPDIR_RUN}/generator.sql"
awk '/^SELECT replaceOne\(/,/^FORMAT TSVRaw;/' "$MIGRATION" \
  | sed "s/uptrace_all/${CLUSTER}/g" > "$GEN_SQL"

if ! grep -q 'replaceOne' "$GEN_SQL" || ! grep -q 'FORMAT TSVRaw' "$GEN_SQL"; then
  abort "Göç dosyasından ADIM 1 üretici sorgusu çıkarılamadı." \
        "Beklenen çapa: 'SELECT replaceOne(' ile başlayan satır ve onu" \
        "izleyen 'FORMAT TSVRaw;' satırı — $MIGRATION içinde." \
        "Dosya değiştiyse script'i onunla birlikte güncelle; şemayı ya da" \
        "tablo listesini script'e KOPYALAMA (ıraksar)."
fi
ok "Üretici sorgu göç dosyasından okundu ($(grep -c . "$GEN_SQL") satır)"

# 0.2 istemci + koordinatör bağlantısı ------------------------------
command -v "$CLIENT_BIN" >/dev/null 2>&1 \
  || abort "clickhouse-client bulunamadı: $CLIENT_BIN" "--client-bin ile yolunu ver."

PROBE_F="${TMPDIR_RUN}/probe.txt"
ch_run "$CH_HOST" "SELECT 1" "koordinatöre bağlanma" "$PROBE_F"
[ "$(cat "$PROBE_F")" = "1" ] || abort "Koordinatör beklenmedik cevap verdi."
ok "Koordinatör bağlantısı: ${CH_HOST}:${CH_PORT}"

# 0.3 küme var mı, kaç shard × kaç replica ---------------------------
CLUSTER_ROWS="${TMPDIR_RUN}/cluster.tsv"
ch_run "$CH_HOST" \
  "SELECT shard_num, replica_num, host_name, host_address FROM system.clusters WHERE cluster = '${CLUSTER}' ORDER BY shard_num, replica_num FORMAT TSV" \
  "küme topolojisini okuma" "$CLUSTER_ROWS"

[ -s "$CLUSTER_ROWS" ] || abort "Küme system.clusters'ta YOK: '$CLUSTER'" \
  "Mevcut kümeler:  SELECT DISTINCT cluster FROM system.clusters" \
  "--cluster ile doğru adı ver."

SHARD_NUMS=""
CLUSTER_HOSTS=0
while IFS="$(printf '\t')" read -r sn rn hn ha; do
  [ -n "$sn" ] || continue
  CLUSTER_HOSTS=$((CLUSTER_HOSTS + 1))
  in_list "$sn" "$SHARD_NUMS" || SHARD_NUMS="$SHARD_NUMS $sn"
  : "$rn" "$hn" "$ha"
done < "$CLUSTER_ROWS"
SHARD_COUNT=0
for _s in $SHARD_NUMS; do SHARD_COUNT=$((SHARD_COUNT + 1)); done
ok "Küme '$CLUSTER': ${SHARD_COUNT} shard, ${CLUSTER_HOSTS} host"

# 0.4 MAKRO BENZERSİZLİĞİ (göç dosyası 0a) ---------------------------
# Birleşik yolda replika adı '{shard}-{replica}'. İki host aynı adı
# iddia ederse ikincisi REPLICA_ALREADY_EXISTS alır ve göç yarıda kalır.
MACRO_ROWS="${TMPDIR_RUN}/macros.tsv"
ch_run "$CH_HOST" \
  "SELECT hostName() AS h, anyIf(substitution, macro = 'shard') AS s, anyIf(substitution, macro = 'replica') AS r FROM clusterAllReplicas('${CLUSTER}', system.macros) GROUP BY h ORDER BY h FORMAT TSV" \
  "makro okuma" "$MACRO_ROWS"

MACRO_SEEN=""; MACRO_DUP=""; MACRO_MISSING=""; MACRO_N=0
while IFS="$(printf '\t')" read -r mh ms mr; do
  [ -n "$mh" ] || continue
  MACRO_N=$((MACRO_N + 1))
  if [ -z "$ms" ] || [ -z "$mr" ]; then
    MACRO_MISSING="$MACRO_MISSING $mh"
    continue
  fi
  uniq_id="${ms}-${mr}"
  if in_list "$uniq_id" "$MACRO_SEEN"; then
    MACRO_DUP="$MACRO_DUP ${mh}=${uniq_id}"
  else
    MACRO_SEEN="$MACRO_SEEN $uniq_id"
  fi
  dim "$mh   shard='${ms}'  replica='${mr}'   →   '{shard}-{replica}' = '${uniq_id}'"
done < "$MACRO_ROWS"

[ "$MACRO_N" -eq "$CLUSTER_HOSTS" ] || abort \
  "Makro sorgusu $MACRO_N host döndü ama küme tanımında $CLUSTER_HOSTS host var." \
  "Bir host erişilemiyor olabilir — göçe başlamadan önce kümeyi topla."
[ -z "$MACRO_MISSING" ] || abort \
  "Şu host'larda 'shard' ya da 'replica' makrosu TANIMSIZ:$MACRO_MISSING" \
  "Replicated tablonun ZK yolu ve replika adı bu makrolardan kuruluyor;" \
  "eksikse birleşik tablo kurulamaz."
[ -z "$MACRO_DUP" ] || abort \
  "'{shard}-{replica}' ÇAKIŞIYOR:$MACRO_DUP" \
  "Birleşik yolda dört host TEK replikasyon grubunda olacak; iki host aynı" \
  "replika adını iddia ederse ikincisi REPLICA_ALREADY_EXISTS alır ve göç" \
  "ilk tabloda yarıda kalır." \
  "Makroları düzelt (her node benzersiz bir 'replica' değeri almalı) ve" \
  "tekrar koş. HİÇBİR ŞEY DEĞİŞTİRİLMEDİ."
ok "Makrolar benzersiz: $MACRO_N host → $MACRO_N farklı '{shard}-{replica}'"

# 0.5 INSERT host'ları (göç dosyası 0c) ------------------------------
if [ "${#INSERT_HOSTS[@]}" -eq 0 ]; then
  warn "--insert-host verilmedi; system.clusters'tan replica_num=1 türetiliyor."
  while IFS="$(printf '\t')" read -r sn rn hn ha; do
    [ -n "$sn" ] || continue
    [ "$rn" = "1" ] || continue
    INSERT_HOSTS[${#INSERT_HOSTS[@]}]="$hn"
    : "$ha"
  done < "$CLUSTER_ROWS"
fi

INS_N="${#INSERT_HOSTS[@]}"
[ "$INS_N" -eq "$SHARD_COUNT" ] || abort \
  "--insert-host sayısı ($INS_N) shard sayısıyla ($SHARD_COUNT) EŞLEŞMİYOR." \
  "ADIM 2'nin INSERT'ü her shard'ın TAM BİR node'unda koşmalı (T1):" \
  "  eksik host  → o shard'ın satırları hiç kopyalanmaz, göçte SESSİZCE kaybolur;" \
  "  fazla host  → append-only (MergeTree) tablolarda satırlar İKİYE KATLANIR" \
  "                ve FINAL onları toplamaz." \
  "Shard başına bir host ver:  --insert-host <h1> --insert-host <h2> ..."

COVERED=""
i=0
while [ "$i" -lt "$INS_N" ]; do
  h="${INSERT_HOSTS[$i]}"
  match_shard=""; match_replica=""; match_n=0
  while IFS="$(printf '\t')" read -r sn rn hn ha; do
    [ -n "$sn" ] || continue
    if [ "$h" = "$hn" ] || [ "$h" = "$ha" ]; then
      match_shard="$sn"; match_replica="$rn"; match_n=$((match_n + 1))
    fi
  done < "$CLUSTER_ROWS"

  [ "$match_n" -ne 0 ] || abort \
    "--insert-host '$h' '$CLUSTER' kümesinde YOK." \
    "Kümedeki host'lar (shard / replica / host_name / host_address):" \
    "$(awk -F'\t' '{printf "  %s  %s  %s  %s\n", $1, $2, $3, $4}' "$CLUSTER_ROWS")"
  [ "$match_n" -eq 1 ] || abort \
    "--insert-host '$h' küme tanımında $match_n kez geçiyor — hangi shard olduğu belirsiz."
  if in_list "$match_shard" "$COVERED"; then
    abort "İki --insert-host aynı shard'a ($match_shard) düşüyor; sonuncusu '$h'." \
          "Her shard TAM BİR kez temsil edilmeli — aynı shard'da iki INSERT," \
          "MergeTree tablolarında satırları ikiye katlar (T1)."
  fi
  COVERED="$COVERED $match_shard"
  [ "$match_replica" = "1" ] || warn \
    "'$h' shard ${match_shard}'ın replica_num=${match_replica} node'u (göç dosyası 0c" \
    "replica_num=1 gösteriyor; shard başına tekil olduğu sürece sorun değil)."

  ch_run "$h" "SELECT 1" "INSERT host'una bağlanma" "$PROBE_F"
  [ "$(cat "$PROBE_F")" = "1" ] || abort "INSERT host '$h' beklenmedik cevap verdi."
  ok "INSERT host   shard $match_shard   →   $h"
  i=$((i + 1))
done

for _s in $SHARD_NUMS; do
  in_list "$_s" "$COVERED" || abort \
    "Shard $_s için --insert-host verilmedi." \
    "O shard'ın satırları hiç kopyalanmaz ve göçten sonra SESSİZCE kaybolur."
done

# 0.6 uygulama sürümü — ÖNKOŞUL --------------------------------------
# v0.9.1308'den eski bir imaj, RENAME'den sonra eski yollu tabloları
# birleşiklerin YANINA kurmaya çalışır ve METADATA_MISMATCH alır.
if [ -n "$APP_URL" ]; then
  command -v curl >/dev/null 2>&1 || abort "--app-url verildi ama curl yok."
  set +e
  VER_JSON="$(curl -fsS --max-time 10 "${APP_URL%/}/api/version" 2>/dev/null)"
  vrc=$?
  set -e
  [ "$vrc" -eq 0 ] || abort "Uygulama sürümü okunamadı: ${APP_URL%/}/api/version" \
    "curl rc=$vrc — URL yanlış ya da uygulama ayakta değil."
  # buildVersion = imajın kimliği. `version` COREMETRY_VERSION ile
  # ezilebiliyor (v0.9.339); önkoşul imaja bakmalı, gösterilene değil.
  APP_VER="$(printf '%s' "$VER_JSON" | sed -n 's/.*"buildVersion"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')"
  [ -n "$APP_VER" ] || APP_VER="$(printf '%s' "$VER_JSON" | sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')"
  [ -n "$APP_VER" ] || abort "/api/version cevabı ayrıştırılamadı: $VER_JSON"

  if ! vnum "$APP_VER"; then
    abort "Uygulama sürümü sayısal değil: '$APP_VER'" \
      "Muhtemelen ldflag'siz bir 'dev' imajı — hangi kod olduğu belli değil." \
      "İmajın $MIN_APP_VERSION+ olduğunu elle doğrula; doğruysa --app-url'i" \
      "kaldırıp tekrar koş."
  fi
  APP_N="$VNUM"
  vnum "$MIN_APP_VERSION"; MIN_N="$VNUM"
  [ "$APP_N" -ge "$MIN_N" ] || abort \
    "Uygulama $APP_VER — bu göç $MIN_APP_VERSION+ İSTİYOR." \
    "Daha eski bir imaj, RENAME'den sonra eski yollu tabloları birleşiklerin" \
    "YANINA kurmaya çalışır ve METADATA_MISMATCH alır." \
    "Önce uygulamayı yükselt, sonra göçü koş."
  ok "Uygulama imajı $APP_VER (≥ $MIN_APP_VERSION)"
else
  warn "--app-url verilmedi: uygulamanın $MIN_APP_VERSION+ olduğu DOĞRULANMADI."
  warn "Daha eski bir imaj göçten sonra METADATA_MISMATCH alır. Emin değilsen"
  warn "durdur ve  curl -s <url>/api/version  ile buildVersion'a bak."
fi

# 0.7 ADIM 1 üreticisini koş, çıktısını DOĞRULA ----------------------
DDL_RAW="${TMPDIR_RUN}/ddl.txt"
ch_run "$CH_HOST" "$(cat "$GEN_SQL")" "ADIM 1 üreticisini koşma" "$DDL_RAW"

GEN_ROWS="$(grep -c . "$DDL_RAW" || true)"
[ "$GEN_ROWS" -gt 0 ] || abort \
  "ADIM 1 üreticisi SIFIR satır döndü." \
  "'$DATABASE' içinde Replicated state tablosu görünmüyor. --database yanlış" \
  "olabilir; ya da kurulum tek-node (Replicated değil) kipinde — o durumda" \
  "bu göç GEREKMİYOR."

BAD_SHARD="$(grep -c '/{shard}/' "$DDL_RAW" || true)"
[ "$BAD_SHARD" -eq 0 ] || abort \
  "Üretilen DDL'in $BAD_SHARD satırında hâlâ '/{shard}/' var." \
  "Üreticinin replaceOne'ı tutmamış — ör. ReplicaPath elle değiştirilmiş ve" \
  "yol '/{shard}/<ad>' biçiminde değil." \
  "DUR ve göç dosyasının ADIM 1'ini elle incele: bu hâliyle koşulursa" \
  "tablolar yine shard başına AYRIŞIK kalır ve bölünme sürer."

GOOD_STATE="$(grep -c '/state/' "$DDL_RAW" || true)"
[ "$GOOD_STATE" -eq "$GEN_ROWS" ] || abort \
  "Üretilen $GEN_ROWS DDL'in yalnız $GOOD_STATE tanesinde '/state/' var." \
  "Kalanlar birleşik yola taşınmıyor — göç eksik kalırdı."
ok "ADIM 1 üreticisi: $GEN_ROWS DDL · $GOOD_STATE '/state/' · 0 '/{shard}/'"

# Tablo adlarını ÜRETİLEN DDL'den ayrıştır. Sıra üreticinin sırasıdır
# (ORDER BY total_rows ASC — küçükten büyüğe, `problems` en sonda).
GEN_NAMES=""
TBL_NAME=(); TBL_DDL=()
while IFS= read -r ddl; do
  [ -n "$ddl" ] || continue
  n="${ddl#CREATE TABLE }"
  n="${n%% *}"
  n="${n#*.}"
  n="${n%_unified}"
  safe_ident "$n" || abort "DDL'den ayrıştırılan tablo adı geçersiz: '$n'"
  TBL_NAME[${#TBL_NAME[@]}]="$n"
  TBL_DDL[${#TBL_DDL[@]}]="$ddl"
  GEN_NAMES="$GEN_NAMES $n"
done < "$DDL_RAW"

N_ALL="${#TBL_NAME[@]}"
[ "$N_ALL" -eq "$GEN_ROWS" ] || abort \
  "DDL satır sayısı ($GEN_ROWS) ayrıştırılan tablo sayısıyla ($N_ALL) uyuşmuyor."

# 0.8 tablo başına motor, satır ve DURUM -----------------------------
IN_LIST=""
i=0
while [ "$i" -lt "$N_ALL" ]; do
  if [ -z "$IN_LIST" ]; then IN_LIST="'${TBL_NAME[$i]}'"
  else IN_LIST="${IN_LIST},'${TBL_NAME[$i]}'"; fi
  i=$((i + 1))
done

ENGINE_ROWS="${TMPDIR_RUN}/engines.tsv"
ch_run "$CH_HOST" \
  "SELECT name, engine, toString(coalesce(total_rows, 0)) FROM system.tables WHERE database = currentDatabase() AND name IN (${IN_LIST}) FORMAT TSV" \
  "motor/satır okuma" "$ENGINE_ROWS"

SUFFIX_ROWS="${TMPDIR_RUN}/suffix.tsv"
ch_run "$CH_HOST" \
  "SELECT name FROM system.tables WHERE database = currentDatabase() AND (name LIKE '%\\_old' OR name LIKE '%\\_unified') ORDER BY name FORMAT TSV" \
  "artık tablo okuma" "$SUFFIX_ROWS"
EXISTING_SUFFIXED=""
while IFS= read -r sname; do
  [ -n "$sname" ] || continue
  EXISTING_SUFFIXED="$EXISTING_SUFFIXED $sname"
done < "$SUFFIX_ROWS"

# "Birleşik mi?" — göç dosyasının ADIM 4a doğrulaması, tablo başına:
# TEK zookeeper_path VE yolda '/{shard}/' YOK.
REPL_ROWS="${TMPDIR_RUN}/replicas.tsv"
ch_run "$CH_HOST" \
  "SELECT table, toString(uniqExact(zookeeper_path)) AS paths, toString(count()) AS reps, any(zookeeper_path) AS p FROM clusterAllReplicas('${CLUSTER}', system.replicas) WHERE database = currentDatabase() AND table IN (${IN_LIST}) GROUP BY table FORMAT TSV" \
  "replikasyon grubu okuma" "$REPL_ROWS"

repl_field() { # <tablo> <1=paths|2=reps|3=path>
  awk -F'\t' -v t="$1" -v f="$2" '$1 == t { print $(f + 1) }' "$REPL_ROWS"
}
engine_of() { awk -F'\t' -v t="$1" '$1 == t { print $2 }' "$ENGINE_ROWS"; }
rows_of()   { awk -F'\t' -v t="$1" '$1 == t { print $3 }' "$ENGINE_ROWS"; }

# --only süzgeci
ONLY_SET=""
if [ -n "$ONLY_LIST" ]; then
  ONLY_SET="$(printf '%s' "$ONLY_LIST" | tr ',' ' ')"
  for t in $ONLY_SET; do
    in_list "$t" "$GEN_NAMES" || abort \
      "--only '$t' üreticinin listesinde YOK." \
      "Üretici $N_ALL tablo döndürdü; '$t' bir state tablosu değil ya da adı" \
      "yanlış yazılmış."
  done
fi

TODO_IDX=""
N_DONE=0; N_PARTIAL=0; N_TODO=0; N_OUT_OF_SCOPE=0
INCONSISTENT=""
i=0
while [ "$i" -lt "$N_ALL" ]; do
  t="${TBL_NAME[$i]}"
  if [ -n "$ONLY_SET" ] && ! in_list "$t" "$ONLY_SET"; then
    N_OUT_OF_SCOPE=$((N_OUT_OF_SCOPE + 1)); i=$((i + 1)); continue
  fi
  paths="$(repl_field "$t" 1)"
  path="$(repl_field "$t" 3)"
  unified=0
  if [ "$paths" = "1" ]; then
    case "$path" in *'/{shard}/'*) unified=0 ;; *) unified=1 ;; esac
  fi
  has_old=0; in_list "${t}_old" "$EXISTING_SUFFIXED" && has_old=1
  has_uni=0; in_list "${t}_unified" "$EXISTING_SUFFIXED" && has_uni=1

  if [ "$unified" -eq 1 ]; then
    N_DONE=$((N_DONE + 1))
  elif [ "$has_old" -eq 1 ]; then
    INCONSISTENT="$INCONSISTENT $t"
  elif [ "$has_uni" -eq 1 ]; then
    N_PARTIAL=$((N_PARTIAL + 1)); TODO_IDX="$TODO_IDX $i"
  else
    N_TODO=$((N_TODO + 1)); TODO_IDX="$TODO_IDX $i"
  fi
  i=$((i + 1))
done

[ -z "$INCONSISTENT" ] || abort \
  "TUTARSIZ DURUM — şu tablolar hâlâ shard'lı AMA '_old' kardeşi var:$INCONSISTENT" \
  "Bu, yarıda kesilmiş bir RENAME'in izi. Otomatik devam GÜVENLİ DEĞİL." \
  "İncele:" \
  "  SELECT name, engine FROM system.tables WHERE database='$DATABASE' AND name LIKE '%_old';" \
  "Göç dosyasının GERİ ALMA bölümü tablo başına tek ifadeyle eski hâle döner."

if [ "$N_PARTIAL" -gt 0 ] && [ "$RESUME" -eq 0 ]; then
  abort "$N_PARTIAL tabloda ADIM 1 zaten koşmuş ('_unified' duruyor), takas bitmemiş." \
    "Önceki koşu yarıda kalmış. Devam etmek için --resume ekle; yoksa ADIM 1" \
    "aynı ZK yolunu ikinci kez kurmaya çalışır ve REPLICA_ALREADY_EXISTS alır" \
    "(göç dosyası T4 — sessiz devam etmektense gürültülü durmak)."
fi
if [ "$N_DONE" -gt 0 ] && [ "$N_TODO" -gt 0 ] && [ "$RESUME" -eq 0 ]; then
  abort "$N_DONE tablo zaten birleşik, $N_TODO tablo değil — bu KISMİ bir göç." \
    "Bitenleri atlayıp devam etmek için --resume ekle."
fi

say ""
ok "Durum: ${N_DONE} birleşik · ${N_TODO} bekliyor · ${N_PARTIAL} yarım (ADIM 1 tamam)"
[ "$N_OUT_OF_SCOPE" -gt 0 ] && dim "--only dışında bırakılan: $N_OUT_OF_SCOPE tablo"

# 0.9 BÖLÜNME FOTOĞRAFI (göç dosyası 0b) -----------------------------
mkdir -p "$OUT_DIR" 2>/dev/null || abort "Çıktı dizini yazılamıyor: $OUT_DIR"
snapshot() { # snapshot <dosya>
  local q="" j=0 t
  while [ "$j" -lt "$N_ALL" ]; do
    t="${TBL_NAME[$j]}"
    [ -n "$q" ] && q="$q UNION ALL "
    q="${q} SELECT '${t}' AS tablo, hostName() AS host, count() AS satir FROM clusterAllReplicas('${CLUSTER}', currentDatabase(), ${t}) GROUP BY host"
    j=$((j + 1))
  done
  ch_run "$CH_HOST" \
    "SELECT tablo, host, satir FROM (${q}) ORDER BY tablo, host FORMAT TSVWithNames" \
    "bölünme fotoğrafı" "$1"
}
snapshot "$BEFORE_FILE"
SPLIT_TABLES="$(awk -F'\t' '
  NR > 1 { c[$1] = c[$1] "|" $3 }
  END {
    n = 0
    for (t in c) {
      k = split(substr(c[t], 2), a, "|")
      same = 1
      for (i = 2; i <= k; i++) if (a[i] != a[1]) same = 0
      if (!same) n++
    }
    print n
  }' "$BEFORE_FILE")"
ok "Fotoğraf yazıldı: $BEFORE_FILE"
if [ "$SPLIT_TABLES" -gt 0 ]; then
  warn "$SPLIT_TABLES tabloda host'lar FARKLI sayı veriyor — ölçülen bölünme bu."
else
  dim "Hiçbir tabloda host'lar arasında sayı farkı yok."
fi

# =====================================================================
# PLAN
# =====================================================================
N_WORK=0
for _i in $TODO_IDX; do N_WORK=$((N_WORK + 1)); done

if [ "$N_WORK" -eq 0 ]; then
  hdr "YAPILACAK İŞ YOK"
  say "  Kapsamdaki tabloların hepsi ZATEN birleşik yolda: tek replikasyon"
  say "  grubu ve ZK yolunda '/{shard}/' yok. Göç tamamlanmış."
  say ""
  say "  Fotoğraf: $BEFORE_FILE"
  say ""
  exit 0
fi

cp "$DDL_RAW" "$DDL_FILE"

hdr "PLAN — $N_WORK tablo (üreticinin sırası: küçükten büyüğe)"
say "  Tablo başına sırayla: ADIM 2 (kopyala) → ADIM 3 (takas) → ADIM 4 (doğrula)."
say "  ADIM 3b yakalama YALNIZ ReplacingMergeTree tablolarında koşar; MergeTree'de"
say "  satırları ikiye katlardı (göç dosyası T1/T2)."
say ""
n=1
for idx in $TODO_IDX; do
  t="${TBL_NAME[$idx]}"
  case "$(engine_of "$t")" in
    *ReplacingMergeTree) tag="RMT       · 3b var" ;;
    *)                   tag="MergeTree · 3b YOK" ;;
  esac
  printf '  %2d/%d  %-26s %s  (%s satır/host)\n' "$n" "$N_WORK" "$t" "$tag" "$(rows_of "$t")"
  n=$((n + 1))
done
say ""
say "  ADIM 1 DDL'leri : $DDL_FILE"
say "  Fotoğraf (önce) : $BEFORE_FILE"

# =====================================================================
# DRY-RUN
# =====================================================================
print_table_plan() { # <sıra> <tablo> <motor>
  local nn="$1" t="$2" eng="$3" h
  printf '\n%s[%s/%s] %s%s\n' "$c_bld" "$nn" "$N_WORK" "$t" "$c_off"
  printf '   ADIM 1  @%-18s CREATE TABLE %s.%s_unified ON CLUSTER %s (…)\n' \
    "$CH_HOST" "$DATABASE" "$t" "$CLUSTER"
  printf '           %-19s   ENGINE = …(%s, %s%s)\n' "" \
    "'…/state/${t}'" "'{shard}-{replica}'" ""
  for h in ${INSERT_HOSTS[@]+"${INSERT_HOSTS[@]}"}; do
    printf '   ADIM 2  @%-18s INSERT INTO %s_unified SELECT * FROM %s\n' "$h" "$t" "$t"
  done
  printf '   ADIM 3  @%-18s RENAME TABLE %s TO %s_old, %s_unified TO %s ON CLUSTER %s\n' \
    "$CH_HOST" "$t" "$t" "$t" "$t" "$CLUSTER"
  case "$eng" in
    *ReplacingMergeTree)
      for h in ${INSERT_HOSTS[@]+"${INSERT_HOSTS[@]}"}; do
        printf '   ADIM 3b @%-18s INSERT INTO %s SELECT * FROM %s_old\n' "$h" "$t" "$t"
      done
      ;;
    *)
      printf '   ADIM 3b  %-18s ATLANDI — %s MergeTree; yakalama satırları İKİYE KATLARDI (T1)\n' "" "$t"
      ;;
  esac
  printf '   ADIM 4  @%-18s %s host da TEK /state/ yolunda ve AYNI sayıda olmalı — değilse DUR\n' \
    "$CH_HOST" "$CLUSTER_HOSTS"
}

if [ "$MODE" = "dry-run" ]; then
  hdr "DRY-RUN — koşulacak ifadeler (hiçbiri çalıştırılmadı)"
  n=1
  for idx in $TODO_IDX; do
    print_table_plan "$n" "${TBL_NAME[$idx]}" "$(engine_of "${TBL_NAME[$idx]}")"
    n=$((n + 1))
  done
  say ""
  hdr "DRY-RUN BİTTİ — hiçbir şey değişmedi"
  say "  Gerçekten koşmak için AYNI komuta ${c_bld}--apply${c_off} ekle."
  say "  ADIM 5 (yedekleri DROP) bu script'te YOK; göçten sonra birkaç gün bekle."
  say ""
  exit 0
fi

# =====================================================================
# APPLY
# =====================================================================
if [ "$ASSUME_YES" -eq 0 ]; then
  say ""
  printf '%s%s tablo RENAME edilecek. ADIM 5 koşulmadıkça her adım geri alınabilir.%s\n' \
    "$c_yel" "$N_WORK" "$c_off"
  printf 'Devam etmek için EVET yaz: '
  IFS= read -r confirm
  [ "$confirm" = "EVET" ] || abort "Onay alınmadı ('$confirm'). HİÇBİR ŞEY DEĞİŞMEDİ."
fi

hdr "ADIM 1 — '_unified' tabloları kuruluyor"
created=0; reused=0
SINK="${TMPDIR_RUN}/sink.txt"
for idx in $TODO_IDX; do
  t="${TBL_NAME[$idx]}"
  if in_list "${t}_unified" "$EXISTING_SUFFIXED"; then
    dim "$t — '_unified' zaten var, ADIM 1 atlandı (--resume)"
    reused=$((reused + 1)); continue
  fi
  ch_run "$CH_HOST" "${TBL_DDL[$idx]}" "ADIM 1: ${t}_unified kurma" "$SINK"
  created=$((created + 1))
done
ok "ADIM 1 tamam: $created kuruldu, $reused mevcut kullanıldı"

# ADIM 4 doğrulayıcı. İKİ ŞART birden:
#   (a) tablo TEK zookeeper_path'te ve yolda '/{shard}/' yok  ← 4a
#   (b) her host aynı satır sayısını veriyor                  ← 4b
# Yalnız (b) yetmez: 0 satırlı tablolar göçten ÖNCE de sonra da her
# host'ta 0 verir, yani sayı testi tek başına hiçbir şey kanıtlamaz.
# Replikasyon asenkron olduğu için (b) yakınsayana kadar beklenir.
VERIFY_COUNT=""; VERIFY_FINAL=""; VERIFY_WHY=""
verify_table() { # <tablo> → 0 tamam / 1 değil
  local t="$1" deadline=$((SECONDS + VERIFY_TIMEOUT)) paths path uniqc minc hosts
  ch_run "$CH_HOST" \
    "SELECT toString(uniqExact(zookeeper_path)), any(zookeeper_path), toString(count()) FROM clusterAllReplicas('${CLUSTER}', system.replicas) WHERE database = currentDatabase() AND table = '${t}' FORMAT TSV" \
    "ADIM 4a: $t replikasyon grubu" "$SINK"
  paths="$(cut -f1 "$SINK")"; path="$(cut -f2 "$SINK")"; hosts="$(cut -f3 "$SINK")"
  if [ "$paths" != "1" ]; then
    VERIFY_WHY="$paths farklı zookeeper_path — tablo hâlâ birden çok replikasyon grubunda"
    return 1
  fi
  case "$path" in
    *'/{shard}/'*)
      VERIFY_WHY="zookeeper_path hâlâ '/{shard}/' içeriyor: $path"
      return 1 ;;
  esac
  if [ "$hosts" != "$CLUSTER_HOSTS" ]; then
    VERIFY_WHY="tablo $hosts host'ta görünüyor, küme $CLUSTER_HOSTS host"
    return 1
  fi
  while :; do
    ch_run "$CH_HOST" \
      "SELECT toString(uniqExact(c)), toString(min(c)), toString(count()) FROM (SELECT hostName() AS h, count() AS c FROM clusterAllReplicas('${CLUSTER}', currentDatabase(), ${t}) GROUP BY h) FORMAT TSV" \
      "ADIM 4b: $t satır sayısı" "$SINK"
    uniqc="$(cut -f1 "$SINK")"; minc="$(cut -f2 "$SINK")"; hosts="$(cut -f3 "$SINK")"
    if [ "$uniqc" = "1" ] && [ "$hosts" = "$CLUSTER_HOSTS" ]; then
      VERIFY_COUNT="$minc"; VERIFY_WHY=""
      # ReplacingMergeTree'de HAM count() birleşme (merge) olana kadar
      # şişik durur: ADIM 3b yakalaması aynı satırları tekrar yazar.
      # Eşitlik testi HAM sayıda kalmalı (ayrışmayı o yakalar); ama
      # operatöre anlamlı sayıyı göç dosyasının 4c'si gibi FINAL verir.
      VERIFY_FINAL=""
      case "$(engine_of "$t")" in
        *ReplacingMergeTree)
          ch_run "$CH_HOST" "SELECT toString(count()) FROM ${t} FINAL" \
            "ADIM 4c: $t FINAL sayısı" "$SINK"
          VERIFY_FINAL="$(cat "$SINK")"
          ;;
      esac
      return 0
    fi
    if [ "$SECONDS" -ge "$deadline" ]; then
      VERIFY_WHY="host'lar ${VERIFY_TIMEOUT}s içinde yakınsamadı ($uniqc farklı sayı, $hosts host)"
      return 1
    fi
    sleep 3
  done
}

hdr "ADIM 2 + 3 + 4 — tablo başına"
SUMMARY="${TMPDIR_RUN}/summary.tsv"
: > "$SUMMARY"
OLD_TABLES=""
n=1
RUN_START=$SECONDS
for idx in $TODO_IDX; do
  t="${TBL_NAME[$idx]}"
  eng="$(engine_of "$t")"
  t0=$SECONDS
  printf '\n%s[%s/%s] %s%s   %s\n' "$c_bld" "$n" "$N_WORK" "$t" "$c_off" "$eng"

  for h in ${INSERT_HOSTS[@]+"${INSERT_HOSTS[@]}"}; do
    ch_run "$h" "INSERT INTO ${t}_unified SELECT * FROM ${t}" "ADIM 2: $t @ $h" "$SINK"
    dim "ADIM 2  @$h  kopyalandı"
  done

  ch_run "$CH_HOST" \
    "RENAME TABLE ${t} TO ${t}_old, ${t}_unified TO ${t} ON CLUSTER ${CLUSTER}" \
    "ADIM 3: $t takas" "$SINK"
  dim "ADIM 3  @$CH_HOST  takas edildi → ${t}_old"
  OLD_TABLES="$OLD_TABLES ${t}_old"

  case "$eng" in
    *ReplacingMergeTree)
      for h in ${INSERT_HOSTS[@]+"${INSERT_HOSTS[@]}"}; do
        ch_run "$h" "INSERT INTO ${t} SELECT * FROM ${t}_old" "ADIM 3b: $t @ $h" "$SINK"
      done
      dim "ADIM 3b yakalama koştu (RMT, idempotent)"
      ;;
    *)
      dim "ADIM 3b ATLANDI — MergeTree; yakalama satırları ikiye katlardı (T1)"
      ;;
  esac

  set +e
  verify_table "$t"; vrc=$?
  set -e
  dt=$((SECONDS - t0))
  if [ "$vrc" -ne 0 ]; then
    abort "ADIM 4 BAŞARISIZ — '$t': $VERIFY_WHY" \
      "SONRAKİ TABLOLARA GEÇİLMEDİ. Yedek duruyor (ADIM 5 koşulmadı)." \
      "İncele:" \
      "  SELECT hostName(), count() FROM clusterAllReplicas('$CLUSTER', '$DATABASE', $t) GROUP BY 1;" \
      "  SELECT table, zookeeper_path FROM clusterAllReplicas('$CLUSTER', system.replicas) WHERE table = '$t';" \
      "Geri al (tek ifade):" \
      "  RENAME TABLE $t TO ${t}_unified, ${t}_old TO $t ON CLUSTER $CLUSTER;" \
      "Düzelttikten sonra --resume ile devam edebilirsin."
  fi
  printf '%s\t%s\t%s\t%s\t%s\n' "$t" "$VERIFY_COUNT" "${VERIFY_FINAL:--}" "$dt" "$eng" >> "$SUMMARY"
  if [ -n "$VERIFY_FINAL" ]; then
    printf '   %s✓%s ADIM 4  %s host da tek /state/ yolunda · %s ham / %s FINAL satır · %ss\n' \
      "$c_grn" "$c_off" "$CLUSTER_HOSTS" "$VERIFY_COUNT" "$VERIFY_FINAL" "$dt"
  else
    printf '   %s✓%s ADIM 4  %s host da tek /state/ yolunda · %s satır · %ss\n' \
      "$c_grn" "$c_off" "$CLUSTER_HOSTS" "$VERIFY_COUNT" "$dt"
  fi
  n=$((n + 1))
done
RUN_TOTAL=$((SECONDS - RUN_START))

# ------------------------------------------------------------- ÖZET
snapshot "$AFTER_FILE"

hdr "ÖZET — $N_WORK tablo birleştirildi, toplam ${RUN_TOTAL}s"
printf '  %-28s %10s %10s %8s  %s\n' "TABLO" "HAM" "FINAL" "SÜRE" "MOTOR"
printf '  %-28s %10s %10s %8s  %s\n' "----------------------------" "----------" "----------" "--------" "-----"
while IFS="$(printf '\t')" read -r st sc sf sd se; do
  printf '  %-28s %10s %10s %7ss  %s\n' "$st" "$sc" "$sf" "$sd" "$se"
done < "$SUMMARY"
dim "HAM = count() · FINAL = count() FINAL (RMT'de 3b yakalaması geçici tekrar bırakır)"

say ""
ok "Fotoğraf (önce) : $BEFORE_FILE"
ok "Fotoğraf (sonra): $AFTER_FILE"
dim "kıyas:  diff \"$BEFORE_FILE\" \"$AFTER_FILE\""

say ""
say "${c_bld}YEDEKLER — SİLİNMEDİ${c_off}"
say "  Aşağıdaki '_old' tablolar duruyor. Geri alma tablo başına tek ifade:"
say "    RENAME TABLE <t> TO <t>_unified, <t>_old TO <t> ON CLUSTER $CLUSTER;"
for o in $OLD_TABLES; do printf '    %s\n' "$o"; done

say ""
say "${c_yel}${c_bld}ADIM 5'İ ŞİMDİ KOŞMA.${c_off}"
say "  Uygulama en az birkaç gün sorunsuz çalıştıktan SONRA, göç dosyasındaki"
say "  DROP satırlarını tek tek koştur. ADIM 5 sonrası GERİ DÖNÜŞ YOKTUR — bu"
say "  yüzden script onu kapsamıyor."
say ""
say "  Şimdi doğrula:  /inbox (problems) · /settings (alert rules) · giriş"
say "  Uygulama yeniden başlatma GEREKMİYOR: ${MIN_APP_VERSION}+ state tablosunun"
say "  ZK yolunu KÜMEDEN okur, bir sonraki boot'ta birleşik yolu görür."
say ""
