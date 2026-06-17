package tools

// Shared max_tokens auto-continuation parameters, used by BOTH agentic loops:
// the foreground executor (executor.go executeLoop) and the sub-agent loop
// (agent.go executeLoop). When a provider truncates a TEXT response at the
// output-token limit (FinishReason == MaxTokens with no function calls), the
// loop asks the model to continue exactly where it stopped instead of surfacing
// a fake "done" turn.
//
// The two loops keep their own text-stitching and history mutation (the
// executor stitches carriedText and fires OnWarning; the agent writes straight
// to its output buffer + a.history under stateMu) because those parts differ
// structurally. They share ONLY these two knobs so the budget and the
// continuation prompt can never drift apart — the exact two-hand-synced-copy
// hazard Tier-4 (slice 2) exists to remove. They were added as two identical
// copies in v0.100.17/.18; this is their single home.

// MaxTruncationContinuations bounds how many times a max_tokens-truncated TEXT
// response is auto-continued before the loop gives up and surfaces the
// truncation warning. A model should emit large content via tools (write), not
// a giant text turn — so a few continuations covers the legitimate case (a long
// plan/explanation cut off) without risking a runaway.
const MaxTruncationContinuations = 3

// TruncationContinuationPrompt is the user-role nudge appended to history to
// resume a truncated turn. It must tell the model NOT to repeat already-written
// text and to call the next tool if one is needed.
const TruncationContinuationPrompt = "Continue exactly where the previous assistant message stopped. Do not repeat already-written text. If the next needed action is a tool call, call the tool now; otherwise finish the answer."
