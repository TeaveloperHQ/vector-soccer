package main

import (
	"math"
	"time"
)

// 경기 한 판의 물리·규칙. 서버가 단일 진실원천이다 — 학생 화면은 받은 상태를 그리기만 한다.
// Match 의 메서드는 Hub.run() 고루틴 안에서만 호출된다(락 없음).
//
// 좌표계: 경기장 가로 FieldW × 세로 FieldH (추상 단위). 왼쪽 골대는 side 0 이 지키고
// 오른쪽 골대는 side 1 이 지킨다. 학생 화면은 자기가 오른쪽으로 공격하도록 돌려서 그린다.

const (
	FieldW = 100.0
	FieldH = 60.0

	GoalTop    = 22.0 // 골대 입구 y 범위
	GoalBottom = 38.0
	GoalDepth  = 5.0

	BallR = 2.0

	// 바람(빨대) — 드래그 길이 비율(0~1)에 비례하는 힘을 BlowDur 동안 가한다.
	// 공 질량을 1 로 두므로 힘 = 가속도. 최대로 불면 Δv = BlowMaxF × BlowDur.
	BlowMaxF = 60.0
	BlowDur  = 0.35
	BlowMin  = 0.05 // 이보다 짧은 드래그는 무시(실수 터치)
	Cooldown = 3 * time.Second

	// 헛방질(쉬는 중에 불기 시도) 벌칙: 남은 쿨타임 +MissPenalty, 최대 MaxCooldown 까지.
	MissPenalty = 1 * time.Second
	MaxCooldown = 6 * time.Second

	Friction    = 4.0  // 운동 마찰(속력과 무관한 일정 감속)
	Drag        = 0.25 // 공기 저항(속력 비례 감속, 1/s)
	Restitution = 0.8  // 벽 반발 계수

	TickDT = 1.0 / 60

	ReadyDur  = 3 * time.Second // 시작 전 카운트다운
	GoalPause = 2 * time.Second // 골 세리머니 후 킥오프
	// 연결이 끊기면 돌아올 때까지 기다린다. 둘 다 끊긴 채 이만큼 지나면 기록 없이 정리.
	BothGoneWait = 10 * time.Minute

	// 선축: 킥오프 때 선축인 쪽만 먼저 불 수 있다. 이 시간 안에 안 불면 둘 다 불 수 있게 풀린다.
	KickoffWait = 5 * time.Second
)

type phase string

const (
	phReady phase = "ready"
	phPlay  phase = "play"
	phGoal  phase = "goal"
	phPause phase = "pause" // 한쪽 연결 끊김 — 재접속 대기
	phEnd   phase = "end"
)

type Rules struct {
	TimeSec int `json:"timeSec"` // 0 = 시간 제한 없음
	Goals   int `json:"goals"`   // 0 = 목표 골 없음
	// GoldenGoal: 시간이 끝났는데 동점이면 다음 골이 결승골(토너먼트 — 무승부 없음).
	GoldenGoal bool `json:"goldenGoal,omitempty"`
}

func (r Rules) valid() bool {
	okT := r.TimeSec == 0 || (r.TimeSec >= 30 && r.TimeSec <= 600)
	okG := r.Goals == 0 || (r.Goals >= 1 && r.Goals <= 20)
	return okT && okG && (r.TimeSec > 0 || r.Goals > 0)
}

type vec struct{ X, Y float64 }

func (a vec) add(b vec) vec       { return vec{a.X + b.X, a.Y + b.Y} }
func (a vec) scale(k float64) vec { return vec{a.X * k, a.Y * k} }
func (a vec) len() float64        { return math.Hypot(a.X, a.Y) }
func (a vec) finite() bool        { return !math.IsNaN(a.X+a.Y) && !math.IsInf(a.X+a.Y, 0) }

// blow 는 지금 공에 작용 중인 바람 하나.
type blow struct {
	side   int
	force  vec
	remain float64 // 남은 작용 시간(초)
}

