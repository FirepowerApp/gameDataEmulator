package gamereplay

// Shared slog attribute keys. Defined once here (gamereplay is a leaf package
// services already imports) so services and gamereplay can't drift on the
// keys Aptakube substring-filtering depends on.
const (
	LogKeyGame     = "game"     // schedule game ID — the primary filter key
	LogKeyFeed     = "feed"     // "pbp" or "stats"
	LogKeyUpstream = "upstream" // resolved real upstream ID (may differ from LogKeyGame for synthetic duplicates)
)
