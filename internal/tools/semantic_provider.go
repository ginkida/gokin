package tools

import "context"

// SemanticProvider is the narrow integration boundary between the built-in
// semantic tools and a managed Go intelligence service (for example, a
// persistent gopls session). Implementations are workspace-bound; callers pass
// canonical file paths that have already crossed the tool's PathValidator.
//
// An empty result with a nil error is an authoritative "no matches" answer.
// A non-nil error means the provider could not answer and lets the tool return
// an explicitly degraded, lexical/AST fallback instead of misreporting the
// transport failure as "not found".
type SemanticProvider interface {
	FindReferences(context.Context, SemanticReferencesRequest) (SemanticQueryResult, error)
	SearchSymbols(context.Context, SemanticSearchRequest) (SemanticQueryResult, error)
}

// SemanticProviderAware is implemented by tools that can be wired to a managed
// semantic provider after registry construction.
type SemanticProviderAware interface {
	SetSemanticProvider(SemanticProvider)
}

// SemanticReferencesRequest describes a semantic reference lookup. Line and
// Column are optional and zero when omitted; providers that resolve by symbol
// name may ignore them.
type SemanticReferencesRequest struct {
	File              string
	Symbol            string
	Line              int
	Column            int
	IncludeDefinition bool
	Limit             int
}

// SemanticSearchRequest describes a fuzzy workspace-symbol search.
type SemanticSearchRequest struct {
	Query string
	Limit int
}

// SemanticQueryResult is deliberately structured. In particular, an empty
// Matches slice with nil error is not interchangeable with a provider failure.
type SemanticQueryResult struct {
	Matches   []SemanticMatch
	Truncated bool
}

// SemanticMatch is the common location shape returned by managed providers and
// bounded fallbacks. File may be absolute or workspace-relative on input; tools
// normalize it before presenting it to the model.
type SemanticMatch struct {
	File    string `json:"file"`
	Line    int    `json:"line,omitempty"`
	Column  int    `json:"column,omitempty"`
	Name    string `json:"name,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Snippet string `json:"snippet,omitempty"`
}

// SemanticResultSource identifies which accuracy tier produced a tool result.
type SemanticResultSource string

const (
	SemanticSourceProvider SemanticResultSource = "provider"
	SemanticSourceFallback SemanticResultSource = "fallback"
)

// SemanticResultData is attached to every semantic result. DegradedReason is
// populated only for fallback answers, so callers never have to infer degraded
// accuracy by parsing human-facing text.
type SemanticResultData struct {
	Source         SemanticResultSource `json:"source"`
	DegradedReason string               `json:"degraded_reason,omitempty"`
	Matches        []SemanticMatch      `json:"matches,omitempty"`
	MatchCount     int                  `json:"match_count"`
	Truncated      bool                 `json:"truncated,omitempty"`
}
