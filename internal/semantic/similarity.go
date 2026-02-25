package semantic

import "math"

// CosineSimilarity calculates the cosine similarity between two vectors.
// Returns a value between -1 and 1, where 1 means identical direction.
func CosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}

	var dotProduct, normA, normB float64

	for i := range a {
		dotProduct += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}

	if normA == 0 || normB == 0 {
		return 0
	}

	return float32(dotProduct / (math.Sqrt(normA) * math.Sqrt(normB)))
}

// SearchResult represents a search result with similarity score.
type SearchResult struct {
	FilePath string  // Path to the file
	Score    float32 // Final hybrid score (semantic + retrieval signals)

	// Score breakdown for explainability/debugging.
	BaseScore       float32 // Embedding cosine similarity component
	LexicalScore    float32 // Query-token lexical overlap
	PathScore       float32 // Path hint match score
	DependencyScore float32 // Dependency centrality/impact score
	FreshnessScore  float32 // Recency score from file modification time
	ChangeProximity float32 // Boost for changed files and neighbors
	SymbolHintBonus float32 // Bonus when symbol-like hints are present
	SymbolIndexScore float32 // Symbol-index (definition/usage/caller) + hop score

	DependencyDegree  int  // Outgoing dependency count for file
	DependentDegree   int  // Incoming dependent count for file
	DefinitionHits    int  // Matching symbol definitions in file
	CallerHits        int  // Matching symbol caller-sites in file
	UsageHits         int  // Matching symbol usages in file
	ChangedFileDirect bool // True when file is directly changed in working tree

	Content   string // Matched content chunk
	LineStart int    // Starting line number
	LineEnd   int    // Ending line number
}

// SearchResults is a sortable slice of SearchResult.
type SearchResults []SearchResult

func (r SearchResults) Len() int           { return len(r) }
func (r SearchResults) Less(i, j int) bool { return r[i].Score > r[j].Score } // Descending by score
func (r SearchResults) Swap(i, j int)      { r[i], r[j] = r[j], r[i] }
