package main

import (
	"fmt"
	"testing"
	"time"
)

func entrants(n int) []Entrant {
	out := []Entrant{}
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("학생%02d", i)
		out = append(out, Entrant{Key: playerKey("", name), Name: name})
	}
	return out
}

func TestLeagueEveryPairOnce(t *testing.T) {
	for _, n := range []int{2, 3, 4, 5, 8, 9} {
		c, err := newCompetition("리그", League, Rules{TimeSec: 60}, entrants(n), 1, t0)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[[2]int]int{}
		perRound := map[int]map[int]bool{}
		for _, f := range c.Fixtures {
			a, b := min(f.A, f.B), max(f.A, f.B)
			seen[[2]int{a, b}]++
			if perRound[f.Round] == nil {
				perRound[f.Round] = map[int]bool{}
			}
			if perRound[f.Round][f.A] || perRound[f.Round][f.B] {
				t.Fatalf("n=%d 한 라운드에 같은 학생이 두 번", n)
			}
			perRound[f.Round][f.A], perRound[f.Round][f.B] = true, true
		}
		if len(seen) != n*(n-1)/2 || len(c.Fixtures) != n*(n-1)/2 {
			t.Fatalf("n=%d 모든 쌍이 정확히 한 번: pairs=%d fixtures=%d", n, len(seen), len(c.Fixtures))
		}
	}
}

func TestLeagueGroupsRandomBalanced(t *testing.T) {
	c, err := newCompetition("반 리그", League, Rules{TimeSec: 60}, entrants(26), 4, t0)
	if err != nil {
		t.Fatal(err)
	}
	size := map[int]int{}
	for _, e := range c.Entrants {
		size[e.Group]++
	}
	if len(size) != 4 || size[0] != 7 || size[1] != 7 || size[2] != 6 || size[3] != 6 {
		t.Fatalf("26명 4조 → 7,7,6,6: %v", size)
	}
	pairs := map[[2]int]bool{}
	want := 0
	for _, n := range size {
		want += n * (n - 1) / 2
	}
	perRound := map[int]map[int]bool{}
	for _, f := range c.Fixtures {
		if c.Entrants[f.A].Group != c.Entrants[f.B].Group || f.Group != c.Entrants[f.A].Group {
			t.Fatal("다른 조끼리 경기하면 안 됨")
		}
		pairs[[2]int{min(f.A, f.B), max(f.A, f.B)}] = true
		if perRound[f.Round] == nil {
			perRound[f.Round] = map[int]bool{}
		}
		if perRound[f.Round][f.A] || perRound[f.Round][f.B] {
			t.Fatal("한 라운드에 같은 학생 두 번")
		}
		perRound[f.Round][f.A], perRound[f.Round][f.B] = true, true
	}
	if len(pairs) != want || len(c.Fixtures) != want || c.Rounds != 7 {
		t.Fatalf("조 안에서만 모두 한 번: pairs=%d want=%d rounds=%d", len(pairs), want, c.Rounds)
	}
	if c.groupName(1) != "B조" {
		t.Fatal(c.groupName(1))
	}
	// 조별 순위: 각 조에 1위가 있다
	firsts := map[int]int{}
	for _, s := range c.standings() {
		if s.Rank == 1 {
			firsts[s.Group]++
		}
	}
	if len(firsts) != 4 {
		t.Fatalf("조마다 순위: %v", firsts)
	}
	if _, err := newCompetition("x", League, Rules{TimeSec: 60}, entrants(5), 3, t0); err == nil {
		t.Fatal("조마다 2명 미만이면 거부")
	}
	// 무작위: 두 번 만들면 조 편성이 대체로 다르다
	d, _ := newCompetition("반 리그", League, Rules{TimeSec: 60}, entrants(26), 4, t0)
	same := true
	for _, e := range c.Entrants {
		for _, e2 := range d.Entrants {
			if e.Key == e2.Key && e.Group != e2.Group {
				same = false
			}
		}
	}
	if same {
		t.Fatal("조 편성이 무작위여야 함")
	}
}

func TestTournamentByesAndAdvance(t *testing.T) {
	c, err := newCompetition("토너먼트", Tournament, Rules{TimeSec: 60}, entrants(5), 1, t0)
	if err != nil {
		t.Fatal(err)
	}
	if c.Rounds != 3 || !c.Rules.GoldenGoal {
		t.Fatalf("5명 → 8강 대진 3라운드, 골든골: rounds=%d gg=%v", c.Rounds, c.Rules.GoldenGoal)
	}
	byes := 0
	for _, f := range c.Fixtures {
		if f.Round == 0 && f.Reason == "bye" {
			byes++
			if f.A < 0 {
				t.Fatal("부전승 경기에는 학생이 한 명 있어야 함")
			}
		}
	}
	if byes != 3 {
		t.Fatalf("부전승 3개: %d", byes)
	}
	// 1라운드 남은 경기를 모두 A 승으로 → 결승까지
	for c.currentRound() >= 0 {
		r := c.currentRound()
		for _, f := range c.Fixtures {
			if f.Round == r && f.Status == fxPending {
				if !f.ready() {
					t.Fatalf("%s 대진이 채워지지 않음: %+v", c.roundName(r), f)
				}
				c.decide(f, f.A, 1, 0, "goals")
			}
		}
	}
	if c.champion() < 0 {
		t.Fatal("우승자가 정해져야 함")
	}
	if c.roundName(0) != "8강" || c.roundName(2) != "결승" {
		t.Fatalf("라운드 이름: %s %s", c.roundName(0), c.roundName(2))
	}
}

