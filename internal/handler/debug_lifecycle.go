package handler

import (
	"cyberstrike-ai/internal/debug"
)

// wrapRunWithDebug calls sink.StartSession before runFn and
// sink.EndSession after (with the outcome runFn returned). Used by
// the non-streaming MultiAgentLoop handler, which has a simple
// "success/error → outcome" flow.
//
// The streaming MultiAgentLoopStream handler does NOT use this
// helper. Its flow mutates a local taskStatus across multiple
// branches (cancelled / failed / completed) and needs the
// EndSession to fire with the final value, so it uses a raw
// h.debugSink.StartSession call paired with a deferred closure
// that reads taskStatus at exec time. See multi_agent.go for the
// pattern.
//
// runFn returns (outcome, err) where outcome is one of
// "completed"|"cancelled"|"failed"|"interrupted"|"". Empty-string
// outcome is defaulted to "failed" on non-nil err, "completed" on
// nil err — so a caller that doesn't bother classifying still
// produces a terminal debug_sessions row.
//
// EndSession is deferred inside the helper body (via named-return
// closure) so it fires even if runFn panics.
func wrapRunWithDebug(sink debug.Sink, conversationID string, runFn func() (string, error)) (outcome string, err error) {
	if sink != nil {
		sink.StartSession(conversationID)
	}
	defer func() {
		// Apply defaults before EndSession fires so the deferred
		// call always receives a non-empty outcome string.
		if outcome == "" {
			if err != nil {
				outcome = "failed"
			} else {
				outcome = "completed"
			}
		}
		if sink != nil {
			sink.EndSession(conversationID, outcome)
		}
	}()
	outcome, err = runFn()
	return outcome, err
}
