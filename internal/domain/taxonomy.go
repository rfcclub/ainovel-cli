package domain

import "slices"

var (
	hookTypes       = []string{"crisis", "mystery", "desire", "emotion", "choice"}
	dominantStrands = []string{"quest", "fire", "constellation"}
)

// HookTypes returns the chapter-hook taxonomy. It returns a copy so callers cannot mutate the domain vocabulary.
func HookTypes() []string { return slices.Clone(hookTypes) }

// DominantStrands returns the dominant narrative strand taxonomy.
func DominantStrands() []string { return slices.Clone(dominantStrands) }

func ValidHookType(value string) bool       { return slices.Contains(hookTypes, value) }
func ValidDominantStrand(value string) bool { return slices.Contains(dominantStrands, value) }
