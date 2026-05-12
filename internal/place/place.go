// Package place defines locations. NPC instantiation lands in the second
// build phase; for the walking skeleton a Place is a labelled location only.
package place

// Place is a location characters can be summoned to. NPCs are listed by
// character ID and resolved at summon time.
type Place struct {
	ID          string
	Name        string
	Description string
	NPCs        []string
}
