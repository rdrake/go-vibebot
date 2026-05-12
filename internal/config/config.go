// Package config loads YAML seed data into runtime structs. It deliberately
// does not depend on internal/character or internal/scene: callers map these
// flat types into wired runtime objects.
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// CharacterSpec is the YAML shape of a character.
type CharacterSpec struct {
	ID           string   `yaml:"id"`
	Name         string   `yaml:"name"`
	Persona      string   `yaml:"persona"`
	Capabilities []string `yaml:"capabilities"`
	Blurb        string   `yaml:"blurb"`
}

// GroupSpec is the YAML shape of a group.
type GroupSpec struct {
	ID      string   `yaml:"id"`
	Name    string   `yaml:"name"`
	Leader  string   `yaml:"leader"`
	Members []string `yaml:"members"`
}

// PlaceSpec is the YAML shape of a place (used in build phase 2).
type PlaceSpec struct {
	ID          string   `yaml:"id"`
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	NPCs        []string `yaml:"npcs"`
}

// LoadCharacters parses a YAML file containing a list of characters.
func LoadCharacters(path string) ([]CharacterSpec, error) {
	var out struct {
		Characters []CharacterSpec `yaml:"characters"`
	}
	if err := readYAML(path, &out); err != nil {
		return nil, err
	}
	return out.Characters, nil
}

// LoadGroups parses a YAML file containing a list of groups.
func LoadGroups(path string) ([]GroupSpec, error) {
	var out struct {
		Groups []GroupSpec `yaml:"groups"`
	}
	if err := readYAML(path, &out); err != nil {
		return nil, err
	}
	return out.Groups, nil
}

// LoadPlace parses a single place YAML file.
func LoadPlace(path string) (PlaceSpec, error) {
	var p PlaceSpec
	if err := readYAML(path, &p); err != nil {
		return PlaceSpec{}, err
	}
	return p, nil
}

func readYAML(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(b, v); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}
