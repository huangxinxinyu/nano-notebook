package skillcatalog

import (
	"strings"
	"testing"
)

func TestEmbeddedCatalogContainsGrillMe(t *testing.T) {
	catalog, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	skill, ok := catalog.Resolve("skill.grill-me", 1)
	if !ok {
		t.Fatal("missing skill.grill-me@1")
	}
	if skill.Name != "Grill Me" || strings.TrimSpace(skill.Description) == "" || strings.TrimSpace(skill.Body) == "" {
		t.Fatalf("skill=%+v", skill)
	}
	if len(skill.SHA256) != 64 || skill.SourcePath != "skills/grill-me.v1.md" {
		t.Fatalf("hash/path=%q/%q", skill.SHA256, skill.SourcePath)
	}
}

func TestEmbeddedCatalogContainsResearchWorkflow(t *testing.T) {
	catalog := MustLoadEmbedded()
	skill, ok := catalog.Resolve("skill.research-workflow", 1)
	if !ok {
		t.Fatal("missing skill.research-workflow@1")
	}
	for _, required := range []string{"rewrite_todo_list", "update_todo_status", "Evidence", "TODO"} {
		if !strings.Contains(skill.Body, required) {
			t.Fatalf("Research workflow skill is missing %q", required)
		}
	}
}

func TestResearchWorkflowV2TeachesScopedInspectionSearch(t *testing.T) {
	skill, ok := MustLoadEmbedded().Resolve("skill.research-workflow", 2)
	if !ok {
		t.Fatal("missing skill.research-workflow@2")
	}
	for _, required := range []string{"inspect_source", "search_evidence", "source_id", "entry_id", "navigation only"} {
		if !strings.Contains(skill.Body, required) {
			t.Fatalf("Research workflow v2 is missing %q", required)
		}
	}
}

func TestCanonicalSHA256NormalizesSkillContent(t *testing.T) {
	left := SkillVersion{Identity: "skill.test", Version: 2, Name: "Test Skill", Description: "Useful test skill", Body: "alpha\r\nbeta"}
	right := SkillVersion{Identity: "skill.test", Version: 2, Name: "Test Skill", Description: "Useful test skill", Body: "alpha\nbeta\n"}
	leftHash, err := CanonicalSHA256(left)
	if err != nil {
		t.Fatal(err)
	}
	rightHash, err := CanonicalSHA256(right)
	if err != nil {
		t.Fatal(err)
	}
	if leftHash != rightHash {
		t.Fatalf("left=%s right=%s", leftHash, rightHash)
	}
}

func TestCatalogRejectsConflictingImmutableSkillVersion(t *testing.T) {
	_, err := New([]SkillVersion{
		{Identity: "skill.test", Version: 1, Name: "Test Skill", Description: "Useful test skill", Body: "alpha"},
		{Identity: "skill.test", Version: 1, Name: "Test Skill", Description: "Useful test skill", Body: "beta"},
	})
	if err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("err=%v", err)
	}
}

func TestSkillMarkdownRejectsUnknownOrDuplicateMetadata(t *testing.T) {
	for _, payload := range []string{
		"---\nidentity: skill.test\nidentity: skill.other\nversion: 1\nname: Test\ndescription: Test description\n---\nbody\n",
		"---\nidentity: skill.test\nversion: 1\nname: Test\ndescription: Test description\nunknown: value\n---\nbody\n",
	} {
		if _, err := parseMarkdown("skill.md", payload); err == nil {
			t.Fatalf("accepted metadata: %s", payload)
		}
	}
}
