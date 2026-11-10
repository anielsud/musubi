package session

import (
	"fmt"
	"os"
	"regexp"
	"slices"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"

	"github.com/anielsud/musubi/internal/recorder/sigfix"
	"github.com/anielsud/musubi/internal/recorder/speaker"
)

// pcmSampleRate is the rate every decode in this package produces and the
// diarization models expect.
const pcmSampleRate = 16000

// DiarizeConfig configures a diarization run over a session's audio.
type DiarizeConfig struct {
	SegmentationModelPath string // pyannote segmentation model
	EmbeddingModelPath    string // speaker embedding model
	// NumSpeakers fixes how many speakers to find; 0 lets clustering
	// decide, using Threshold (0 = 1.1).
	NumSpeakers int
	Threshold   float32
	// MergeThreshold, when above 0, joins clusters whose voices are at
	// least this cosine-similar afterwards (see MergeSimilarSpeakers).
	MergeThreshold float64
	UseGPU         bool // run the models on CUDA
	NumThreads     int  // CPU threads per model (0 = 4)
	// MinDurationOn and MinDurationOff are the shortest speech turn and
	// the shortest pause diarization keeps, in seconds (0 = 0.3 and 0.5).
	MinDurationOn  float64
	MinDurationOff float64
	// SplitOnSpeakerChange splits a transcript line wherever diarization
	// changes speaker mid-line (using the line's word timings), instead of
	// giving the whole line one speaker. See SplitByDiarization.
	SplitOnSpeakerChange bool
	Verbose              bool
}

// DiarizeSegment is a stretch of audio diarization gave to one speaker
// cluster. Times are in seconds.
type DiarizeSegment struct {
	Start   float64
	End     float64
	Speaker int
}

// Diarize finds who spoke when in 16 kHz mono samples. It returns the
// speaker turns and a label for each cluster, "Person 1", "Person 2", ...
// numbered in order of first appearance.
func Diarize(samples []float32, cfg DiarizeConfig) ([]DiarizeSegment, map[int]string, error) {
	provider, numThreads := "cpu", 4
	if cfg.NumThreads > 0 {
		numThreads = cfg.NumThreads
	}
	if cfg.UseGPU {
		// One thread is enough to feed the GPU.
		provider, numThreads = "cuda", 1
	}

	var sc sherpa.OfflineSpeakerDiarizationConfig
	sc.Segmentation.Pyannote.Model = cfg.SegmentationModelPath
	sc.Segmentation.NumThreads, sc.Segmentation.Provider = numThreads, provider
	sc.Embedding.Model = cfg.EmbeddingModelPath
	sc.Embedding.NumThreads, sc.Embedding.Provider = numThreads, provider
	switch {
	case cfg.NumSpeakers > 0:
		sc.Clustering.NumClusters = cfg.NumSpeakers
	case cfg.Threshold <= 0:
		sc.Clustering.Threshold = 1.1
	default:
		sc.Clustering.Threshold = cfg.Threshold
	}
	sc.MinDurationOn = 0.3
	if cfg.MinDurationOn > 0 {
		sc.MinDurationOn = float32(cfg.MinDurationOn)
	}
	sc.MinDurationOff = 0.5
	if cfg.MinDurationOff > 0 {
		sc.MinDurationOff = float32(cfg.MinDurationOff)
	}

	sd := sherpa.NewOfflineSpeakerDiarization(&sc)
	if sd == nil {
		return nil, nil, fmt.Errorf("could not load the diarization models (check their paths)")
	}
	// Creating the engine lets ONNX Runtime install its own SIGSEGV
	// handler, which lacks SA_ONSTACK and would break Go's signal
	// handling; put the flag back right away.
	sigfix.AfterSherpa()
	defer sherpa.DeleteOfflineSpeakerDiarization(sd)

	if rate := sd.SampleRate(); rate != pcmSampleRate {
		return nil, nil, fmt.Errorf("diarization models want %d Hz audio, not %d Hz", rate, pcmSampleRate)
	}
	if cfg.Verbose {
		fmt.Printf("Diarizing %.1fs of audio...\n", float64(len(samples))/float64(pcmSampleRate))
	}
	found := sd.Process(samples)
	if cfg.Verbose {
		fmt.Printf("Diarization: %d turns\n", len(found))
		for _, t := range found {
			fmt.Printf("  %.1fs - %.1fs  speaker_%d\n", t.Start, t.End, t.Speaker)
		}
	}

	segs := make([]DiarizeSegment, 0, len(found))
	for _, t := range found {
		segs = append(segs, DiarizeSegment{Start: float64(t.Start), End: float64(t.End), Speaker: t.Speaker})
	}
	labels := personLabels(segs)
	if cfg.MergeThreshold > 0 && len(labels) > 1 {
		segs, labels = MergeSimilarSpeakers(segs, labels, samples,
			cfg.EmbeddingModelPath, cfg.MergeThreshold, cfg.Verbose)
	}
	return segs, labels, nil
}

