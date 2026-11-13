package gui

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/anielsud/musubi/internal/recorder/control"
)

func TestVoiceSamples(t *testing.T) {
	a, f, ev, _ := testApp(t, "musubi")
	f.serve(t, a.eng.Cfg.RecorderControlSocket())
	if _, err := a.ProfileSamples("eres2net-en", "0a1b2c3d4e5f6071"); err != nil {
		t.Fatal(err)
	}
	if f.last().Op != control.OpProfileSamples {
		t.Errorf("asked %s", f.last().Op)
	}
	good := control.SampleArgs{Model: "eres2net-en", ID: "0a1b2c3d4e5f6071", Session: sessA, Start: 12.5, End: 80}
	if err := a.VoiceSample("not_them", good); err != nil {
		t.Fatal(err)
	}
	var got control.SampleArgs
	json.Unmarshal(f.last().Args, &got)
	if f.last().Op != control.OpExcludeSample || got != good || !ev.has("voices:changed") {
		t.Errorf("asked %s %+v", f.last().Op, got)
	}
	if err := a.VoiceSample("put_back", good); err != nil || f.last().Op != control.OpIncludeSample {
		t.Errorf("undo: %v, asked %s", err, f.last().Op)
	}
	if err := a.VoiceSample("junk", good); err != nil || f.last().Op != control.OpJunkSample {
		t.Errorf("junk: %v, asked %s", err, f.last().Op)
	}
	if _, err := a.NameReturnedSample(good, "  Bo Example "); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(f.last().Args, &got)
	if f.last().Op != control.OpNameReturnedSample || got.Name != "Bo Example" || got.Session != sessA {
		t.Errorf("named: asked %s %+v", f.last().Op, got)
	}
	if err := a.SkipReturnedSample(good); err != nil || f.last().Op != control.OpSkipReturnedSample {
		t.Errorf("skip: %v, asked %s", err, f.last().Op)
	}
	into := []SplitTarget{{Model: "eres2net-en", ID: "0a1b"}, {Model: "eres2net-en", ID: "0c1d", Label: " Bo "}}
	if _, err := a.SplitSpeakerByVoice(sessA, "Person 2 (Ana)", into, true); err != nil {
		t.Fatal(err)
	}
	var sa control.SplitArgs
	json.Unmarshal(f.last().Args, &sa)
	if f.last().Op != control.OpSplitLabelByVoice || !sa.Preview || sa.Label != "Person 2 (Ana)" || len(sa.Into) != 2 || sa.Into[1].Label != "Bo" {
		t.Errorf("split: asked %s %+v", f.last().Op, sa)
	}
	n := len(f.ops())
	if err := a.VoiceSample("move", good); err == nil {
		t.Error("an unknown sample action was accepted")
	}
	if _, err := a.NameReturnedSample(good, "two\nlines"); err == nil {
		t.Error("a two-line name was accepted")
	}
	if _, err := a.SplitSpeakerByVoice(sessA, "Ana", into[:1], true); err == nil {
		t.Error("a split into one voice was accepted")
	}
	if _, err := a.SplitSpeakerByVoice("../x", "Ana", into, true); err == nil {
		t.Error("a bad session was accepted")
	}
	if _, err := a.SplitSpeakerByVoice(sessA, "Ana", []SplitTarget{{Model: "m", ID: "../p"}, into[0]}, true); err == nil {
		t.Error("a bad profile was accepted")
	}
	if err := a.UndoSplit("nope"); err == nil {
		t.Error("a bad batch was accepted")
	}
	for _, bad := range []control.SampleArgs{
		{Model: "../m", ID: good.ID, Session: sessA, Start: 1, End: 2},
		{Model: good.Model, ID: "p/1", Session: sessA, Start: 1, End: 2},
		{Model: good.Model, ID: good.ID, Session: "../x", Start: 1, End: 2},
		{Model: good.Model, ID: good.ID, Session: sessA, Start: 2, End: 1},
		{Model: good.Model, ID: good.ID, Session: sessA, Start: math.NaN(), End: 1},
	} {
		if err := a.VoiceSample("not_them", bad); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
	if _, err := a.ProfileSamples("eres2net-en", "../p"); err == nil {
		t.Error("a bad profile id was asked about")
	}
	if len(f.ops()) != n {
		t.Errorf("refused asks reached the recorder: %v", f.ops()[n:])
	}
}

func TestVoiceSampleOnlyWhenMusubiRecords(t *testing.T) {
	a, f, _, _ := testApp(t, "tomoe")
	f.serve(t, a.eng.Cfg.RecorderControlSocket())
	if err := a.VoiceSample("junk", control.SampleArgs{Model: "eres2net-en", ID: "0a1b", Session: sessA, Start: 1, End: 2}); err == nil {
		t.Error("a sample edit was allowed with Tomoe of record")
	}
	if len(f.ops()) != 0 {
		t.Errorf("recorder asked %v", f.ops())
	}
}
