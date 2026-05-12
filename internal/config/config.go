// Package config loads YAML seed data into runtime structs. It deliberately
// does not depend on internal/character or internal/scene: callers map these
// flat types into wired runtime objects.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

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

// LoadPlaces reads every .yaml/.yml file in dir as a PlaceSpec and returns
// them sorted by ID. A missing directory returns an empty slice (not an
// error); other I/O failures are surfaced.
func LoadPlaces(dir string) ([]PlaceSpec, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read places dir %s: %w", dir, err)
	}
	var places []PlaceSpec
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}
		p, err := LoadPlace(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		places = append(places, p)
	}
	sort.Slice(places, func(i, j int) bool { return places[i].ID < places[j].ID })
	return places, nil
}
