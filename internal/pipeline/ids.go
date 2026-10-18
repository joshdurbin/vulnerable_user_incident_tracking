// Package pipeline holds the Temporal workflows and activities that run the
// platform: discovery, per-article processing, per-incident lifecycle,
// backfills and reprocessing. Workflows carry only IDs and small summaries;
// article bodies stay in PostgreSQL, never in workflow history.
package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
)

// Task queues. Each has its own worker concurrency and rate limit so a slow
// LLM or a polite crawler never starves the others.
const (
	TQWorkflow  = "collision-workflow"
	TQCrawl     = "collision-crawl"  // Python: HTTP fetch and link discovery
	TQRender    = "collision-render" // Python: headless browser; separate pods because Chromium is heavy
	TQLLM       = "collision-llm"
	TQDiscovery = "collision-discovery" // run a single replica: provider rate limits are per process
	TQDB        = "collision-db"
)

const DefaultNamespace = "collisions"

// Signal, update and query names.
const (
	SignalArticleLinked = "article-linked"
	UpdateDecideReview  = "decide-review"
	QueryStatus         = "status"
	QueryProgress       = "progress"
	SignalPause         = "pause"
	SignalResume        = "resume"
	SignalCancel        = "cancel"
)

// Search attribute names (must be registered on the Temporal namespace before
// enabling Options.SearchAttributes; see docs/operations.md).
const (
	SAArticleURL   = "CollisionURL"
	SASource       = "CollisionSource"
	SAVerdict      = "CollisionVerdict"
	SAStage        = "CollisionStage"
	SAModel        = "CollisionModel"
	SAIncidentID   = "CollisionIncidentID"
	SAWorkflowKind = "CollisionKind"
)

func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:20]
}

// ArticleWorkflowID is deterministic per normalized URL, so the same article
// found by several providers or runs is processed once.
func ArticleWorkflowID(normalizedURL string) string { return "article-" + shortHash(normalizedURL) }
func IncidentWorkflowID(incidentID string) string   { return "incident-" + incidentID }
func DiscoveryScheduleID(slug string) string        { return "discover-" + slug }
