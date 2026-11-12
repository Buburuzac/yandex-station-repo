#!/usr/bin/env sh
# Сборка stationctl под Яндекс.Станцию (aarch64, статик, без CGO).
# Кладёт бинарь в packages/stationctl/stationctl — дальше pack-packages.sh/one-pkg.sh.
set -eu

HERE=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
OUT="$HERE/packages/stationctl"
mkdir -p "$OUT"

echo ">> go build linux/arm64 static -> $OUT/stationctl"
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
  go build -trimpath -ldflags="-s -w" \
  -o "$OUT/stationctl" "$HERE/cmd/stationctl"

ls -la "$OUT/stationctl"
file "$OUT/stationctl" 2>/dev/null || true
echo ">> готово. Публикация: ./scripts/pack-packages.sh && ./scripts/push-pkg-branch.sh"
echo "   проверка на колонке: apkg update && apkg install stationctl && stationctl --version"
