package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"gopkg.in/yaml.v3"
)

func formatTimestamp(seconds float64) string {
	totalSeconds := int(seconds)
	hours := totalSeconds / 3600
	minutes := (totalSeconds % 3600) / 60
	secs := totalSeconds % 60

	if hours > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, secs)
	}
	return fmt.Sprintf("%02d:%02d", minutes, secs)
}

// extractTagsFromObsidian scans the Obsidian vault and extracts all unique tags
// Returns a map of tag -> count
func extractTagsFromObsidian(vaultPath string) (map[string]int, error) {
	tagCounts := make(map[string]int)
	md := goldmark.New()

	err := filepath.Walk(vaultPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip directories and non-markdown files
		if info.IsDir() || !strings.HasSuffix(info.Name(), ".md") {
			return nil
		}

		// Skip transcript files (they don't have frontmatter tags)
		if strings.HasSuffix(info.Name(), "-transcript.md") {
			return nil
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		// Extract frontmatter tags
		tags := extractFrontmatterTags(content)
		for _, tag := range tags {
			tagCounts[tag]++
		}

		// Extract inline hashtags from markdown content (excluding frontmatter)
		bodyContent := stripFrontmatter(content)

		// Parse markdown to AST
		doc := md.Parser().Parse(text.NewReader(bodyContent))

		// Walk the AST and extract hashtags from text nodes
		_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
			if !entering {
				return ast.WalkContinue, nil
			}

			// Only process text nodes (not links, code blocks, etc.)
			if textNode, ok := n.(*ast.Text); ok {
				segment := textNode.Segment
				textContent := string(segment.Value(bodyContent))

				// Find hashtags in this text segment
				tags := extractHashtags(textContent)
				for _, tag := range tags {
					tagCounts[tag]++
				}
			}

			return ast.WalkContinue, nil
		})

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("error scanning vault: %w", err)
	}

	return tagCounts, nil
}

// stripFrontmatter removes YAML frontmatter from markdown content
func stripFrontmatter(content []byte) []byte {
	lines := bytes.Split(content, []byte("\n"))
	if len(lines) < 3 || !bytes.Equal(bytes.TrimSpace(lines[0]), []byte("---")) {
		return content
	}

	// Find the closing ---
	for i := 1; i < len(lines); i++ {
		if bytes.Equal(bytes.TrimSpace(lines[i]), []byte("---")) {
			// Return everything after the frontmatter
			return bytes.Join(lines[i+1:], []byte("\n"))
		}
	}

	return content
}

// extractFrontmatterTags parses YAML frontmatter and extracts tags
func extractFrontmatterTags(content []byte) []string {
	var tags []string

	lines := bytes.Split(content, []byte("\n"))
	if len(lines) < 3 || !bytes.Equal(bytes.TrimSpace(lines[0]), []byte("---")) {
		return tags
	}

	// Find frontmatter boundaries
	var frontmatterLines [][]byte
	for i := 1; i < len(lines); i++ {
		if bytes.Equal(bytes.TrimSpace(lines[i]), []byte("---")) {
			frontmatterLines = lines[1:i]
			break
		}
	}

	if len(frontmatterLines) == 0 {
		return tags
	}

	// Parse YAML
	frontmatterYAML := bytes.Join(frontmatterLines, []byte("\n"))
	var frontmatter struct {
		Tags interface{} `yaml:"tags"`
	}

	if err := yaml.Unmarshal(frontmatterYAML, &frontmatter); err != nil {
		return tags
	}

	// Handle both array and string formats
	switch v := frontmatter.Tags.(type) {
	case []interface{}:
		for _, tag := range v {
			if tagStr, ok := tag.(string); ok {
				tags = append(tags, tagStr)
			}
		}
	case string:
		// Single tag as string
		tags = append(tags, v)
	}

	return tags
}

// extractHashtags extracts hashtags from a text string, excluding those in URLs/links
func extractHashtags(text string) []string {
	var tags []string

	// Pattern for hashtags: # followed by word chars and hyphens
	// But exclude if preceded by ( or [ (common in markdown links/anchors)
	hashtagRegex := regexp.MustCompile(`(?:^|[^(\[])#([\w-]+)`)

	matches := hashtagRegex.FindAllStringSubmatch(text, -1)
	for _, match := range matches {
		if len(match) > 1 && match[1] != "" {
			tags = append(tags, match[1])
		}
	}

	return tags
}

// runExtractTags extracts all tags from the Obsidian vault and writes them to a file
func runExtractTags(vaultPath string) error {
	fmt.Println("\n=== Extracting tags from Obsidian vault ===")
	fmt.Printf("Scanning vault: %s\n", vaultPath)

	tagCounts, err := extractTagsFromObsidian(vaultPath)
	if err != nil {
		return err
	}

	if len(tagCounts) == 0 {
		fmt.Println("⚠ No tags found in vault")
		return nil
	}

	// Convert to sorted list
	type tagInfo struct {
		Tag   string `json:"tag"`
		Count int    `json:"count"`
	}

	var tags []tagInfo
	for tag, count := range tagCounts {
		tags = append(tags, tagInfo{Tag: tag, Count: count})
	}

	// Sort by count (descending), then alphabetically
	sort.Slice(tags, func(i, j int) bool {
		if tags[i].Count != tags[j].Count {
			return tags[i].Count > tags[j].Count
		}
		return tags[i].Tag < tags[j].Tag
	})

	// Write to obsidian-tags.json
	data, err := json.MarshalIndent(tags, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal tags: %w", err)
	}

	if err := os.WriteFile("obsidian-tags.json", data, 0644); err != nil {
		return fmt.Errorf("failed to write obsidian-tags.json: %w", err)
	}

	fmt.Printf("\n✅ Extracted %d unique tags from vault\n", len(tags))
	fmt.Printf("📝 Saved to obsidian-tags.json\n")
	fmt.Printf("\nTop 10 tags:\n")
	for i := 0; i < 10 && i < len(tags); i++ {
		fmt.Printf("  %2d. %-30s (used %d times)\n", i+1, tags[i].Tag, tags[i].Count)
	}

	return nil
}

// loadObsidianTags loads tags from obsidian-tags.json
func loadObsidianTags() ([]string, error) {
	data, err := os.ReadFile("obsidian-tags.json")
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No tags file yet
		}
		return nil, fmt.Errorf("failed to read obsidian-tags.json: %w", err)
	}

	var tags []struct {
		Tag   string `json:"tag"`
		Count int    `json:"count"`
	}

	if err := json.Unmarshal(data, &tags); err != nil {
		return nil, fmt.Errorf("failed to parse obsidian-tags.json: %w", err)
	}

	// Extract just the tag names
	tagNames := make([]string, 0, len(tags))
	for _, t := range tags {
		tagNames = append(tagNames, t.Tag)
	}

	return tagNames, nil
}
