#!/usr/bin/env bash
# ============================================================
# Video Download Helper — сброс счётчика загрузок
# Работает на macOS и Linux
# Поддерживает: Chrome, Chromium, Yandex, Brave, Edge, Opera, Vivaldi, Arc
# ============================================================
set -euo pipefail

# ---------- цвета ----------
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; NC='\033[0m'
info()  { echo -e "${GREEN}[INFO]${NC}  $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC}  $*"; }
err()   { echo -e "${RED}[ERR]${NC}   $*"; }
header(){ echo -e "\n${CYAN}━━━ $* ━━━${NC}"; }

# ---------- имена расширений ----------
KNOWN_NAMES=(
    "Video Download Helper"
    "Video DownloadHelper"
    "Video Downloader"
    "Video Downloader professional"
    "SaveFrom.net helper"
    "SaveFrom.net помощник"
)

# ---------- определяем ОС и собираем пути ----------
declare -a BROWSER_NAMES=()
declare -a BROWSER_ROOTS=()

detect_browser_roots() {
    local os_type
    os_type="$(uname -s)"

    if [[ "$os_type" == "Darwin" ]]; then
        local base="$HOME/Library/Application Support"
        BROWSER_NAMES+=("Chrome");         BROWSER_ROOTS+=("$base/Google/Chrome")
        BROWSER_NAMES+=("Chrome Beta");    BROWSER_ROOTS+=("$base/Google/Chrome Beta")
        BROWSER_NAMES+=("Chrome Dev");     BROWSER_ROOTS+=("$base/Google/Chrome Dev")
        BROWSER_NAMES+=("Chromium");       BROWSER_ROOTS+=("$base/Chromium")
        BROWSER_NAMES+=("Yandex");         BROWSER_ROOTS+=("$base/Yandex/YandexBrowser")
        BROWSER_NAMES+=("Brave");          BROWSER_ROOTS+=("$base/BraveSoftware/Brave-Browser")
        BROWSER_NAMES+=("Edge");           BROWSER_ROOTS+=("$base/Microsoft Edge")
        BROWSER_NAMES+=("Opera");          BROWSER_ROOTS+=("$base/com.operasoftware.Opera")
        BROWSER_NAMES+=("Vivaldi");        BROWSER_ROOTS+=("$base/Vivaldi")
        BROWSER_NAMES+=("Arc");            BROWSER_ROOTS+=("$base/Arc")
    elif [[ "$os_type" == "Linux" ]]; then
        BROWSER_NAMES+=("Chrome");         BROWSER_ROOTS+=("$HOME/.config/google-chrome")
        BROWSER_NAMES+=("Chrome Beta");    BROWSER_ROOTS+=("$HOME/.config/google-chrome-beta")
        BROWSER_NAMES+=("Chrome Dev");     BROWSER_ROOTS+=("$HOME/.config/google-chrome-dev")
        BROWSER_NAMES+=("Chromium");       BROWSER_ROOTS+=("$HOME/.config/chromium")
        BROWSER_NAMES+=("Yandex");         BROWSER_ROOTS+=("$HOME/.config/yandex-browser")
        BROWSER_NAMES+=("Brave");          BROWSER_ROOTS+=("$HOME/.config/BraveSoftware/Brave-Browser")
        BROWSER_NAMES+=("Edge");           BROWSER_ROOTS+=("$HOME/.config/microsoft-edge")
        BROWSER_NAMES+=("Opera");          BROWSER_ROOTS+=("$HOME/.config/opera")
        BROWSER_NAMES+=("Vivaldi");        BROWSER_ROOTS+=("$HOME/.config/vivaldi")
        BROWSER_NAMES+=("Arc");            BROWSER_ROOTS+=("$HOME/.config/arc")
    else
        err "Неподдерживаемая ОС: $os_type (только macOS и Linux)"
        exit 1
    fi
}

# ---------- получить имя расширения из manifest.json ----------
# использует python3 для парсинга JSON + обработки __MSG_ локализации
get_extension_name() {
    local manifest="$1"
    python3 -c "
import sys, json, os, glob

with open(sys.argv[1], 'r') as f:
    d = json.load(f)

name = d.get('name', '')
if name.startswith('__MSG_') and name.endswith('__'):
    key = name.replace('__MSG_', '').replace('__', '')
    manifest_dir = os.path.dirname(sys.argv[1])
    for loc in ['en', 'ru']:
        msg_file = os.path.join(manifest_dir, '_locales', loc, 'messages.json')
        if os.path.exists(msg_file):
            with open(msg_file) as mf:
                md = json.load(mf)
            if key in md:
                name = md[key].get('message', name)
                break
print(name)
" "$manifest" 2>/dev/null
}

