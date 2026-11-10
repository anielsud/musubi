// Package sigfix keeps ONNX Runtime's crash handler from tripping Go's
// signal checks.
package sigfix

/*
#include <signal.h>

// ONNX Runtime installs a SIGSEGV handler without SA_ONSTACK. Go requires
// that flag on handlers it may chain to, and aborts the process when it
// finds one without. Re-register the same handler with the flag added.
static void add_onstack_to_segv(void) {
	struct sigaction act;
	if (sigaction(SIGSEGV, NULL, &act) != 0) return;
	if (act.sa_handler == SIG_DFL || act.sa_handler == SIG_IGN) return;
	if (act.sa_flags & SA_ONSTACK) return;
	act.sa_flags |= SA_ONSTACK;
	sigaction(SIGSEGV, &act, NULL);
}
*/
import "C"

// AfterSherpa repairs the handler. Each sherpa-onnx constructor (recognizer,
// VAD, speaker model and so on) can install it again, so call this after
// every one.
func AfterSherpa() { C.add_onstack_to_segv() }
