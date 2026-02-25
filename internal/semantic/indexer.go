package semantic

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gokin/internal/git"
)

// ChunkInfo represents a chunk of code with its embedding.
type ChunkInfo struct {
	FilePath  string
	LineStart int
	LineEnd   int
	Content   string
	Embedding []float32
}

// Indexer manages file indexing for semantic search.
type Indexer struct {
	embedder    *Embedder
	workDir     string
	cache       *EmbeddingCache
	gitIgnore   *git.GitIgnore
	maxFileSize int64
	chunker     Chunker
	chunks      map[string][]ChunkInfo // filePath -> chunks
	fileSymbols map[string]fileSymbolIndex
	mu          sync.RWMutex
}

// NewIndexer creates a new indexer.
func NewIndexer(embedder *Embedder, workDir string, cache *EmbeddingCache, maxFileSize int64) *Indexer {
	gitIgnore := git.NewGitIgnore(workDir)
	_ = gitIgnore.Load() // Ignore error - gitignore is optional

	return &Indexer{
		embedder:    embedder,
		workDir:     workDir,
		cache:       cache,
		gitIgnore:   gitIgnore,
		maxFileSize: maxFileSize,
		chunker:     NewStructuralChunker(50, 10),
		chunks:      make(map[string][]ChunkInfo),
		fileSymbols: make(map[string]fileSymbolIndex),
	}
}

// IndexFile indexes a single file.
func (i *Indexer) IndexFile(ctx context.Context, filePath string) error {
	// Check if file should be ignored
	relPath, err := filepath.Rel(i.workDir, filePath)
	if err != nil {
		relPath = filePath
	}

	if i.gitIgnore.IsIgnored(relPath) {
		return nil
	}

	// Check file size
	info, err := os.Stat(filePath)
	if err != nil {
		return err
	}
	if info.Size() > i.maxFileSize {
		return nil // Skip large files
	}

	// Skip binary files and non-code files
	if !isCodeFile(filePath) {
		return nil
	}

	// Read file content
	content, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	symbolIndex := extractFileSymbols(filePath, string(content))

	// Split into chunks using structural chunker
	chunks := i.chunker.Chunk(filePath, string(content))
	if len(chunks) == 0 {
		i.mu.Lock()
		i.fileSymbols[filePath] = symbolIndex
		i.mu.Unlock()
		return nil
	}

	// Generate embeddings for each chunk
	indexedChunks := make([]ChunkInfo, 0, len(chunks))
	for _, chunk := range chunks {
		// Check cache first
		cacheKey := fmt.Sprintf("%s:%d", filePath, chunk.LineStart)
		contentHash := ContentHash(chunk.Content)

		if embedding, ok := i.cache.Get(cacheKey, contentHash); ok {
			chunk.Embedding = embedding
			indexedChunks = append(indexedChunks, chunk)
			continue
		}

		// Generate embedding
		embedding, err := i.embedder.Embed(ctx, chunk.Content)
		if err != nil {
			continue // Skip chunk on error
		}

		chunk.Embedding = embedding
		indexedChunks = append(indexedChunks, chunk)

		// Cache the embedding
		i.cache.Set(cacheKey, embedding, contentHash)
	}

	// Store indexed chunks
	i.mu.Lock()
	i.chunks[filePath] = indexedChunks
	i.fileSymbols[filePath] = symbolIndex
	i.mu.Unlock()

	return nil
}

// IndexDirectory indexes all files in a directory recursively.
func (i *Indexer) IndexDirectory(ctx context.Context, dir string) error {
	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip errors
		}

		// Check context cancellation
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if info.IsDir() {
			// Skip hidden directories and common non-code directories
			name := info.Name()
			if strings.HasPrefix(name, ".") || isSkipDir(name) {
				return filepath.SkipDir
			}
			return nil
		}

		return i.IndexFile(ctx, path)
	})
}

