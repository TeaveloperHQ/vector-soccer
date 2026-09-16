package main

import (
	"math"
	"testing"
	"time"
)

var t0 = time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)

func playing(rules Rules) *Match {
	m := newMatch("m", &Player{name: "A"}, &Player{name: "B"}, rules, t0, -1) // 선축 없이
	run(m, ReadyDur)
	return m
}

func run(m *Match, d time.Duration) {
	for el := time.Duration(0); el < d; el += tickPeriod {
		m.step(tickPeriod)
	}
}

func TestReadyThenPlay(t *testing.T) {
	m := newMatch("m", &Player{}, &Player{}, Rules{TimeSec: 60}, t0, -1)
	if m.applyBlow(0, vec{1, 0}) != blowIgnored {
		t.Fatal("카운트다운 중에는 불 수 없고 벌칙도 없다")
	}
	run(m, ReadyDur)
	if m.phase != phPlay {
		t.Fatalf("phase=%s, want play", m.phase)
	}
}

func TestBlowMovesBallAndCooldown(t *testing.T) {
	m := playing(Rules{TimeSec: 120})
	if m.applyBlow(0, vec{0.5, 0}) != blowOK {
		t.Fatal("첫 바람은 허용")
	}
	run(m, 100*time.Millisecond)
	if m.applyBlow(0, vec{0.01, 0}) != blowIgnored || m.nMisses[0] != 0 {
		t.Fatal("쉬는 중이라도 실수 터치는 벌칙 없음")
	}
	if m.applyBlow(0, vec{1, 0}) != blowMiss || len(m.blows) != 1 {
		t.Fatal("쿨타임 중 바람은 헛방질")
	}
	run(m, 500*time.Millisecond)
	if m.ball.X <= FieldW/2 || m.vel.X <= 0 {
		t.Fatalf("공이 +x 로 움직여야 함: ball=%v vel=%v", m.ball, m.vel)
	}
	run(m, Cooldown)
	if m.applyBlow(0, vec{-1, 0}) != blowMiss {
		t.Fatal("헛방질 벌칙 1초 때문에 3초 뒤에는 아직 못 분다")
	}
	run(m, 2*MissPenalty)
	if m.applyBlow(0, vec{-1, 0}) != blowOK {
		t.Fatal("벌칙이 끝나면 다시 불 수 있어야 한다")
	}
}

func TestKickoff(t *testing.T) {
	m := newMatch("m", &Player{}, &Player{}, Rules{TimeSec: 120}, t0, 1)
	run(m, ReadyDur)
	if m.kickOffSide() != 1 || m.applyBlow(0, vec{1, 0}) != blowIgnored || m.nMisses[0] != 0 {
		t.Fatal("선축(1) 전에는 상대(0)가 못 불고 벌칙도 없다")
	}
	if m.applyBlow(1, vec{-1, 0}) != blowOK || m.kickOffSide() != -1 {
		t.Fatal("선축이 불면 자유")
	}
	if m.applyBlow(0, vec{0, 1}) != blowOK {
		t.Fatal("선축 뒤에는 상대도 불 수 있다")
	}
	// 골: side 1 득점 → side 0 이 다음 선축
	m.ball, m.vel = vec{97, 30}, vec{40, 0}
	m.blows = nil
	run(m, 300*time.Millisecond)
	if m.score[0] != 1 && m.score[1] != 1 {
		t.Fatalf("골이 들어가야 함: %v", m.ball)
	}
	scorer := m.scorer
	if m.kickOffSide() != 1-scorer {
		t.Fatalf("골 먹은 쪽이 선축: scorer=%d ko=%d", scorer, m.kickOffSide())
	}
	run(m, GoalPause)
	// 선축이 안 불면 5초 뒤 풀린다
	run(m, KickoffWait+100*time.Millisecond)
	if m.kickOffSide() != -1 || m.applyBlow(scorer, vec{1, 0}) != blowOK {
		t.Fatal("선축 대기 시간이 지나면 둘 다 불 수 있다")
	}
}

func TestKickoffSurvivesPause(t *testing.T) {
	m := newMatch("m", &Player{}, &Player{}, Rules{TimeSec: 120}, t0, 0)
	run(m, ReadyDur)
	m.pauseForReconnect()
	m.unpause()
	run(m, ReadyDur)
	if m.kickOffSide() != 0 {
		t.Fatalf("선축 전에 끊겼다 돌아오면 선축 유지: %d", m.kickOffSide())
	}
	m.applyBlow(0, vec{1, 0})
	m.pauseForReconnect()
	m.unpause()
	run(m, ReadyDur)
	if m.kickOffSide() != -1 {
		t.Fatal("선축 뒤에 끊겼다 돌아오면 자유")
	}
}

