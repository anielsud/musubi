// Command export writes the linking benchmark's inputs from an instance:
// every linked statement with the lines it cites, its claim and topic, and
// a stratified sample of statement pairs for reference labelling
// (docs/linking-benchmark.md). Output goes to a private directory.
//
//	go run -tags sqlite_fts5 ./tools/link-bench/export -db <musubi.db> -log <knowledge.log> -out <dir>
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/anielsud/musubi/internal/knowledge"
	"github.com/anielsud/musubi/internal/store"
)

type stmt struct {
	ID         int64    `json:"id"`
	Meeting    string   `json:"meeting"`
	Minute     int      `json:"minute"`
	Kind       string   `json:"kind"`
	Status     string   `json:"status,omitempty"`
	Who        []string `json:"who,omitempty"`
	Text       string   `json:"text"`
	Lines      []string `json:"lines"`
	Claim      int64    `json:"claim"`
	Topic      int64    `json:"topic"`
	TopicLabel string   `json:"topic_label"`
}

type pair struct {
	ID          string  `json:"pair"`
	Stratum     string  `json:"stratum"`
	Earlier     int64   `json:"earlier"`
	Later       int64   `json:"later"`
	Sim         float64 `json:"sim"`
	SameMeeting bool    `json:"same_meeting"`
	System      string  `json:"system"` // the linker's relation, "unrelated", or "not judged"
}

