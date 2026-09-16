#!/usr/bin/env bash
# 교사 배포용 Windows exe 빌드. 리눅스에서 그대로 크로스컴파일된다(CGO 불필요 — 순수 Go).
#
#   ./build.sh                       # dist/vector-soccer.exe (콘솔 창 보임 = 닫으면 종료)
#   ./build.sh dist/quiz.exe gui     # 콘솔 창 없이(-H windowsgui), 백그라운드 실행
set -euo pipefail

OUT="${1:-dist/vector-soccer.exe}"
MODE="${2:-console}"
mkdir -p "$(dirname "$OUT")"

# 골격 단계 기본값은 console: 콘솔 창이 곧 "실행 중" 표시이자 종료 수단(창 닫기).
# 추후 systray 를 붙이면 gui 모드로 전환.
LDFLAGS="-s -w"
if [ "$MODE" = "gui" ]; then
  LDFLAGS="-H windowsgui -s -w"
fi

GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
  go build -trimpath -ldflags "$LDFLAGS" -o "$OUT" .

echo "built: $OUT ($(du -h "$OUT" | cut -f1))  mode=$MODE"