// personLabels names each cluster in segs "Person N", N counting up in
// the order the clusters first speak.
func personLabels(segs []DiarizeSegment) map[int]string {
	labels := make(map[int]string)
	for _, s := range segs {
		if _, ok := labels[s.Speaker]; !ok {
			labels[s.Speaker] = fmt.Sprintf("Person %d", len(labels)+1)
		}
	}
	return labels
}

// MergeSimilarSpeakers undoes over-splitting, where diarization gives one
// voice two or more clusters. It embeds each cluster's speech (its first
// 30 seconds) and folds any cluster at least mergeThreshold cosine-similar
// to a lower-numbered one into it, then renumbers the labels. segments is
// updated in place. If the embedder can't load, or fewer than two clusters
// can be embedded, the input comes back unchanged.
func MergeSimilarSpeakers(segments []DiarizeSegment, speakerMap map[int]string,
	samples []float32, embeddingModelPath string, mergeThreshold float64, verbose bool) ([]DiarizeSegment, map[int]string) {
	if len(speakerMap) < 2 {
		return segments, speakerMap
	}
	emb, err := speaker.NewEmbedder(embeddingModelPath)
	if err != nil {
		if verbose {
			fmt.Printf("  merge: embedder unavailable: %v\n", err)
		}
		return segments, speakerMap
	}
	defer emb.Close()

	voices := clusterVoices(segments, samples, emb, verbose)
	if len(voices) < 2 {
		return segments, speakerMap
	}

	// Greedy, so visit clusters in ID order for a result that doesn't
	// depend on map iteration. A cluster already folded into another is
	// neither a target nor folded again.
	ids := make([]int, 0, len(voices))
	for id := range voices {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	into := make(map[int]int)
	for a, keep := range ids {
		if _, gone := into[keep]; gone {
			continue
		}
		for _, other := range ids[a+1:] {
			if _, gone := into[other]; gone {
				continue
			}
			sim := speaker.CosineSimilarity(voices[keep], voices[other])
			if verbose {
				fmt.Printf("  merge: %d ~ %d similarity %.3f\n", keep, other, sim)
			}
			if sim >= mergeThreshold {
				into[other] = keep
				if verbose {
					fmt.Printf("  merge: %d folded into %d\n", other, keep)
				}
			}
		}
	}
	if len(into) == 0 {
		return segments, speakerMap
	}

	for i := range segments {
		id := segments[i].Speaker
		for {
			next, ok := into[id]
			if !ok {
				break
			}
			id = next
		}
		segments[i].Speaker = id
	}
	merged := personLabels(segments)
	if verbose {
		fmt.Printf("  merge: %d speakers left of %d\n", len(merged), len(speakerMap))
	}
	return segments, merged
}

// clusterVoices returns one speaker embedding per cluster, from that
// cluster's turns joined in order and cut to 30 seconds. A cluster whose
// embedding fails is left out.
func clusterVoices(segments []DiarizeSegment, samples []float32, emb *speaker.Embedder, verbose bool) map[int][]float32 {
	audio := make(map[int][]float32)
	for _, s := range segments {
		from := max(int(s.Start*pcmSampleRate), 0)
		to := min(int(s.End*pcmSampleRate), len(samples))
		if from < to {
			audio[s.Speaker] = append(audio[s.Speaker], samples[from:to]...)
		}
	}
	const limit = 30 * pcmSampleRate
	voices := make(map[int][]float32, len(audio))
	for id, a := range audio {
		v, err := emb.Extract(a[:min(len(a), limit)])
		if err != nil {
			if verbose {
				fmt.Printf("  merge: no embedding for speaker %d: %v\n", id, err)
			}
			continue
		}
		voices[id] = v
	}
	return voices
}

// ReidentifyByDiarization relabels the remote speakers of sess by running
// diarization over its monitor audio, returning how many lines it
// relabeled (or, with SplitOnSpeakerChange, the count SplitByDiarization
// reports). Sessions with nothing to diarize return 0 before any audio is
// decoded or model loaded.
func ReidentifyByDiarization(sess *Session, cfg DiarizeConfig) (int, error) {
	if sess.AudioPath == "" {
		return 0, fmt.Errorf("session has no audio")
	}
	if _, err := os.Stat(sess.AudioPath); err != nil {
		return 0, fmt.Errorf("session audio missing: %s", sess.AudioPath)
	}
	if !slices.ContainsFunc(sess.Segments, diarizable) {
		return 0, nil
	}
	samples, err := extractMonitorAudio(sess.AudioPath, sess)
	if err != nil {
		return 0, fmt.Errorf("reading monitor audio: %w", err)
	}
	if samples == nil {
		return 0, nil
	}
	if cfg.Verbose {
		fmt.Printf("Monitor audio for diarization: %d samples (%.1fs)\n",
			len(samples), float64(len(samples))/float64(pcmSampleRate))
	}
	diarSegments, speakerMap, err := Diarize(samples, cfg)
	if err != nil {
		return 0, err
	}
	if len(diarSegments) == 0 {
		return 0, nil
	}

	if cfg.SplitOnSpeakerChange {
		var count int
		sess.Segments, count = SplitByDiarization(sess.Segments, diarSegments, speakerMap)
		return count, nil
	}
	return relabelByDiarization(sess.Segments, diarSegments, speakerMap, cfg.Verbose), nil
}

// RelabelByDiarization gives each diarizable segment the label of the
// diarization speaker it overlaps most (see relabelByDiarization), for
// callers that already have diarization output, such as `tomoe eval`.
func RelabelByDiarization(segs []Segment, diar []DiarizeSegment, speakerMap map[int]string) int {
	return relabelByDiarization(segs, diar, speakerMap, false)
}

// DiarizationLabels returns, for each segment, the diarization speaker it
// overlaps most (-1 if none, or not diarizable), and each diarization
// speaker's label with the video-hint name its segments' live labels carry
// most, if any (see relabelByDiarization).
func DiarizationLabels(segs []Segment, diar []DiarizeSegment, speakerMap map[int]string) (assigned []int, labels map[int]string) {
	return diarizationLabels(segs, diar, speakerMap)
}

// diarizable reports whether a transcript segment's speaker label should
// come from diarization: not the mic ("You"), and not "System Audio",
// which live transcription uses for a whole-system audio tap (macOS's
// "Everything" source) precisely because per-speaker clustering isn't
// meaningful there. Judged by the live label, since Speaker may have been
// relabeled already.
func diarizable(seg Segment) bool {
	live := seg.LiveLabel()
	return seg.Source != "mic" && live != "You" && live != "System Audio"
}

// Diarizable reports whether seg's speaker comes from diarization (see
// diarizable).
func Diarizable(seg Segment) bool { return diarizable(seg) }

// HintName is the video-hint name in a live label "Person N (Name)", or "".
func HintName(label string) string {
	if m := hintLabel.FindStringSubmatch(label); m != nil {
		return m[1]
	}
	return ""
}

// hintLabel is the shape speaker.Tracker gives a live label once a video
// hint has named the speaker; the group captures the name.
var hintLabel = regexp.MustCompile(`^Person \d+ \((.+)\)$`)

// relabelByDiarization gives each diarizable segment its diarization
// speaker's label (see diarizationLabels) and returns how many it
// changed.
//
// Diarization clusters are anonymous, but a live label may already carry
// a name from the meeting window. That name is kept for the cluster, as
// "Person K (Name)", so relabeling doesn't throw away who someone is.
func relabelByDiarization(segs []Segment, diar []DiarizeSegment, speakerMap map[int]string, verbose bool) int {
	assigned, labels := diarizationLabels(segs, diar, speakerMap)
	count := 0
	for i := range segs {
		label, ok := labels[assigned[i]]
		if assigned[i] < 0 || !ok {
			continue
		}
		segs[i].Speaker = label
		count++
		if verbose {
			fmt.Printf("  transcript seg %d [%.1fs-%.1fs] → %s\n", i, segs[i].StartTime, segs[i].EndTime, label)
		}
	}
	return count
}

// diarizationLabels returns, for each segment, the diarization speaker it
// overlaps most (-1 if none, or not diarizable), and each diarization
// speaker's label: its "Person N", plus the video-hint name its segments
// were live-labeled with for the most time, if any.
func diarizationLabels(segs []Segment, diar []DiarizeSegment, speakerMap map[int]string) (assigned []int, labels map[int]string) {
	assigned = make([]int, len(segs))
	// hinted[cluster][name] is how long that cluster's lines were live
	// labeled with that name.
	hinted := make(map[int]map[string]float64)
	for i, seg := range segs {
		assigned[i] = -1
		if !diarizable(seg) {
			continue
		}
		// The first turn with the largest positive overlap wins.
		most := 0.0
		for _, d := range diar {
			if o := min(seg.EndTime, d.End) - max(seg.StartTime, d.Start); o > most {
				most, assigned[i] = o, d.Speaker
			}
		}
		if assigned[i] < 0 {
			continue
		}
		if m := hintLabel.FindStringSubmatch(seg.LiveLabel()); m != nil {
			names := hinted[assigned[i]]
			if names == nil {
				names = make(map[string]float64)
				hinted[assigned[i]] = names
			}
			names[m[1]] += seg.EndTime - seg.StartTime
		}
	}

	labels = make(map[int]string, len(speakerMap))
	for id, label := range speakerMap {
		if name := mostHeardName(hinted[id]); name != "" {
			label += " (" + name + ")"
		}
		labels[id] = label
	}
	return assigned, labels
}

// mostHeardName picks the name with the most speaking time. Ties go to
// the longer name, since the window often shows a name cut short and the
// full read is the better one, then to the alphabetically first, so the
// pick never depends on map order.
func mostHeardName(spoken map[string]float64) string {
	pick, pickTime := "", -1.0
	for name, t := range spoken {
		better := t > pickTime
		if t == pickTime {
			better = len(name) > len(pick) || (len(name) == len(pick) && name < pick)
		}
		if better {
			pick, pickTime = name, t
		}
	}
	return pick
}

// extractMonitorAudio returns the far end of the call, the samples to
// diarize, or nil when the session recorded no monitor source.
//
// An M4A keeps each source on its own stream (see SaveAudioM4A): the
// monitor is stream 2 next to a mic, otherwise stream 0. Older sessions
// saved one MP3 with a dual-source recording's two sources one after the
// other, so there the monitor is the second half.
func extractMonitorAudio(audioPath string, sess *Session) ([]float32, error) {
	if !slices.Contains(sess.Sources, "monitor") {
		return nil, nil
	}
	withMic := slices.Contains(sess.Sources, "mic")
	if IsM4A(audioPath) {
		stream := 0
		if withMic {
			stream = 2
		}
		return DecodeTrackToFloat32(audioPath, stream)
	}
	all, err := DecodeToFloat32(audioPath)
	if err != nil || !withMic {
		return all, err
	}
	return all[len(all)/2:], nil
}

// DecodeToFloat32 decodes any audio file ffmpeg reads (MP3, WAV, ...) to
// 16 kHz mono float32 PCM, downmixing all channels.
func DecodeToFloat32(path string) ([]float32, error) {
	return decodePCM(path)
}
