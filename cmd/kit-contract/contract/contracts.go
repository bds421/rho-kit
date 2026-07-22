// Package contract defines portable service-contract artifacts and conservative
// compatibility checks. It intentionally supports only OpenAPI 3.1 documents
// and JSON Schema event documents in v2; unsupported schema constructs fail
// closed instead of producing a misleading compatibility verdict.
package contract

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const ManifestFilename = "contracts.json"

type Kind string

const (
	KindOpenAPI         Kind = "openapi"
	KindEventJSONSchema Kind = "event-jsonschema"
)

type Manifest struct {
	Format    int        `json:"format"`
	Artifacts []Artifact `json:"artifacts"`
}

type Artifact struct {
	ID            string        `json:"id"`
	Owner         string        `json:"owner"`
	Kind          Kind          `json:"kind"`
	Version       string        `json:"version"`
	Path          string        `json:"path"`
	SchemaVersion uint          `json:"schema_version,omitempty"`
	Compatibility Compatibility `json:"compatibility"`
	Waivers       []Waiver      `json:"waivers,omitempty"`
}

type Compatibility struct {
	Mode string `json:"mode"`
}

type Waiver struct {
	Code      string `json:"code"`
	Reason    string `json:"reason"`
	ExpiresAt string `json:"expires_at"`
}

