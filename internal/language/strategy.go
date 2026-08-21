package language

import "context"

// Detection records the confidence and bounded local evidence produced by a
// language strategy. A non-positive score means the strategy did not match.
type Detection struct {
	Score    int
	Evidence []string
}

// Strategy detects one language without running project code and builds the
// trusted profile used by a coding turn.
type Strategy interface {
	ID() LanguageID
	Detect(ctx context.Context, root string) (Detection, error)
	BuildProfile(ctx context.Context, root string) (LanguageProfile, error)
}