func TestMissPenaltyCapped(t *testing.T) {
	m := playing(Rules{TimeSec: 120})
	m.applyBlow(1, vec{0, 1})
	for i := 0; i < 10; i++ {
		m.applyBlow(1, vec{0, 1})
	}
	if left := m.cooldownLeft(1); left != MaxCooldown || m.nMisses[1] != 10 || m.cdTotal[1] != MaxCooldown {
		t.Fatalf("헛방질이 쌓여도 남은 쿨타임은 최대 %v: left=%v misses=%d total=%v", MaxCooldown, left, m.nMisses[1], m.cdTotal[1])
	}
	if m.cooldownLeft(0) != 0 {
		t.Fatal("상대 쿨타임에는 영향 없음")
	}
}

func TestLongerDragIsStronger(t *testing.T) {
	speed := func(l float64) float64 {
		m := playing(Rules{TimeSec: 120})
		m.applyBlow(0, vec{0, l})
		run(m, time.Duration(BlowDur*float64(time.Second)))
		return m.vel.len()
	}
	if s1, s2 := speed(0.3), speed(0.9); !(s2 > s1*2.5) {
		t.Fatalf("세기는 드래그 길이에 비례해야 함: 0.3→%.2f 0.9→%.2f", s1, s2)
	}
	if speed(5) > speed(1)+1e-9 {
		t.Fatal("길이 1 을 넘는 입력은 1 로 잘려야 한다")
	}
	if speed(0.01) != 0 {
		t.Fatal("너무 짧은 드래그는 무시")
	}
}

func TestOpposingBlowsCancel(t *testing.T) {
	m := playing(Rules{TimeSec: 120})
	m.applyBlow(0, vec{1, 0})
	m.applyBlow(1, vec{-1, 0})
	run(m, time.Second)
	if m.vel.len() > 1e-9 || math.Abs(m.ball.X-FieldW/2) > 1e-9 {
		t.Fatalf("같은 크기 반대 방향 → 알짜힘 0: ball=%v vel=%v", m.ball, m.vel)
	}
}

func TestPerpendicularBlowsAdd(t *testing.T) {
	m := playing(Rules{TimeSec: 120})
	m.applyBlow(0, vec{1, 0})
	m.applyBlow(1, vec{0, 1})
	run(m, 100*time.Millisecond)
	if ang := math.Atan2(m.vel.Y, m.vel.X); math.Abs(ang-math.Pi/4) > 1e-6 {
		t.Fatalf("수직인 두 힘의 알짜힘은 45° 방향: %.3f rad", ang)
	}
}

func TestFrictionStopsBall(t *testing.T) {
	m := playing(Rules{TimeSec: 120})
	m.vel = vec{10, 0}
	run(m, 5*time.Second)
	if m.vel.len() != 0 {
		t.Fatalf("마찰로 결국 멈춰야 함: %v", m.vel)
	}
}

func TestGoalScoresAndKickoff(t *testing.T) {
	m := playing(Rules{TimeSec: 120})
	m.ball = vec{3, 30}
	m.vel = vec{-30, 0}
	run(m, 500*time.Millisecond)
	if m.score != [2]int{0, 1} || m.phase != phGoal {
		t.Fatalf("왼쪽 골대에 들어가면 side1 득점: score=%v phase=%s", m.score, m.phase)
	}
	if m.ball != (vec{FieldW / 2, FieldH / 2}) {
		t.Fatalf("골 후 공은 중앙: %v", m.ball)
	}
	run(m, GoalPause)
	if m.phase != phPlay {
		t.Fatalf("세리머니 후 재개: %s", m.phase)
	}
}

func TestWallBounceOutsideGoalMouth(t *testing.T) {
	m := playing(Rules{TimeSec: 120})
	m.ball = vec{3, 10} // 골대 입구 밖
	m.vel = vec{-30, 0}
	run(m, 500*time.Millisecond)
	if m.score != [2]int{} || m.vel.X <= 0 {
		t.Fatalf("입구 밖 벽에서는 튕겨야 함: score=%v vel=%v", m.score, m.vel)
	}
}

func TestGoalTargetEndsMatch(t *testing.T) {
	m := playing(Rules{Goals: 1})
	m.ball = vec{97, 30}
	m.vel = vec{30, 0}
	run(m, 500*time.Millisecond)
	if m.phase != phEnd || m.winner != 0 || m.reason != "goals" {
		t.Fatalf("목표 골 달성 → 종료: phase=%s winner=%d reason=%s", m.phase, m.winner, m.reason)
	}
}