type Match struct {
	id      string
	rules   Rules
	players [2]*Player

	phase     phase
	phaseLeft time.Duration // ready/goal: 남은 시간, pause: 기다린 시간

	ball    vec
	vel     vec
	blows   []blow
	score   [2]int
	nBlows  [2]int
	nMisses [2]int
	cdUntil [2]time.Time     // 이 시각까지 쉬어야 함
	cdTotal [2]time.Duration // 지금 쿨타임의 전체 길이(벌칙 포함) — 화면 게이지용
	scorer  int              // 직전 골을 넣은 side

	playLeft  time.Duration // 남은 경기 시간(시간 제한 없으면 무시)
	played    time.Duration // 실제 경기 진행 시간
	startedAt time.Time
	now       time.Time // 시뮬레이션 기준 시각(테스트에서 주입 가능)

	winner  int    // -1 무승부
	reason  string // "time" | "goals" | "blows" | "golden" | "forfeit"
	rematch [2]bool
	saved   bool
	golden  bool // 골든골 연장 중

	injury   time.Duration // 멈춘 시간(골 세리머니 등) — 정규 시간 뒤 추가시간으로
	inInjury bool          // 추가시간 진행 중

	firstKick int           // 경기 첫 선축(side)
	kickSide  int           // 다음 킥오프의 선축(-1 = 없음)
	kicker    int           // 경기 중 아직 선축을 기다리는 side(-1 = 자유)
	kickLeft  time.Duration // 선축 대기 남은 시간
	fixture   *Fixture      // 대회 경기면 그 대진
}

// first 는 첫 선축 side(-1 = 선축 없음).
func newMatch(id string, a, b *Player, rules Rules, now time.Time, first int) *Match {
	m := &Match{
		id: id, rules: rules, players: [2]*Player{a, b},
		startedAt: now, now: now, winner: -1,
		firstKick: first, kickSide: first, kicker: -1,
		playLeft: time.Duration(rules.TimeSec) * time.Second,
	}
	m.kickoff(phReady, ReadyDur)
	return m
}

func (m *Match) kickoff(ph phase, wait time.Duration) {
	m.ball = vec{FieldW / 2, FieldH / 2}
	m.vel = vec{}
	m.blows = nil
	m.resetCooldowns()
	m.phase = ph
	m.phaseLeft = wait
}

func (m *Match) resetCooldowns() {
	m.cdUntil = [2]time.Time{}
	m.cdTotal = [2]time.Duration{Cooldown, Cooldown}
}

// cooldownLeft 는 side 가 다시 불 수 있을 때까지 남은 시간.
func (m *Match) cooldownLeft(side int) time.Duration {
	if d := m.cdUntil[side].Sub(m.now); d > 0 {
		return d
	}
	return 0
}

type blowResult int

const (
	blowIgnored blowResult = iota // 경기 중이 아님·실수 터치 — 아무 일 없음
	blowOK
	blowMiss // 헛방질 — 쿨타임 벌칙
)

// applyBlow 는 학생의 바람 입력을 검증해 반영한다. d 는 경기장 좌표계의 방향×세기(길이 ≤ 1).
func (m *Match) applyBlow(side int, d vec) blowResult {
	if m.phase != phPlay || !d.finite() {
		return blowIgnored
	}
	l := d.len()
	if l < BlowMin {
		return blowIgnored
	}
	if m.kicker >= 0 && side != m.kicker {
		return blowIgnored // 상대 선축 차례 — 벌칙 없음
	}
	if left := m.cooldownLeft(side); left > 0 {
		add := MissPenalty
		if left+add > MaxCooldown {
			add = MaxCooldown - left
		}
		if add > 0 {
			m.cdUntil[side] = m.cdUntil[side].Add(add)
			m.cdTotal[side] += add
		}
		m.nMisses[side]++
		return blowMiss
	}
	if l > 1 {
		d = d.scale(1 / l)
	}
	m.blows = append(m.blows, blow{side: side, force: d.scale(BlowMaxF), remain: BlowDur})
	m.cdUntil[side] = m.now.Add(Cooldown)
	m.cdTotal[side] = Cooldown
	m.nBlows[side]++
	if side == m.kicker {
		m.kicker = -1
	}
	return blowOK
}

