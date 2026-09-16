"""빨대 축구 아이콘 생성기 — python3 gen.py 로 icon.svg / icon-small.svg 를 다시 만든다.
래스터·ico 는 README 의 magick 명령으로."""
import math

GRAD = '''  <defs>
    <linearGradient id="g" x1="0" y1="0" x2="1" y2="1">
      <stop offset="0%"   stop-color="#6366f1"/>
      <stop offset="50%"  stop-color="#8b5cf6"/>
      <stop offset="100%" stop-color="#06b6d4"/>
    </linearGradient>
    <clipPath id="ball"><circle cx="{cx}" cy="{cy}" r="{r}"/></clipPath>
  </defs>

  <rect x="112" y="112" width="800" height="800" rx="184" fill="url(#g)"/>
'''

def pent(x, y, r, rot):
    pts = [(x + r*math.cos(rot + i*2*math.pi/5), y + r*math.sin(rot + i*2*math.pi/5)) for i in range(5)]
    return "M" + " L".join(f"{a:.1f} {b:.1f}" for a, b in pts) + " Z"

def ball(cx, cy, r, tilt):
    """흰 공 + 브랜드색 오각형 무늬(가운데 1 + 테두리에 걸친 5)."""
    rot = -math.pi/2 + tilt
    ps = [pent(cx, cy, r*0.38, rot)]
    for i in range(5):
        t = rot + math.pi/5 + i*2*math.pi/5
        ps.append(pent(cx + math.cos(t)*r*0.94, cy + math.sin(t)*r*0.94, r*0.36, t))
    body = "\n    ".join(f'<path d="{d}"/>' for d in ps)
    ring = r * 0.045  # 흰 테: 테두리 무늬가 윤곽을 파먹어 톱니바퀴처럼 보이지 않게
    return f'''  <circle cx="{cx}" cy="{cy}" r="{r}" fill="#ffffff"/>
  <g clip-path="url(#ball)" fill="#6366f1">
    {body}
  </g>
  <circle cx="{cx}" cy="{cy}" r="{r - ring/2:.1f}" fill="none" stroke="#ffffff" stroke-width="{ring:.1f}"/>
'''

def straw(x0, y0, x1, y1, w):
    return f'  <line x1="{x0:.0f}" y1="{y0:.0f}" x2="{x1:.0f}" y2="{y1:.0f}" stroke="#ffffff" stroke-width="{w}" stroke-linecap="round"/>\n'

def big():
    ex, ey, er = 300, 300, 72            # 죽방 머리
    bx, by, br = 664, 654, 150           # 공
    ang = math.atan2(by - ey, bx - ex)
    ux, uy = math.cos(ang), math.sin(ang)
    nx, ny = -uy, ux
    s0, s1 = er * 0.35, er + 150         # 빨대 구간(죽방 머리 중심에서 거리) — 머리 안에서 시작해 입에서 나온 듯
    svg = f'''<svg width="1024" height="1024" viewBox="0 0 1024 1024" xmlns="http://www.w3.org/2000/svg">
  <!--
    빨대 축구(vector-soccer) 아이콘 — 큰 크기용(256px 이상). gen.py 로 생성.

    teaveloper 공용 규격: 800x800 라운드 사각형(rx 184) · 공식 그라데이션 · 흰색 죽방
    (원보다 밑변이 넓고 높이 ≈ 원 지름 — 좁고 길면 열쇠구멍으로 보인다).

    이 앱만의 것: 죽방이 빨대로 바람을 불어 축구공을 민다(왼쪽 위 → 오른쪽 아래 한 줄).
    공 무늬는 브랜드 색(#6366f1). 작은 크기(16·32·48px)는 icon-small.svg.
  -->
{GRAD.format(cx=bx, cy=by, r=br)}
  <!-- 죽방 — 원 r{er} · 밑변 172 · 높이 156 -->
  <g fill="#ffffff">
    <circle cx="{ex}" cy="{ey}" r="{er}"/>
    <path d="M{ex} {ey+48} L{ex-86} {ey+204} L{ex+86} {ey+204} Z"/>
  </g>

  <!-- 빨대 -->
{straw(ex+ux*s0, ey+uy*s0, ex+ux*s1, ey+uy*s1, 14)}
  <!-- 바람 줄기(빨대 끝 → 공) -->
  <g stroke="#ffffff" stroke-width="11" stroke-linecap="round" opacity="0.85">
'''
    gap0, gap1 = s1 + 40, math.hypot(bx-ex, by-ey) - br - 30
    for off, shrink in ((0, 0), (-44, 18), (44, 18)):
        a0, a1 = gap0 + shrink, gap1 - shrink
        svg += f'    <line x1="{ex+ux*a0+nx*off:.0f}" y1="{ey+uy*a0+ny*off:.0f}" x2="{ex+ux*a1+nx*off:.0f}" y2="{ey+uy*a1+ny*off:.0f}"/>\n'
    svg += '  </g>\n\n  <!-- 축구공 -->\n' + ball(bx, by, br, 0.12) + '</svg>\n'
    return svg

def small():
    bx, by, br = 590, 590, 230
    svg = f'''<svg width="1024" height="1024" viewBox="0 0 1024 1024" xmlns="http://www.w3.org/2000/svg">
  <!--
    빨대 축구 아이콘 — 작은 크기용(16·32·48px). gen.py 로 생성.
    16px 에 죽방·빨대·바람·공을 다 넣으면 뭉개진다. 공을 크게, 빨대 하나만 남겼다.
  -->
{GRAD.format(cx=bx, cy=by, r=br)}
  <!-- 빨대 -->
{straw(250, 250, 380, 380, 34)}
  <!-- 축구공 -->
{ball(bx, by, br, 0.12)}</svg>
'''
    return svg

open("icon.svg", "w").write(big())
open("icon-small.svg", "w").write(small())
