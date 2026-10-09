package stages

import (
	"context"

	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/store"
)

const EmbedVersion = "e2"

const embedBatch = 32

// Embed returns a vector per chunk id.
func Embed(ctx context.Context, e provider.Embedder, chunks []store.Chunk) (map[int64][]float32, error) {
	out := make(map[int64][]float32, len(chunks))
	for i := 0; i < len(chunks); i += embedBatch {
		batch := chunks[i:min(i+embedBatch, len(chunks))]
		texts := make([]string, len(batch))
		for k, c := range batch {
			// The title gives a passage its context: "renewal" means more
			// inside "Acme Freight renewal" than on its own.
			texts[k] = c.Text
			if c.Title != "" {
				texts[k] = c.Title + "\n" + c.Text
			}
		}
		vecs, err := e.Embed(ctx, texts, false)
		if err != nil {
			return nil, err
		}
		for k, c := range batch {
			out[c.ID] = vecs[k]
		}
	}
	return out, nil
}