type Finding struct {
	Artifact string `json:"artifact"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	Waived   bool   `json:"waived"`
}

type Report struct {
	Compatible bool      `json:"compatible"`
	Findings   []Finding `json:"findings"`
}

var semver = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)

func LoadDir(dir string) (Manifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, ManifestFilename))
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return Manifest{}, fmt.Errorf("contracts: parse manifest: %w", err)
	}
	if err := ValidateManifest(m); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

func ValidateDir(dir string) error {
	m, err := LoadDir(dir)
	if err != nil {
		return err
	}
	for _, a := range m.Artifacts {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(a.Path)))
		if err != nil {
			return fmt.Errorf("contracts: read %s: %w", a.ID, err)
		}
		if err := validateDocument(a, b); err != nil {
			return err
		}
	}
	return nil
}

func ValidateManifest(m Manifest) error {
	if m.Format != 1 {
		return fmt.Errorf("contracts: manifest format must be 1")
	}
	if len(m.Artifacts) == 0 {
		return errors.New("contracts: manifest requires at least one artifact")
	}
	seen := map[string]struct{}{}
	for _, a := range m.Artifacts {
		if a.ID == "" || a.Owner == "" || a.Path == "" {
			return errors.New("contracts: artifact id, owner, and path are required")
		}
		if _, ok := seen[a.ID]; ok {
			return fmt.Errorf("contracts: duplicate artifact id %q", a.ID)
		}
		seen[a.ID] = struct{}{}
		if a.Kind != KindOpenAPI && a.Kind != KindEventJSONSchema {
			return fmt.Errorf("contracts: artifact %s has unsupported kind %q", a.ID, a.Kind)
		}
		if a.Kind == KindEventJSONSchema && a.SchemaVersion == 0 {
			return fmt.Errorf("contracts: event artifact %s requires a non-zero schema_version", a.ID)
		}
		if !semver.MatchString(a.Version) {
			return fmt.Errorf("contracts: artifact %s has invalid semantic version %q", a.ID, a.Version)
		}
		if a.Compatibility.Mode != "backward" {
			return fmt.Errorf("contracts: artifact %s compatibility mode must be backward", a.ID)
		}
		if filepath.IsAbs(a.Path) || strings.Contains(filepath.ToSlash(a.Path), "../") {
			return fmt.Errorf("contracts: artifact %s path must be relative and contained", a.ID)
		}
		for _, w := range a.Waivers {
			if w.Code == "" || w.Reason == "" || w.ExpiresAt == "" {
				return fmt.Errorf("contracts: artifact %s waiver requires code, reason, and expires_at", a.ID)
			}
			if _, err := time.Parse(time.DateOnly, w.ExpiresAt); err != nil {
				return fmt.Errorf("contracts: artifact %s waiver expiry: %w", a.ID, err)
			}
		}
	}
	return nil
}

func CompareDirs(candidateDir, baselineDir string) (Report, error) {
	if err := ValidateDir(candidateDir); err != nil {
		return Report{}, fmt.Errorf("candidate: %w", err)
	}
	if err := ValidateDir(baselineDir); err != nil {
		return Report{}, fmt.Errorf("baseline: %w", err)
	}
	candidate, _ := LoadDir(candidateDir)
	baseline, _ := LoadDir(baselineDir)
	byID := make(map[string]Artifact, len(candidate.Artifacts))
	for _, a := range candidate.Artifacts {
		byID[a.ID] = a
	}
	report := Report{Compatible: true}
	for _, old := range baseline.Artifacts {
		current, ok := byID[old.ID]
		if !ok {
			report.add(old, "artifact_removed", "artifact present in baseline is missing from candidate")
			continue
		}
		if current.Kind != old.Kind {
			report.add(current, "artifact_kind_changed", "artifact kind changed from "+string(old.Kind)+" to "+string(current.Kind))
			continue
		}
		if old.Kind == KindEventJSONSchema && old.SchemaVersion != current.SchemaVersion {
			report.add(current, "event_schema_version_changed", "event schema_version changed; publish a separate transitioned artifact instead")
		}
		oldDoc, err := readJSON(filepath.Join(baselineDir, filepath.FromSlash(old.Path)))
		if err != nil {
			return Report{}, err
		}
		newDoc, err := readJSON(filepath.Join(candidateDir, filepath.FromSlash(current.Path)))
		if err != nil {
			return Report{}, err
		}
		switch old.Kind {
		case KindOpenAPI:
			compareOpenAPI(&report, current, oldDoc, newDoc)
		case KindEventJSONSchema:
			compareSchema(&report, current, oldDoc, newDoc, "event")
		}
	}
	sort.Slice(report.Findings, func(i, j int) bool {
		if report.Findings[i].Artifact == report.Findings[j].Artifact {
			return report.Findings[i].Code < report.Findings[j].Code
		}
		return report.Findings[i].Artifact < report.Findings[j].Artifact
	})
	return report, nil
}

func (r *Report) add(a Artifact, code, message string) {
	f := Finding{Artifact: a.ID, Code: code, Message: message}
	today := time.Now().UTC().Format(time.DateOnly)
	for _, w := range a.Waivers {
		if w.Code == code && w.ExpiresAt >= today {
			f.Waived = true
			break
		}
	}
	if !f.Waived {
		r.Compatible = false
	}
	r.Findings = append(r.Findings, f)
}

func validateDocument(a Artifact, b []byte) error {
	doc, err := readJSONBytes(b)
	if err != nil {
		return fmt.Errorf("contracts: artifact %s is not JSON: %w", a.ID, err)
	}
	switch a.Kind {
	case KindOpenAPI:
		if v, _ := doc["openapi"].(string); !strings.HasPrefix(v, "3.1.") {
			return fmt.Errorf("contracts: artifact %s must be OpenAPI 3.1", a.ID)
		}
		if _, ok := doc["paths"].(map[string]any); !ok {
			return fmt.Errorf("contracts: artifact %s OpenAPI document requires paths", a.ID)
		}
	case KindEventJSONSchema:
		if v, _ := doc["$schema"].(string); v != "https://json-schema.org/draft/2020-12/schema" {
			return fmt.Errorf("contracts: artifact %s must declare JSON Schema 2020-12", a.ID)
		}
		if err := validateSupportedSchema(doc, "$ "); err != nil {
			return fmt.Errorf("contracts: artifact %s: %w", a.ID, err)
		}
	}
	return nil
}

func readJSON(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return readJSONBytes(b)
}

func readJSONBytes(b []byte) (map[string]any, error) {
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, errors.New("document must be a JSON object")
	}
	return doc, nil
}

var supportedSchemaKeys = map[string]bool{
	"$schema": true, "$id": true, "title": true, "description": true,
	"type": true, "properties": true, "required": true, "additionalProperties": true,
	"items": true, "enum": true, "const": true, "format": true,
	"minimum": true, "maximum": true, "minLength": true, "maxLength": true,
	"pattern": true, "minItems": true, "maxItems": true,
}

func validateSupportedSchema(schema map[string]any, path string) error {
	for k, v := range schema {
		if !supportedSchemaKeys[k] {
			return fmt.Errorf("unsupported JSON Schema keyword %q at %s", k, path)
		}
		if k == "properties" {
			properties, ok := v.(map[string]any)
			if !ok {
				return fmt.Errorf("properties at %s must be an object", path)
			}
			for name, raw := range properties {
				child, ok := raw.(map[string]any)
				if !ok {
					return fmt.Errorf("property %q at %s must be an object schema", name, path)
				}
				if err := validateSupportedSchema(child, path+"/properties/"+name); err != nil {
					return err
				}
			}
		}
		if k == "items" {
			child, ok := v.(map[string]any)
			if !ok {
				return fmt.Errorf("items at %s must be an object schema", path)
			}
			if err := validateSupportedSchema(child, path+"/items"); err != nil {
				return err
			}
		}
	}
	return nil
}