func main() {
	dbPath := flag.String("db", "", "instance database")
	logPath := flag.String("log", "", "knowledge -v log of the run (judged pairs)")
	out := flag.String("out", "", "output directory")
	model := flag.String("model", "nomic-embed-text", "statement embedding model")
	seed := flag.Int64("seed", 7, "sampling seed")
	titled := flag.Bool("titled", false, "name meetings by date, time and title (several meetings a day), not by date")
	flag.Parse()
	st, err := store.Open(*dbPath)
	check(err)
	defer st.Close()
	sts, err := st.AllStatements()
	check(err)
	rows, err := st.CardRows()
	check(err)
	claimOf, topicOf := map[int64]int64{}, map[int64]int64{}
	for _, r := range rows {
		claimOf[r.StatementID], topicOf[r.StatementID] = r.ClaimID, r.TopicID
	}
	infos, err := st.TopicInfos()
	check(err)
	items, err := st.AllItems()
	check(err)
	meeting := map[string]string{}
	start := map[string]int64{}
	for _, it := range items {
		meeting[it.ID] = it.OccurredAt.Format("2006-01-02")
		if *titled {
			meeting[it.ID] = it.OccurredAt.UTC().Format("2006-01-02 15:04") + " " + it.Title
		}
		start[it.ID] = it.OccurredAt.Unix()
	}
	lines := map[int64][]string{} // chunk id → lines
	for id := range meeting {
		cs, err := st.Chunks(id)
		check(err)
		for _, c := range cs {
			for _, l := range knowledge.SplitLines(c.Text) {
				t := l.Text
				if l.Speaker != "" {
					t = l.Speaker + ": " + t
				}
				lines[c.ID] = append(lines[c.ID], t)
			}
		}
	}
	by := map[int64]store.Statement{}
	var out1 []stmt
	for _, s := range sts {
		by[s.ID] = s
		x := stmt{ID: s.ID, Meeting: meeting[s.ItemID], Minute: int((s.SaidAt.Unix() - start[s.ItemID]) / 60), Kind: s.Kind,
			Status: s.Status, Who: s.Who, Text: s.Text, Claim: claimOf[s.ID], Topic: topicOf[s.ID]}
		if t := infos[x.Topic]; t != nil {
			x.TopicLabel = t.Label
		}
		for _, e := range s.Evidence {
			if ls := lines[e.ChunkID]; e.Line >= 1 && e.Line <= len(ls) {
				x.Lines = append(x.Lines, ls[e.Line-1])
			}
		}
		out1 = append(out1, x)
	}
	sort.Slice(out1, func(i, j int) bool {
		if out1[i].Meeting != out1[j].Meeting {
			return out1[i].Meeting < out1[j].Meeting
		}
		return out1[i].Minute < out1[j].Minute || out1[i].Minute == out1[j].Minute && out1[i].ID < out1[j].ID
	})
	check(os.MkdirAll(*out, 0o755))
	writeJSONL(filepath.Join(*out, "statements.jsonl"), out1)

	// The linker's verdicts: relations stored, and pairs judged unrelated
	// (from the run's log), keyed by the claims' first statements.
	firstOf := map[int64]int64{} // claim → first statement
	for _, s := range out1 {
		if _, ok := firstOf[s.Claim]; !ok {
			firstOf[s.Claim] = s.ID
		}
	}
	vecs, err := st.StatementVecs(*model)
	check(err)
	sim := func(a, b int64) float64 { return cos(vecs[a], vecs[b]) }
	order := map[int64]int{}
	for i, s := range out1 {
		order[s.ID] = i
	}
	type key struct{ a, b int64 }
	verdict := map[key]string{}
	rels, err := st.ClaimRelations()
	check(err)
	for _, r := range rels {
		a, b := firstOf[r.To], r.StatementID
		if a == 0 || b == 0 || a == b {
			continue
		}
		verdict[key{a, b}] = r.Rel
	}
	for _, s := range out1 { // joins: a later statement in the same claim as an earlier one
		if f := firstOf[s.Claim]; f != s.ID {
			verdict[key{f, s.ID}] = "same"
		}
	}
	if *logPath != "" {
		prefix := map[string]int64{}
		for _, s := range out1 {
			p := s.Text
			if len(p) > 50 {
				p = p[:50]
			}
			prefix[p] = s.ID
		}
		re := regexp.MustCompile(`judge "(.*)…" vs claim (\d+): ([a-z]+) ([0-9.]+)`)
		f, err := os.Open(*logPath)
		check(err)
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			m := re.FindStringSubmatch(sc.Text())
			if m == nil {
				continue
			}
			b := prefix[m[1]]
			var c int64
			fmt.Sscan(m[2], &c)
			a := firstOf[c]
			if a == 0 || b == 0 {
				continue
			}
			if _, ok := verdict[key{a, b}]; !ok && m[3] == "unrelated" {
				verdict[key{a, b}] = "unrelated"
			}
		}
		f.Close()
	}
	rng := rand.New(rand.NewSource(*seed))
	var pairs []pair
	add := func(stratum string, ks []key, n int) {
		rng.Shuffle(len(ks), func(i, j int) { ks[i], ks[j] = ks[j], ks[i] })
		if len(ks) > n {
			ks = ks[:n]
		}
		for _, k := range ks {
			a, b := k.a, k.b
			if order[a] > order[b] {
				a, b = b, a
			}
			sys, ok := verdict[k]
			if !ok {
				sys = "not judged"
			}
			pairs = append(pairs, pair{Stratum: stratum, Earlier: a, Later: b, Sim: math.Round(sim(a, b)*1000) / 1000,
				SameMeeting: by[a].ItemID == by[b].ItemID, System: sys})
		}
	}
	byRel := map[string][]key{}
	for k, v := range verdict {
		byRel[v] = append(byRel[v], k)
	}
	for _, v := range []string{"supersedes", "answers", "pending", "contradicts", "same"} {
		add("linker:"+v, byRel[v], 40)
	}
	add("linker:refines", byRel["refines"], 40)
	var hi, mid []key
	for _, k := range byRel["unrelated"] {
		switch s := sim(k.a, k.b); {
		case s >= 0.8:
			hi = append(hi, k)
		case s >= 0.65:
			mid = append(mid, k)
		}
	}
	add("unrelated:sim>=0.80", hi, 20)
	add("unrelated:0.65-0.80", mid, 15)
	// Never judged: across meetings, moderately similar, sharing a rare
	// capitalised word or a figure (where missed updates hide).
	df := map[string]int{}
	words := map[int64]map[string]bool{}
	for _, s := range out1 {
		words[s.ID] = map[string]bool{}
		for _, w := range regexp.MustCompile(`\b([A-Z][A-Za-z]{3,}|\d[\d.,]*)\b`).FindAllString(s.Text, -1) {
			if !words[s.ID][w] {
				words[s.ID][w] = true
				df[w]++
			}
		}
	}
	var never []key
	for i, a := range out1 {
		for _, b := range out1[i+1:] {
			if a.Meeting == b.Meeting {
				continue
			}
			k := key{a.ID, b.ID}
			if _, ok := verdict[k]; ok {
				continue
			}
			s := sim(a.ID, b.ID)
			if s < 0.55 || s >= 0.8 {
				continue
			}
			for w := range words[a.ID] {
				if words[b.ID][w] && df[w] <= 12 && df[w] >= 2 {
					never = append(never, k)
					break
				}
			}
		}
	}
	add("never judged:shared rare word", never, 30)
	sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].Stratum < pairs[j].Stratum })
	for i := range pairs {
		pairs[i].ID = fmt.Sprintf("P%03d", i+1)
	}
	writeJSONL(filepath.Join(*out, "pairs.jsonl"), pairs)
	counts := map[string]int{}
	for _, p := range pairs {
		counts[p.Stratum]++
	}
	fmt.Println(len(out1), "statements;", len(pairs), "pairs", counts)
}

func writeJSONL[T any](path string, xs []T) {
	f, err := os.Create(path)
	check(err)
	defer f.Close()
	w := bufio.NewWriter(f)
	for _, x := range xs {
		b, _ := json.Marshal(x)
		w.Write(b)
		w.WriteByte('\n')
	}
	check(w.Flush())
}

func cos(a, b []float32) float64 {
	var d, na, nb float64
	for i := range a {
		d += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return d / math.Sqrt(na*nb)
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