func TestTimeUpDraw(t *testing.T) {
	m := playing(Rules{TimeSec: 30})
	run(m, 31*time.Second)
	if m.phase != phEnd || m.winner != -1 || m.reason != "time" {
		t.Fatalf("시간 종료 동점 → 무승부: phase=%s winner=%d", m.phase, m.winner)
	}
}

func TestTimeUpTieFewerBlowsWins(t *testing.T) {
	m := playing(Rules{TimeSec: 30, GoldenGoal: true})
	m.applyBlow(0, vec{0, 0.2})
	run(m, 4*time.Second)
	m.applyBlow(0, vec{0, -0.2})
	m.applyBlow(1, vec{0, 0.2})
	run(m, 27*time.Second)
	if m.phase != phEnd || m.reason != "blows" || m.winner != 1 {
		t.Fatalf("동점이면 바람 덜 분 쪽(1) 승 — 골든골보다 먼저: phase=%s reason=%s winner=%d", m.phase, m.reason, m.winner)
	}
}

func TestInjuryTime(t *testing.T) {
	m := playing(Rules{TimeSec: 30})
	m.ball, m.vel = vec{97, 30}, vec{40, 0}
	run(m, 300*time.Millisecond) // 골
	if m.phase != phGoal {
		t.Fatalf("골: %s", m.phase)
	}
	before := m.playLeft
	run(m, GoalPause)
	if m.playLeft >= before-GoalPause+100*time.Millisecond || m.injuryShown() != GoalPause {
		t.Fatalf("골 세리머니 동안 시계는 흐르고 추가시간이 쌓인다: left=%v injury=%v", m.playLeft, m.injuryShown())
	}
	run(m, m.playLeft+50*time.Millisecond)
	if m.phase != phPlay || !m.inInjury || m.playLeft <= GoalPause-200*time.Millisecond {
		t.Fatalf("정규 시간 뒤 추가시간 %v 진행: phase=%s inj=%v left=%v", GoalPause, m.phase, m.inInjury, m.playLeft)
	}
	run(m, GoalPause)
	if m.phase != phEnd || m.winner != 0 {
		t.Fatalf("추가시간이 끝나면 종료: phase=%s", m.phase)
	}
}

func TestNoInjuryWithoutStoppage(t *testing.T) {
	m := playing(Rules{TimeSec: 30})
	run(m, 30*time.Second+50*time.Millisecond)
	if m.phase != phEnd || m.inInjury {
		t.Fatal("멈춘 적 없으면 추가시간 없이 끝")
	}
}

func TestPauseDoesNotCountTime(t *testing.T) {
	m := playing(Rules{TimeSec: 60})
	m.pauseForReconnect()
	run(m, 10*time.Second)
	if m.playLeft != 60*time.Second {
		t.Fatalf("재접속 대기 중에는 시간이 흐르지 않음: %v", m.playLeft)
	}
	m.unpause()
	run(m, ReadyDur)
	if m.phase != phPlay {
		t.Fatalf("복귀 후 재개: %s", m.phase)
	}
}

func TestBallStaysInBounds(t *testing.T) {
	m := playing(Rules{TimeSec: 600})
	dirs := []vec{{1, 1}, {-1, 0.3}, {0.2, -1}, {-1, -1}, {1, -0.1}}
	for i := 0; i < 200; i++ {
		m.resetCooldowns()
		m.applyBlow(i%2, dirs[i%len(dirs)])
		run(m, 700*time.Millisecond)
		if m.ball.Y < BallR-1e-9 || m.ball.Y > FieldH-BallR+1e-9 || m.ball.X < -GoalDepth || m.ball.X > FieldW+GoalDepth {
			t.Fatalf("공이 경기장을 벗어남: %v", m.ball)
		}
		if m.phase == phGoal {
			run(m, GoalPause)
		}
	}
}

func TestRulesValid(t *testing.T) {
	for _, c := range []struct {
		r  Rules
		ok bool
	}{{Rules{TimeSec: 180}, true}, {Rules{Goals: 5}, true}, {Rules{TimeSec: 120, Goals: 3}, true}, {Rules{}, false}, {Rules{TimeSec: 5}, false}, {Rules{Goals: 99}, false}} {
		if c.r.valid() != c.ok {
			t.Errorf("%+v valid=%v", c.r, !c.ok)
		}
	}
}
