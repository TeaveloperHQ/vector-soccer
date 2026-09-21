package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 교사가 주관하는 대회(리그전·토너먼트). 한 번에 하나만 진행한다.
// 대진(Fixture)은 만들 때 전부 생성하고, 교사가 라운드를 시작하면 hub 가 경기를 연다.
// 진행 상황은 exe 옆 competitions/<id>.json 에 저장해 서버를 다시 켜도 이어서 한다.
// Competition 의 메서드는 Hub.run() 고루틴 안에서만 호출된다(락 없음).

type CompType string

const (
	League     CompType = "league"
	Tournament CompType = "tournament"
)

const (
	fxPending = "pending"
	fxPlaying = "playing"
	fxDone    = "done"

	slotBye = -1 // 토너먼트 부전승 자리
	slotTBD = -2 // 앞 경기 승자가 들어올 자리
)

type Entrant struct {
	Key   string `json:"key"` // 학번|이름 — 새 탭·새 기기로 다시 들어와도 같은 학생
	SID   string `json:"sid"`
	Name  string `json:"name"`
	Group int    `json:"group"` // 리그 조(0 = A조)
}

func (e Entrant) label() string {
	if e.SID != "" {
		return e.SID + " " + e.Name
	}
	return e.Name
}

type Fixture struct {
	ID     string `json:"id"`
	Round  int    `json:"round"`
	Slot   int    `json:"slot"`   // 라운드 안 순서(토너먼트: 다음 라운드 Slot/2 로 올라감)
	A      int    `json:"a"`      // 참가자 번호 또는 slotBye/slotTBD
	B      int    `json:"b"`      //
	Status string `json:"status"` // pending | playing | done
	ScoreA int    `json:"scoreA"`
	ScoreB int    `json:"scoreB"`
	Winner int    `json:"winner"` // 참가자 번호, -1 = 무승부/미정
	Group  int    `json:"group"`  // 리그 조
	Reason string `json:"reason"` // time | goals | golden | forfeit | bye | teacher
}

type Competition struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Type      CompType   `json:"type"`
	Rules     Rules      `json:"rules"`
	Entrants  []Entrant  `json:"entrants"`
	Fixtures  []*Fixture `json:"fixtures"`
	Rounds    int        `json:"rounds"`
	Groups    int        `json:"groups"` // 리그 조 수(토너먼트는 1)
	CreatedAt string     `json:"createdAt"`
	Closed    bool       `json:"closed"`
}

func playerKey(sid, name string) string { return sid + "|" + name }

func randInt(n int) int {
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0
	}
	return int(v.Int64())
}

func shuffle[T any](s []T) {
	for i := len(s) - 1; i > 0; i-- {
		j := randInt(i + 1)
		s[i], s[j] = s[j], s[i]
	}
}

