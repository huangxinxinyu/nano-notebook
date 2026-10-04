package promptcatalog

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

//go:embed prompts/*.md archive/prompts/*.md
var embeddedFiles embed.FS

type PromptVersion struct {
	Identity   string
	Version    int
	Contract   string
	Content    string
	SHA256     string
	SourcePath string
	Archived   bool
}

type Catalog struct {
	versions map[string]PromptVersion
}

func LoadEmbedded() (Catalog, error) {
	paths, err := fs.Glob(embeddedFiles, "prompts/*.md")
	if err != nil {
		return Catalog{}, err
	}
	archivedPaths, err := fs.Glob(embeddedFiles, "archive/prompts/*.md")
	if err != nil {
		return Catalog{}, err
	}
	paths = append(paths, archivedPaths...)
	definitions := make([]PromptVersion, 0, len(paths))
	for _, path := range paths {
		payload, err := embeddedFiles.ReadFile(path)
		if err != nil {
			return Catalog{}, err
		}
		// Keep the original logical path for immutable database registrations.
		definition, err := parseMarkdown(strings.TrimPrefix(path, "archive/"), string(payload))
		if err != nil {
			return Catalog{}, err
		}
		definition.Archived = strings.HasPrefix(path, "archive/")
		definitions = append(definitions, definition)
	}
	return New(definitions)
}

func MustLoadEmbedded() Catalog {
	catalog, err := LoadEmbedded()
	if err != nil {
		panic(err)
	}
	return catalog
}

func New(definitions []PromptVersion) (Catalog, error) {
	catalog := Catalog{versions: make(map[string]PromptVersion, len(definitions))}
	for _, definition := range definitions {
		definition.Identity = strings.TrimSpace(definition.Identity)
		definition.Contract = strings.TrimSpace(definition.Contract)
		definition.Content = canonicalContent(definition.Content)
		definition.SourcePath = strings.TrimSpace(definition.SourcePath)
		if err := validate(definition); err != nil {
			return Catalog{}, err
		}
		hash, err := CanonicalSHA256(definition)
		if err != nil {
			return Catalog{}, err
		}
		if definition.SHA256 != "" && !strings.EqualFold(definition.SHA256, hash) {
			return Catalog{}, fmt.Errorf("prompt %s@%d hash conflict", definition.Identity, definition.Version)
		}
		definition.SHA256 = hash
		key := catalogKey(definition.Identity, definition.Version)
		if existing, ok := catalog.versions[key]; ok {
			if existing.SHA256 != definition.SHA256 {
				return Catalog{}, fmt.Errorf("prompt %s@%d definition conflict", definition.Identity, definition.Version)
			}
			continue
		}
		catalog.versions[key] = definition
	}
	if len(catalog.versions) == 0 {
		return Catalog{}, errors.New("prompt catalog is empty")
	}
	return catalog, nil
}

func (c Catalog) Resolve(identity string, version int) (PromptVersion, bool) {
	definition, ok := c.versions[catalogKey(strings.TrimSpace(identity), version)]
	return definition, ok
}

func (c Catalog) Versions() []PromptVersion {
	versions := make([]PromptVersion, 0, len(c.versions))
	for _, definition := range c.versions {
		versions = append(versions, definition)
	}
	sort.Slice(versions, func(left, right int) bool {
		if versions[left].Identity == versions[right].Identity {
			return versions[left].Version < versions[right].Version
		}
		return versions[left].Identity < versions[right].Identity
	})
	return versions
}

// ActiveVersions is the maintained prompt surface. Versions and Resolve also
// include historical definitions needed to resume pinned runs.
func (c Catalog) ActiveVersions() []PromptVersion {
	versions := c.Versions()
	active := make([]PromptVersion, 0, len(versions))
	for _, version := range versions {
		if !version.Archived {
			active = append(active, version)
		}
	}
	return active
}

func CanonicalSHA256(definition PromptVersion) (string, error) {
	definition.Identity = strings.TrimSpace(definition.Identity)
	definition.Contract = strings.TrimSpace(definition.Contract)
	definition.Content = canonicalContent(definition.Content)
	if err := validate(definition); err != nil {
		return "", err
	}
	canonical := fmt.Sprintf("identity:%s\nversion:%d\ncontract:%s\n\n%s", definition.Identity, definition.Version, definition.Contract, definition.Content)
	digest := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(digest[:]), nil
}

func parseMarkdown(path, payload string) (PromptVersion, error) {
	payload = strings.ReplaceAll(strings.ReplaceAll(payload, "\r\n", "\n"), "\r", "\n")
	if !strings.HasPrefix(payload, "---\n") {
		return PromptVersion{}, fmt.Errorf("prompt %s has invalid front matter", path)
	}
	parts := strings.SplitN(strings.TrimPrefix(payload, "---\n"), "\n---\n", 2)
	if len(parts) != 2 {
		return PromptVersion{}, fmt.Errorf("prompt %s has invalid front matter", path)
	}
	metadata := make(map[string]string, 3)
	for _, line := range strings.Split(parts[0], "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return PromptVersion{}, fmt.Errorf("prompt %s has invalid metadata", path)
		}
		key = strings.TrimSpace(key)
		if _, duplicate := metadata[key]; duplicate {
			return PromptVersion{}, fmt.Errorf("prompt %s has duplicate metadata %q", path, key)
		}
		metadata[key] = strings.TrimSpace(value)
	}
	version, err := strconv.Atoi(metadata["version"])
	if err != nil {
		return PromptVersion{}, fmt.Errorf("prompt %s has invalid version", path)
	}
	definition := PromptVersion{
		Identity: metadata["identity"], Version: version, Contract: metadata["contract"],
		Content: parts[1], SourcePath: path,
	}
	if len(metadata) != 3 {
		return PromptVersion{}, fmt.Errorf("prompt %s has unknown or duplicate metadata", path)
	}
	return definition, nil
}

func validate(definition PromptVersion) error {
	if !validIdentifier(definition.Identity) || definition.Version < 1 || !validIdentifier(definition.Contract) || strings.TrimSpace(definition.Content) == "" {
		return errors.New("invalid prompt version")
	}
	if definition.Identity == "latest" || definition.Contract == "latest" {
		return errors.New("mutable prompt selector is forbidden")
	}
	return nil
}

func validIdentifier(value string) bool {
	if value == "" || !strings.Contains(value, ".") {
		return false
	}
	for _, character := range value {
		if unicode.IsLower(character) || unicode.IsDigit(character) || character == '.' || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func canonicalContent(content string) string {
	content = strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\r", "\n")
	return strings.TrimSpace(content) + "\n"
}

func catalogKey(identity string, version int) string {
	return identity + "@" + strconv.Itoa(version)
}
