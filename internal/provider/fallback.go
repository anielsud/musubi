package provider

import "context"

// Fallback answers with Primary and, when it fails (unreachable, refused,
// rate limited past its waits), with Secondary: a hosted model in front,
// a local one behind it. Streamed text from a failed primary isn't taken
// back; callers that show tokens replace them with the final text.
type Fallback struct {
	Primary, Secondary LLM
	// Note is told why the secondary answered (nil: nothing).
	Note func(error)
}

func (f Fallback) Complete(ctx context.Context, msgs []Message, opt ChatOptions, onToken func(string)) (string, error) {
	out, err := f.Primary.Complete(ctx, msgs, opt, onToken)
	if err == nil || ctx.Err() != nil || f.Secondary == nil {
		return out, err
	}
	if f.Note != nil {
		f.Note(err)
	}
	return f.Secondary.Complete(ctx, msgs, opt, onToken)
}

// Local is true only when both are local: what may be sent is decided by
// the less private of the two.
func (f Fallback) Local() bool {
	return f.Primary.Local() && (f.Secondary == nil || f.Secondary.Local())
}

func (f Fallback) ModelName() string { return f.Primary.ModelName() }
