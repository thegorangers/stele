#!/usr/bin/env bash
# Порождает таблицу измерений для спеки stele.
# Источник правды — дефолтная ветка origin каждого репозитория.
# Ищет конфиги на ЛЮБОЙ глубине (engineering/contracts/buf.yaml лежит не в корне).
set -uo pipefail
ROOT="${1:-$PWD}"
SNAP="$(mktemp -d)"
trap 'rm -rf "$SNAP"' EXIT

repos=$(cd "$ROOT" && find . -maxdepth 3 -name .git | sed 's|/\.git$||;s|^\./||' | sort)

for r in $repos; do
  d="$ROOT/$r"
  def=$(git -C "$d" symbolic-ref -q --short refs/remotes/origin/HEAD 2>/dev/null | sed 's|^origin/||')
  [ -z "$def" ] && { git -C "$d" rev-parse -q --verify origin/master >/dev/null && def=master || def=main; }
  git -C "$d" rev-parse -q --verify "origin/$def" >/dev/null || continue
  git -C "$d" ls-tree -r --name-only "origin/$def" 2>/dev/null \
  | grep -E '(^|/)(buf\.yaml|buf\.gen\.yaml|buf\.lock|Makefile|\.gitlab-ci\.yml)$' \
  | while read -r f; do
      out="$SNAP/${r//\//__}__$(echo "$f" | tr '/' '_')"
      git -C "$d" show "origin/$def:$f" > "$out" 2>/dev/null && echo "$r/$f" >> "$SNAP/.index"
    done
done

pick() { ls $SNAP/*$1 2>/dev/null; }
cnt()  { grep -hE "$1" $(pick "$2") 2>/dev/null | grep -vE '^[[:space:]]*#' | wc -l; }

echo "## Репозитории и конфиги (дефолтные ветки origin)"
for f in buf.yaml buf.gen.yaml buf.lock; do printf '%-14s %s\n' "$f" "$(grep -c "/$f\$" $SNAP/.index)"; done
echo "version: v1  → $(grep -rlE '^version:[[:space:]]*v1' $(pick 'buf*.yaml') 2>/dev/null | xargs -r -n1 basename | tr '\n' ' ')"
echo
echo "## Ключи buf.gen.yaml"
for k in local remote out opt directory paths git_repo; do
  printf '%-12s %s\n' "$k" "$(cnt "^[[:space:]]*-?[[:space:]]*$k:" buf.gen.yaml)"; done
for k in managed override disable strategy types; do
  printf '%-12s %s\n' "$k" "$(cnt "^[[:space:]]*$k:" buf.gen.yaml)"; done
echo
echo "## Плагины"
grep -hE '^[[:space:]]*-?[[:space:]]*(local|remote):' $(pick buf.gen.yaml) 2>/dev/null \
  | sed 's/^[[:space:]]*-*[[:space:]]*//' | sort | uniq -c | sort -rn
echo
echo "## Селекторы managed"
grep -hE '(file_option|path|module|value):' $(pick buf.gen.yaml) 2>/dev/null \
  | sed 's/^[[:space:]]*//' | grep -E '^-?[[:space:]]*(file_option|module|path):' | sort | uniq -c
echo
echo "## Вызовы buf"
for c in "buf generate" "buf export" "buf lint" "buf breaking" "buf push"; do
  printf '%-14s Makefile=%-4s CI=%s\n' "$c" \
    "$(grep -h "$c" $(pick Makefile) 2>/dev/null | grep -vE '^[[:space:]]*(#|@#)' | wc -l)" \
    "$(grep -h "$c" $(pick .gitlab-ci.yml) 2>/dev/null | grep -vE '^[[:space:]]*#' | wc -l)"
done
echo
echo "## Флаги (Makefile)"
for fl in output path exclude-imports include-imports; do
  printf -- '--%-17s %s\n' "$fl" "$(grep -ho -- "--$fl" $(pick Makefile) 2>/dev/null | wc -l)"; done
echo
echo "## Граф зависимостей"
echo "внутренние рёбра: $(grep -h 'buf export' $(pick Makefile) 2>/dev/null | grep -cE 'git.example.com/acme|GIT_BASE')"
echo "через BSR:        $(grep -h 'buf export' $(pick Makefile) 2>/dev/null | grep -c 'buf.build/')"
echo "напрямую git:     $(grep -h 'buf export' $(pick Makefile) 2>/dev/null | grep -cE 'github.com')"
echo
echo "## Внешние импорты в спеках владельцев"
find "$ROOT"/services/*/api "$ROOT"/tests "$ROOT"/engineering "$ROOT"/mobiles -name '*.proto' 2>/dev/null \
  | grep -v third_party | xargs -r grep -hoE '^import "[^"]+"' 2>/dev/null \
  | sed 's/import "//;s/"//' | grep -v '^acme/' | sort | uniq -c | sort -rn