// Search performs semantic search across indexed files.
func (i *Indexer) Search(ctx context.Context, query string, topK int) ([]SearchResult, error) {
	if topK <= 0 {
		topK = 10
	}

	// Generate query embedding
	queryEmbedding, err := i.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to embed query: %w", err)
	}

	// Snapshot indexed chunks so ranking can run lock-free.
	i.mu.RLock()
	chunksByFile := make(map[string][]ChunkInfo, len(i.chunks))
	symbolsByFile := make(map[string]fileSymbolIndex, len(i.fileSymbols))
	for filePath, chunks := range i.chunks {
		copied := make([]ChunkInfo, len(chunks))
		copy(copied, chunks)
		chunksByFile[filePath] = copied
	}
	for filePath, symbols := range i.fileSymbols {
		symbolsByFile[filePath] = symbols.clone()
	}
	i.mu.RUnlock()

	signals := buildSearchSignals(ctx, i.workDir, query, chunksByFile, symbolsByFile)
	var ranked SearchResults

	for filePath, chunks := range chunksByFile {
		fileSignals := signals.FileSignals[filePath]
		for _, chunk := range chunks {
			if chunk.Embedding == nil {
				continue
			}

			embeddingScore := float64(CosineSimilarity(queryEmbedding, chunk.Embedding))
			lexicalScore := lexicalRelevance(signals.Query, chunk.Content)
			pathScore := fileSignals.PathHintScore
			dependencyScore := fileSignals.DependencyScore
			changeProximity := fileSignals.ChangeProximity
			freshnessScore := fileSignals.FreshnessScore
				symbolBonus := symbolHintBonus(signals.Query, chunk.Content)
				symbolIndexScore := fileSignals.SymbolIndexScore

				// Hybrid scoring:
				// semantic signal dominates, lexical/path/dependency/freshness/change
				// refine ranking toward practical edit impact.
				// Weights sum to 1.00: 0.55 + 0.18 + 0.08 + 0.06 + 0.03 + 0.03 + 0.07
				finalScore := 0.55*embeddingScore +
					0.18*lexicalScore +
					0.08*pathScore +
					0.06*dependencyScore +
					0.03*freshnessScore +
					0.03*changeProximity +
					0.07*symbolIndexScore +
					symbolBonus

			if finalScore <= 0 {
				continue
			}

			ranked = append(ranked, SearchResult{
				FilePath:          chunk.FilePath,
				Score:             float32(finalScore),
				BaseScore:         float32(embeddingScore),
				LexicalScore:      float32(lexicalScore),
				PathScore:         float32(pathScore),
				DependencyScore:   float32(dependencyScore),
				FreshnessScore:    float32(freshnessScore),
					ChangeProximity:   float32(changeProximity),
					SymbolHintBonus:   float32(symbolBonus),
					SymbolIndexScore:  float32(symbolIndexScore),
					DefinitionHits:    fileSignals.DefinitionHits,
					CallerHits:        fileSignals.CallerHits,
					UsageHits:         fileSignals.UsageHits,
					Content:           chunk.Content,
					LineStart:         chunk.LineStart,
					LineEnd:           chunk.LineEnd,
				DependencyDegree:  fileSignals.DependencyDegree,
				DependentDegree:   fileSignals.DependentDegree,
				ChangedFileDirect: fileSignals.ChangedFileDirect,
			})
		}
	}

	sort.Sort(ranked)
	results := deduplicateSearchResults(ranked, topK)
	if len(results) > topK {
		results = results[:topK]
	}

	return results, nil
}

// GetIndexedFileCount returns the number of indexed files.
func (i *Indexer) GetIndexedFileCount() int {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return len(i.chunks)
}

// SaveCache saves the embedding cache to disk.
func (i *Indexer) SaveCache() error {
	if i.cache != nil {
		return i.cache.Save()
	}
	return nil
}

// RemoveFile removes a file's chunks and symbol index entries.
func (i *Indexer) RemoveFile(filePath string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	delete(i.chunks, filePath)
	delete(i.fileSymbols, filePath)
}

// isCodeFile checks if a file is likely a code file based on extension.
func isCodeFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	codeExts := map[string]bool{
		".go": true, ".py": true, ".js": true, ".ts": true, ".tsx": true, ".jsx": true,
		".java": true, ".c": true, ".cpp": true, ".h": true, ".hpp": true,
		".rs": true, ".rb": true, ".php": true, ".swift": true, ".kt": true,
		".scala": true, ".cs": true, ".fs": true, ".hs": true, ".ml": true,
		".lua": true, ".r": true, ".sh": true, ".bash": true, ".zsh": true,
		".sql": true, ".html": true, ".css": true, ".scss": true, ".less": true,
		".json": true, ".yaml": true, ".yml": true, ".toml": true, ".xml": true,
		".md": true, ".txt": true, ".rst": true,
	}
	return codeExts[ext]
}

// isSkipDir checks if a directory should be skipped.
func isSkipDir(name string) bool {
	skipDirs := map[string]bool{
		"node_modules": true, "vendor": true, "target": true, "build": true,
		"dist": true, "out": true, "__pycache__": true, ".git": true,
		".idea": true, ".vscode": true, "bin": true, "obj": true,
	}
	return skipDirs[name]
}
