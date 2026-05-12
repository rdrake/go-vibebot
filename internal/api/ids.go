package api

// Typed IDs. Distinct string newtypes catch wrong-axis mixups at compile
// time: passing a CharacterID where a SceneID is wanted (or vice versa)
// is a type error, not a runtime mystery.
type (
	CharacterID string
	SceneID     string
	PlaceID     string
	GroupID     string
)