// step 은 dt 만큼 시뮬레이션을 진행한다.
func (m *Match) step(dt time.Duration) {
	m.now = m.now.Add(dt)
	switch m.phase {
	case phEnd:
		return
	case phPause:
		m.phaseLeft += dt // 기다린 시간
		return
	case phReady, phGoal:
		// 골 세리머니·재접속 후 카운트다운에도 시계는 흐르고, 그만큼 추가시간으로 쌓인다(첫 킥오프 전은 제외).
		if m.rules.TimeSec > 0 && !m.golden && (m.phase == phGoal || m.played > 0) {
			m.playLeft = max(0, m.playLeft-dt)
			if !m.inInjury {
				m.injury += dt
			}
		}
		m.phaseLeft -= dt
		if m.phaseLeft > 0 {
			return
		}
		m.phase = phPlay
		m.resetCooldowns() // 킥오프 때 쿨타임 초기화
		m.kicker, m.kickLeft, m.kickSide = m.kickSide, KickoffWait, -1
		return
	}
	if m.kicker >= 0 {
		if m.kickLeft -= dt; m.kickLeft <= 0 {
			m.kicker = -1 // 선축이 안 불면 풀어 준다
		}
	}

	m.played += dt
	if m.rules.TimeSec > 0 && !m.golden {
		m.playLeft -= dt
		if m.playLeft <= 0 && !m.inInjury && m.injuryShown() > 0 {
			m.inInjury = true // 정규 시간 끝 → 추가시간
			m.playLeft = m.injuryShown()
		}
		if m.playLeft <= 0 {
			m.playLeft = 0
			switch {
			case m.score[0] != m.score[1]:
				m.finish("time")
				return
			case m.nBlows[0] != m.nBlows[1]: // 동점이면 바람을 덜 분 쪽이 이긴다
				m.finish("blows")
				m.winner = 0
				if m.nBlows[1] < m.nBlows[0] {
					m.winner = 1
				}
				return
			case m.rules.GoldenGoal: // 그것도 같으면 토너먼트는 골든골
				m.golden = true
			default:
				m.finish("time") // 무승부
				return
			}
		}
	}
	m.physics(dt.Seconds())
}

func (m *Match) physics(dt float64) {
	// 알짜힘 = 작용 중인 바람들의 합 (+ 아래에서 마찰·공기저항)
	var net vec
	kept := m.blows[:0]
	for _, b := range m.blows {
		net = net.add(b.force)
		b.remain -= dt
		if b.remain > 0 {
			kept = append(kept, b)
		}
	}
	m.blows = kept
	m.vel = m.vel.add(net.scale(dt))

	// 마찰: 운동 반대 방향으로 일정 감속, 멈춘 공을 반대로 밀지는 않는다.
	if sp := m.vel.len(); sp > 0 {
		dec := Friction * dt
		if dec >= sp {
			m.vel = vec{}
		} else {
			m.vel = m.vel.scale((sp - dec) / sp)
		}
	}
	m.vel = m.vel.scale(math.Max(0, 1-Drag*dt))

	m.ball = m.ball.add(m.vel.scale(dt))
	m.collide()

	switch {
	case m.ball.X < -BallR: // 공이 왼쪽 골라인을 완전히 넘음 → side 1 득점
		m.goal(1)
	case m.ball.X > FieldW+BallR:
		m.goal(0)
	}
}