# ---------- поиск ВСЕХ ID расширений в профиле ----------
# выводит строки вида: ext_id|ext_name (по одной на каждое найденное расширение)
find_extension_ids() {
    local profile_dir="$1"
    local ext_root="$profile_dir/Extensions"

    [[ -d "$ext_root" ]] || return

    for ext_dir in "$ext_root"/*/; do
        [[ -d "$ext_dir" ]] || continue
        local eid
        eid="$(basename "$ext_dir")"
        [[ "$eid" == "Temp" ]] && continue

        # ищем manifest.json в подпапке версии
        local manifest
        manifest="$(ls "$ext_dir"/*/manifest.json 2>/dev/null | head -1)"
        [[ -n "$manifest" && -f "$manifest" ]] || continue

        local ext_name
        ext_name="$(get_extension_name "$manifest")"
        [[ -z "$ext_name" ]] && continue

        for known in "${KNOWN_NAMES[@]}"; do
            if [[ "$ext_name" == "$known" ]]; then
                echo "${eid}|${ext_name}"
                break  # не return — ищем дальше в других папках!
            fi
        done
    done
}

# ---------- MAIN ----------
main() {
    header "Поиск Video Downloader в браузерах"

    detect_browser_roots

    local found_any=0

    for ((i = 0; i < ${#BROWSER_NAMES[@]}; i++)); do
        local browser_name="${BROWSER_NAMES[$i]}"
        local browser_root="${BROWSER_ROOTS[$i]}"

        [[ -d "$browser_root" ]] || continue
        info "Проверяю: ${CYAN}${browser_name}${NC} ($browser_root)"

        # перебираем профили: Default, Profile 1..N
        local profiles=("$browser_root/Default")
        for p in "$browser_root"/Profile\ [0-9]*; do
            [[ -d "$p" ]] && profiles+=("$p")
        done

        for profile_dir in "${profiles[@]}"; do
            [[ -d "$profile_dir" ]] || continue

            while IFS= read -r result; do
                [[ -z "$result" ]] && continue
                local ext_id="${result%%|*}"
                local ext_name="${result#*|}"

                found_any=1
                header "НАЙДЕНО: ${GREEN}${ext_name}${NC}"
                echo -e "  Браузер:  ${browser_name}"
                echo -e "  ID:       ${ext_id}"
                echo -e "  Профиль:  ${profile_dir}"

                # ---------- сброс ----------
                local storage_dir="$profile_dir/Local Extension Settings/$ext_id"
                local sync_dir="$profile_dir/Sync Extension Settings/$ext_id"
                local indexeddb="$profile_dir/IndexedDB/chrome-extension_${ext_id}_0.indexeddb.leveldb"

                local deleted=0

                for dir in "$storage_dir" "$sync_dir" "$indexeddb"; do
                    if [[ -d "$dir" ]]; then
                        info "Удаляю: ${dir}"
                        rm -rf "$dir"
                        deleted=1
                    fi
                done

                if [[ "$deleted" -eq 1 ]]; then
                    echo -e "${GREEN}✓ Счётчик сброшен!${NC}"
                    echo -e "${YELLOW}⚠️  Перезапусти браузер, чтобы изменения вступили в силу.${NC}"
                else
                    warn "Данные расширения уже отсутствуют."
                fi
            done <<< "$(find_extension_ids "$profile_dir" 2>/dev/null)"
        done
    done

    if [[ "$found_any" -eq 0 ]]; then
        echo ""
        warn "Расширение Video Download Helper не найдено."
        echo ""
        echo "  Проверь вручную — запусти в терминале:"
        echo ""
        echo "  find ~/Library/Application\\ Support ~/.config -path '*/Extensions/*/manifest.json' 2>/dev/null \\"
        echo "    | while read f; do echo \"=== \$f ===\"; python3 -c \"import json; print(json.load(open('\$f')).get('name','?'))\" 2>/dev/null; done"
        echo ""
        exit 1
    fi
}

main "$@"