// groups 는 리그 조 수. manual 이면 entrants 의 Group 을 교사가 정한 조로 그대로 쓰고,
// 아니면 참가자를 무작위로 고르게 나눈다. 토너먼트는 무시.
func newCompetition(name string, typ CompType, rules Rules, entrants []Entrant, groups int, manual bool, now time.Time) (*Competition, error) {
	if typ != League && typ != Tournament {
		return nil, errors.New("대회 방식이 올바르지 않습니다.")
	}
	if !rules.valid() {
		return nil, errors.New("경기 규칙이 올바르지 않습니다.")
	}
	seen := map[string]bool{}
	uniq := []Entrant{}
	for _, e := range entrants {
		if e.Name == "" || seen[e.Key] {
			continue
		}
		seen[e.Key] = true
		uniq = append(uniq, e)
	}
	if len(uniq) < 2 {
		return nil, errors.New("참가자가 2명 이상이어야 합니다.")
	}
	if len(uniq) > 64 {
		return nil, errors.New("참가자는 64명까지입니다.")
	}
	if typ == Tournament || groups < 1 {
		groups = 1
	}
	if groups > len(uniq)/2 {
		return nil, errors.New("조마다 2명 이상이 되도록 조 수를 줄이세요.")
	}
	name = cleanText(name, 40)
	if name == "" {
		name = "빨대 축구 대회"
	}
	shuffle(uniq)
	if manual && groups > 1 {
		size := make([]int, groups)
		for _, e := range uniq {
			if e.Group < 0 || e.Group >= groups {
				return nil, errors.New(e.label() + " 학생의 조가 정해지지 않았습니다.")
			}
			size[e.Group]++
		}
		for g, n := range size {
			if n < 2 {
				return nil, errors.New(string(rune('A'+g)) + "조에 2명 이상 넣어 주세요.")
			}
		}
	} else {
		for i := range uniq { // 섞은 순서대로 돌아가며 배정 → 조 인원 차이 최대 1명
			uniq[i].Group = i % groups
		}
	}
	c := &Competition{ID: newID(), Name: name, Type: typ, Rules: rules, Entrants: uniq, Groups: groups,
		CreatedAt: now.Format(time.RFC3339)}
	if typ == Tournament {
		c.Rules.GoldenGoal = true // 토너먼트는 무승부가 없다
		c.buildTournament()
	} else {
		c.Rules.GoldenGoal = false
		c.buildLeague()
	}
	return c, nil
}

// buildLeague 는 조마다 원형 방식(circle method)으로 모두가 한 번씩 만나는 라운드를 만든다.
// 모든 조의 같은 라운드는 동시에 진행한다. 홀수 인원이면 라운드마다 한 명이 쉰다(그 대진은 만들지 않는다).
func (c *Competition) buildLeague() {
	slots := map[int]int{} // 라운드 → 다음 Slot
	for g := 0; g < c.groupCount(); g++ {
		ids := []int{}
		for i, e := range c.Entrants {
			if e.Group == g {
				ids = append(ids, i)
			}
		}
		if len(ids)%2 == 1 {
			ids = append(ids, slotBye)
		}
		m := len(ids)
		if m-1 > c.Rounds {
			c.Rounds = m - 1
		}
		for r := 0; r < m-1; r++ {
			for i := 0; i < m/2; i++ {
				a, b := ids[i], ids[m-1-i]
				if a == slotBye || b == slotBye {
					continue
				}
				if r%2 == 1 { // 진영(파랑/빨강) 치우침 줄이기
					a, b = b, a
				}
				c.Fixtures = append(c.Fixtures, &Fixture{ID: newID(), Round: r, Slot: slots[r], A: a, B: b, Status: fxPending, Winner: -1, Group: g})
				slots[r]++
			}
			// 첫 번째는 고정하고 나머지를 한 칸씩 돌린다
			last := ids[m-1]
			copy(ids[2:], ids[1:m-1])
			ids[1] = last
		}
	}
}

func (c *Competition) groupCount() int { return max(1, c.Groups) }

// GroupName 은 "A조" 같은 조 이름(조가 하나면 빈 문자열).
func (c *Competition) groupName(g int) string {
	if c.Type != League || c.groupCount() <= 1 {
		return ""
	}
	return string(rune('A'+g)) + "조"
}

