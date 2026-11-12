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
	if err := a.VoiceSample(true, good); err != nil {
		t.Fatal(err)
	}
	var got control.SampleArgs
	json.Unmarshal(f.last().Args, &got)
	if f.last().Op != control.OpExcludeSample || got != good || !ev.has("voices:changed") {
		t.Errorf("asked %s %+v", f.last().Op, got)
	}
	if err := a.VoiceSample(false, good); err != nil || f.last().Op != control.OpIncludeSample {
		t.Errorf("undo: %v, asked %s", err, f.last().Op)
	}
	n := len(f.ops())
	for _, bad := range []control.SampleArgs{
		{Model: "../m", ID: good.ID, Session: sessA, Start: 1, End: 2},
		{Model: good.Model, ID: "p/1", Session: sessA, Start: 1, End: 2},
		{Model: good.Model, ID: good.ID, Session: "../x", Start: 1, End: 2},
		{Model: good.Model, ID: good.ID, Session: sessA, Start: 2, End: 1},
		{Model: good.Model, ID: good.ID, Session: sessA, Start: math.NaN(), End: 1},
	} {
		if err := a.VoiceSample(true, bad); err == nil {
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
	if err := a.VoiceSample(true, control.SampleArgs{Model: "eres2net-en", ID: "0a1b", Session: sessA, Start: 1, End: 2}); err == nil {
		t.Error("a sample edit was allowed with Tomoe of record")
	}
	if len(f.ops()) != 0 {
		t.Errorf("recorder asked %v", f.ops())
	}
}