// collide 는 테두리·골대 안쪽 벽·골포스트와의 충돌을 처리한다.
func (m *Match) collide() {
	b, v := &m.ball, &m.vel
	inMouth := b.Y > GoalTop && b.Y < GoalBottom

	// 위/아래 테두리 — 골대 안(x<0 또는 x>W)에서는 골대 안쪽 벽이 막는다.
	top, bot := BallR, FieldH-BallR
	if b.X < 0 || b.X > FieldW {
		top, bot = GoalTop+BallR, GoalBottom-BallR
	}
	if b.Y < top {
		b.Y, v.Y = top, math.Abs(v.Y)*Restitution
	} else if b.Y > bot {
		b.Y, v.Y = bot, -math.Abs(v.Y)*Restitution
	}

	// 좌/우 테두리(골대 입구 제외)
	if !inMouth {
		if b.X < BallR && b.X > -BallR {
			b.X, v.X = BallR, math.Abs(v.X)*Restitution
		} else if b.X > FieldW-BallR && b.X < FieldW+BallR {
			b.X, v.X = FieldW-BallR, -math.Abs(v.X)*Restitution
		}
	}
	// 골대 뒷벽(골 판정 전에 튕겨 나가지 않도록 뒷벽은 골라인보다 충분히 뒤)
	if b.X < -GoalDepth {
		b.X, v.X = -GoalDepth, math.Abs(v.X)*Restitution
	} else if b.X > FieldW+GoalDepth {
		b.X, v.X = FieldW+GoalDepth, -math.Abs(v.X)*Restitution
	}

	// 골포스트 4개(점)와 원 충돌
	for _, p := range [4]vec{{0, GoalTop}, {0, GoalBottom}, {FieldW, GoalTop}, {FieldW, GoalBottom}} {
		dx, dy := b.X-p.X, b.Y-p.Y
		d := math.Hypot(dx, dy)
		if d >= BallR || d == 0 {
			continue
		}
		nx, ny := dx/d, dy/d
		b.X, b.Y = p.X+nx*BallR, p.Y+ny*BallR
		if vn := v.X*nx + v.Y*ny; vn < 0 {
			v.X -= (1 + Restitution) * vn * nx
			v.Y -= (1 + Restitution) * vn * ny
		}
	}
}

func (m *Match) goal(side int) {
	m.score[side]++
	m.scorer = side
	if m.golden {
		m.finish("golden")
		return
	}
	if m.rules.Goals > 0 && m.score[side] >= m.rules.Goals {
		m.finish("goals")
		return
	}
	m.kickoff(phGoal, GoalPause)
	m.kickSide = 1 - side // 골 먹은 쪽이 선축
}

func (m *Match) finish(reason string) {
	m.phase = phEnd
	m.reason = reason
	m.blows = nil
	switch {
	case m.score[0] > m.score[1]:
		m.winner = 0
	case m.score[1] > m.score[0]:
		m.winner = 1
	default:
		m.winner = -1
	}
}

// forfeit 은 side 가 나갔을 때 상대의 승리로 끝낸다.
func (m *Match) forfeit(side int) {
	if m.phase == phEnd {
		return
	}
	m.finish("forfeit")
	m.winner = 1 - side
}

// pauseForReconnect 는 한쪽 연결이 끊겼을 때 경기를 멈춘다.
func (m *Match) pauseForReconnect() {
	if m.phase == phEnd || m.phase == phPause {
		return
	}
	if m.phase == phGoal { // 골 세리머니 중이었으면 공을 중앙에 두고 멈춘다
		m.kickoff(phPause, 0)
	}
	if m.phase == phPlay { // 선축을 아직 안 했으면 돌아와서 이어서, 했으면 자유
		m.kickSide, m.kicker = m.kicker, -1
	}
	m.blows = nil
	m.phase = phPause
	m.phaseLeft = 0
}

// unpause 는 끊겼던 쪽이 돌아왔을 때 짧은 카운트다운 후 재개한다(공은 멈춘 자리 그대로).
func (m *Match) unpause() {
	if m.phase != phPause {
		return
	}
	m.vel = vec{}
	m.phase = phReady
	m.phaseLeft = ReadyDur
}

// injuryShown 은 추가시간(초 단위 반올림 — 틱 오차로 2.02초가 3초가 되지 않게).
func (m *Match) injuryShown() time.Duration {
	return m.injury.Round(time.Second)
}

// kickOffSide 는 화면에 보여줄 선축(-1 = 없음): 경기 중이면 기다리는 선축, 킥오프 전이면 다음 선축.
func (m *Match) kickOffSide() int {
	if m.phase == phPlay {
		return m.kicker
	}
	if m.phase == phEnd {
		return -1
	}
	return m.kickSide
}

func (m *Match) sideOf(p *Player) int {
	if m.players[0] == p {
		return 0
	}
	if m.players[1] == p {
		return 1
	}
	return -1
}