// buildTournament 는 싱글 엘리미네이션 대진표를 만든다. 2의 거듭제곱에 모자라는 만큼 부전승.
// 부전승은 1라운드 한 경기에 최대 하나만 들어가도록 배치한다.
func (c *Competition) buildTournament() {
	n := len(c.Entrants)
	size := 2
	for size < n {
		size *= 2
	}
	rounds := 0
	for s := size; s > 1; s /= 2 {
		rounds++
	}
	c.Rounds = rounds
	byes := size - n
	pairs := [][2]int{}
	next := 0
	for i := 0; i < size/2; i++ {
		if i < byes {
			pairs = append(pairs, [2]int{next, slotBye})
			next++
		} else {
			pairs = append(pairs, [2]int{next, next + 1})
			next += 2
		}
	}
	shuffle(pairs)
	for i, p := range pairs {
		c.Fixtures = append(c.Fixtures, &Fixture{ID: newID(), Round: 0, Slot: i, A: p[0], B: p[1], Status: fxPending, Winner: -1})
	}
	for r := 1; r < rounds; r++ {
		for i := 0; i < size>>(r+1); i++ {
			c.Fixtures = append(c.Fixtures, &Fixture{ID: newID(), Round: r, Slot: i, A: slotTBD, B: slotTBD, Status: fxPending, Winner: -1})
		}
	}
	for _, f := range c.Fixtures {
		if f.Round == 0 && f.B == slotBye {
			c.decide(f, f.A, 0, 0, "bye")
		}
	}
}

func (c *Competition) fixture(id string) *Fixture {
	for _, f := range c.Fixtures {
		if f.ID == id {
			return f
		}
	}
	return nil
}

func (c *Competition) at(round, slot int) *Fixture {
	for _, f := range c.Fixtures {
		if f.Round == round && f.Slot == slot {
			return f
		}
	}
	return nil
}

// decide 는 대진 결과를 확정하고, 토너먼트면 승자를 다음 라운드로 올린다.
func (c *Competition) decide(f *Fixture, winner, scoreA, scoreB int, reason string) {
	f.Status, f.Winner, f.ScoreA, f.ScoreB, f.Reason = fxDone, winner, scoreA, scoreB, reason
	if c.Type != Tournament || f.Round+1 >= c.Rounds {
		return
	}
	nf := c.at(f.Round+1, f.Slot/2)
	if nf == nil {
		return
	}
	if f.Slot%2 == 0 {
		nf.A = winner
	} else {
		nf.B = winner
	}
}

// undo 는 결과를 되돌린다. 토너먼트에서 승자가 이미 다음 경기를 시작했으면 되돌릴 수 없다.
func (c *Competition) undo(f *Fixture) error {
	if f.Status != fxDone || f.Reason == "bye" {
		return errors.New("되돌릴 결과가 없습니다.")
	}
	if c.Type == Tournament && f.Round+1 < c.Rounds {
		nf := c.at(f.Round+1, f.Slot/2)
		if nf != nil && nf.Status != fxPending {
			return errors.New("다음 라운드 경기가 이미 진행됐습니다.")
		}
		if nf != nil {
			if f.Slot%2 == 0 {
				nf.A = slotTBD
			} else {
				nf.B = slotTBD
			}
		}
	}
	f.Status, f.Winner, f.ScoreA, f.ScoreB, f.Reason = fxPending, -1, 0, 0, ""
	return nil
}

// ready 는 두 자리 모두 참가자가 정해진 대진인지.
func (f *Fixture) ready() bool { return f.A >= 0 && f.B >= 0 }

// currentRound 는 아직 끝나지 않은 대진이 있는 가장 앞 라운드(-1 = 모두 끝).
func (c *Competition) currentRound() int {
	for r := 0; r < c.Rounds; r++ {
		for _, f := range c.Fixtures {
			if f.Round == r && f.Status != fxDone {
				return r
			}
		}
	}
	return -1
}

func (c *Competition) finished() bool { return c.currentRound() == -1 }

func (c *Competition) roundName(r int) string {
	if c.Type == Tournament {
		left := 1 << (c.Rounds - r)
		switch left {
		case 2:
			return "결승"
		case 4:
			return "4강"
		default:
			if left >= 8 {
				return strconv.Itoa(left) + "강"
			}
		}
	}
	return strconv.Itoa(r+1) + "라운드"
}

// Standing 은 리그 순위표 한 줄.
type Standing struct {
	Entrant int `json:"entrant"`
	Group   int `json:"group"`
	Played  int `json:"played"`
	Win     int `json:"win"`
	Draw    int `json:"draw"`
	Lose    int `json:"lose"`
	GF      int `json:"gf"`
	GA      int `json:"ga"`
	Points  int `json:"points"`
	Rank    int `json:"rank"`
}