func TestUndoBlockedAfterNextRoundStarts(t *testing.T) {
	c, _ := newCompetition("t", Tournament, Rules{Goals: 3}, entrants(4), 1, t0)
	f0, f1 := c.at(0, 0), c.at(0, 1)
	c.decide(f0, f0.A, 3, 1, "goals")
	c.decide(f1, f1.B, 0, 3, "goals")
	final := c.at(1, 0)
	if final.A != f0.A || final.B != f1.B {
		t.Fatal("승자가 결승에 올라가야 함")
	}
	final.Status = fxPlaying
	if c.undo(f0) == nil {
		t.Fatal("결승이 진행 중이면 4강 결과를 되돌릴 수 없다")
	}
	final.Status = fxPending
	if err := c.undo(f0); err != nil || final.A != slotTBD {
		t.Fatalf("되돌리면 결승 자리가 비어야 함: %v", err)
	}
}

func TestStandingsOrder(t *testing.T) {
	c, _ := newCompetition("리그", League, Rules{TimeSec: 60}, entrants(3), 1, t0)
	// 참가자 0: 2승, 1: 1무1패, 2: 1무1패(득실 차이)
	for _, f := range c.Fixtures {
		switch {
		case f.A == 0 || f.B == 0:
			other := f.A + f.B
			if f.A == 0 {
				c.decide(f, 0, 2, 0, "time")
			} else {
				c.decide(f, 0, 0, 2, "time")
			}
			_ = other
		default:
			c.decide(f, -1, 1, 1, "time")
		}
	}
	st := c.standings()
	if st[0].Entrant != 0 || st[0].Points != 6 || st[1].Points != 1 || st[1].Rank != 2 || st[2].Rank != 2 {
		t.Fatalf("순위: %+v", st)
	}
}

func TestGoldenGoal(t *testing.T) {
	m := playing(Rules{TimeSec: 30, GoldenGoal: true})
	run(m, 31*time.Second)
	if m.phase != phPlay || !m.golden {
		t.Fatalf("동점으로 시간 종료 → 골든골 연장: phase=%s golden=%v", m.phase, m.golden)
	}
	m.ball, m.vel = vec{3, 30}, vec{-30, 0}
	run(m, 500*time.Millisecond)
	if m.phase != phEnd || m.winner != 1 || m.reason != "golden" {
		t.Fatalf("골든골 → 종료: phase=%s winner=%d reason=%s", m.phase, m.winner, m.reason)
	}
}

func TestHubCompetitionFlow(t *testing.T) {
	f := newFakeHub()
	var saved *Competition
	f.saveComp = func(c *Competition) { saved = c }
	cs := []*client{}
	keys := []string{}
	for i := 0; i < 4; i++ {
		name := fmt.Sprintf("학생%d", i)
		cs = append(cs, f.join(fmt.Sprintf("t%d", i), name))
		keys = append(keys, playerKey("", name))
	}
	host := &client{hub: f.Hub, send: make(chan []byte, 1024), host: true}
	f.onRegister(host)
	f.say(host, map[string]any{"t": "compCreate", "name": "우리반 리그", "type": "league", "rules": Rules{Goals: 1}, "keys": keys})
	if f.comp == nil || saved != f.comp || len(f.comp.Fixtures) != 6 {
		t.Fatalf("리그 생성: %+v", f.comp)
	}
	f.say(host, map[string]any{"t": "compStartRound"})
	if len(f.matches) != 2 {
		t.Fatalf("1라운드 2경기가 자동으로 열려야 함: %d", len(f.matches))
	}
	for _, c := range cs {
		if last(c, "match") == nil {
			t.Fatal("모든 참가자에게 경기 시작 메시지")
		}
	}
	// 한 경기 끝내기
	var m *Match
	for _, x := range f.matches {
		m = x
		break
	}
	f.advance(ReadyDur + 100*time.Millisecond)
	m.ball, m.vel = vec{97, 30}, vec{30, 0}
	f.advance(500 * time.Millisecond)
	if m.fixture.Status != fxDone || m.fixture.Winner != m.fixture.A || m.fixture.ScoreA != 1 {
		t.Fatalf("경기 결과가 대진에 기록: %+v", m.fixture)
	}
	// 대회 경기는 다시 하기 없음
	p0 := m.players[0]
	f.say(p0.conn, map[string]any{"t": "rematch"})
	if m.rematch[0] {
		t.Fatal("대회 경기는 다시 하기 불가")
	}
	// 교사가 다른 경기 취소 → 대기로
	var other *Match
	for _, x := range f.matches {
		if x != m {
			other = x
		}
	}
	f.say(host, map[string]any{"t": "compReset", "id": other.fixture.ID})
	if other.fixture.Status != fxPending || f.matches[other.id] != nil {
		t.Fatal("취소하면 대진은 대기, 경기는 사라짐")
	}
	f.say(host, map[string]any{"t": "compDecide", "id": other.fixture.ID, "winner": "b"})
	// 1라운드 경기를 마친 학생이 결과 화면에 있어도 다음 라운드는 열린다
	f.say(host, map[string]any{"t": "compStartRound"})
	if p0.match == nil || p0.match == m || p0.match.fixture == nil || p0.match.fixture.Round != 1 {
		t.Fatalf("결과 화면의 학생도 다음 라운드 경기로: %+v", p0.match)
	}
	if other.fixture.Status != fxDone || other.fixture.Winner != other.fixture.B {
		t.Fatal("교사가 결과 지정")
	}
	f.advance(200 * time.Millisecond)
	if info := last(cs[0], "lobby")["comp"]; info == nil {
		t.Fatal("참가자 로비에 대회 안내")
	}
}