func (c *Competition) standings() []Standing {
	st := make([]Standing, len(c.Entrants))
	for i := range st {
		st[i].Entrant = i
		st[i].Group = c.Entrants[i].Group
	}
	for _, f := range c.Fixtures {
		if f.Status != fxDone || !f.ready() {
			continue
		}
		a, b := &st[f.A], &st[f.B]
		a.Played++
		b.Played++
		a.GF += f.ScoreA
		a.GA += f.ScoreB
		b.GF += f.ScoreB
		b.GA += f.ScoreA
		switch f.Winner {
		case f.A:
			a.Win++
			b.Lose++
		case f.B:
			b.Win++
			a.Lose++
		default:
			a.Draw++
			b.Draw++
		}
	}
	for i := range st {
		st[i].Points = st[i].Win*3 + st[i].Draw
	}
	less := func(x, y Standing) bool {
		if x.Points != y.Points {
			return x.Points > y.Points
		}
		if dx, dy := x.GF-x.GA, y.GF-y.GA; dx != dy {
			return dx > dy
		}
		return x.GF > y.GF
	}
	// 조별 순위: 조 순서로 묶고 조 안에서 정렬
	sort.SliceStable(st, func(i, j int) bool {
		if st[i].Group != st[j].Group {
			return st[i].Group < st[j].Group
		}
		return less(st[i], st[j])
	})
	pos := 0 // 조 안에서의 위치
	for i := range st {
		if i == 0 || st[i-1].Group != st[i].Group {
			pos = 0
		}
		pos++
		if pos > 1 && !less(st[i-1], st[i]) {
			st[i].Rank = st[i-1].Rank // 공동 순위(그 뒤는 건너뛴다: 1,1,3)
		} else {
			st[i].Rank = pos
		}
	}
	return st
}

// champion 은 토너먼트 결승 승자(없으면 -1).
func (c *Competition) champion() int {
	if c.Type != Tournament {
		return -1
	}
	if f := c.at(c.Rounds-1, 0); f != nil && f.Status == fxDone {
		return f.Winner
	}
	return -1
}

func (c *Competition) entrantIndex(key string) int {
	for i, e := range c.Entrants {
		if e.Key == key {
			return i
		}
	}
	return -1
}

// ── 저장 ────────────────────────────────────────────────────

func compsDir() string { return filepath.Join(exeDir(), "competitions") }

func saveCompetition(c *Competition) {
	if err := os.MkdirAll(compsDir(), 0755); err != nil {
		log.Printf("대회 폴더 만들기 실패: %v", err)
		return
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	p := filepath.Join(compsDir(), c.ID+".json")
	if err := os.WriteFile(p+".tmp", b, 0644); err != nil {
		log.Printf("대회 저장 실패: %v", err)
		return
	}
	if err := os.Rename(p+".tmp", p); err != nil {
		log.Printf("대회 저장 실패: %v", err)
	}
}

// loadOpenCompetition 은 닫히지 않은 가장 최근 대회를 불러온다. 서버가 꺼졌을 때 진행 중이던 경기는 대기로 되돌린다.
func loadOpenCompetition() *Competition {
	files, err := os.ReadDir(compsDir())
	if err != nil {
		return nil
	}
	var best *Competition
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(compsDir(), f.Name()))
		if err != nil {
			continue
		}
		var c Competition
		if json.Unmarshal(b, &c) != nil || c.Closed {
			continue
		}
		if best == nil || c.CreatedAt > best.CreatedAt {
			cc := c
			best = &cc
		}
	}
	if best != nil {
		for _, f := range best.Fixtures {
			if f.Status == fxPlaying {
				f.Status = fxPending
			}
		}
		log.Printf("진행 중이던 대회를 불러왔습니다: %s", best.Name)
	}
	return best
}
