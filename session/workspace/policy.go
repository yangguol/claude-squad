package workspace

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const policyFileName = ".claude-squad-ignore"

type EntryClass string

const (
	ClassIncluded  EntryClass = "included"
	ClassExcluded  EntryClass = "excluded"
	ClassShared    EntryClass = "shared"
	ClassGenerated EntryClass = "generated"
)

type policyRule struct {
	pattern string
	class   EntryClass
	negate  bool
}

type Policy struct {
	rules []policyRule
}

var defaultPolicyRules = []policyRule{
	{pattern: "node_modules/", class: ClassShared},
	{pattern: ".venv/", class: ClassShared},
	{pattern: "venv/", class: ClassShared},
	{pattern: "dist/", class: ClassGenerated},
	{pattern: "build/", class: ClassGenerated},
	{pattern: "target/", class: ClassGenerated},
	{pattern: "out/", class: ClassGenerated},
	{pattern: ".cache/", class: ClassGenerated},
	{pattern: ".pytest_cache/", class: ClassGenerated},
	{pattern: ".mypy_cache/", class: ClassGenerated},
	{pattern: ".ruff_cache/", class: ClassGenerated},
	{pattern: "__pycache__/", class: ClassGenerated},
	{pattern: ".idea/", class: ClassGenerated},
	{pattern: ".vscode/", class: ClassGenerated},
	{pattern: ".DS_Store", class: ClassGenerated},
	{pattern: "Thumbs.db", class: ClassGenerated},
}

func LoadPolicy(source string) (Policy, error) {
	policy := Policy{rules: append([]policyRule(nil), defaultPolicyRules...)}
	file, err := os.Open(filepath.Join(source, policyFileName))
	if errors.Is(err, os.ErrNotExist) {
		return policy, nil
	}
	if err != nil {
		return Policy{}, fmt.Errorf("open %s: %w", policyFileName, err)
	}
	defer file.Close()

	class := ClassExcluded
	scanner := bufio.NewScanner(file)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") || strings.HasSuffix(line, "]") {
			switch line {
			case "[shared]":
				class = ClassShared
			case "[generated]":
				class = ClassGenerated
			default:
				return Policy{}, fmt.Errorf("%s line %d: unsupported section %q", policyFileName, lineNumber, line)
			}
			continue
		}

		negate := strings.HasPrefix(line, "!")
		pattern := strings.TrimPrefix(line, "!")
		if pattern == "" {
			return Policy{}, fmt.Errorf("%s line %d: empty pattern", policyFileName, lineNumber)
		}
		if _, err := path.Match(strings.Trim(pattern, "/"), "validate"); err != nil {
			return Policy{}, fmt.Errorf("%s line %d: invalid pattern %q: %w", policyFileName, lineNumber, pattern, err)
		}
		policy.rules = append(policy.rules, policyRule{pattern: pattern, class: class, negate: negate})
	}
	if err := scanner.Err(); err != nil {
		return Policy{}, fmt.Errorf("read %s: %w", policyFileName, err)
	}
	return policy, nil
}

func (p Policy) Classify(name string) EntryClass {
	name = strings.Trim(strings.ReplaceAll(name, "\\", "/"), "/")
	class := ClassIncluded
	for _, rule := range p.rules {
		if rule.matches(name) {
			if rule.negate {
				class = ClassIncluded
			} else {
				class = rule.class
			}
		}
	}
	return class
}

func (r policyRule) matches(name string) bool {
	pattern := strings.Trim(r.pattern, "/")
	if pattern == "" || name == "" {
		return false
	}

	rooted := strings.HasPrefix(r.pattern, "/") || strings.Contains(pattern, "/")
	if rooted {
		return matchPathOrParent(pattern, name)
	}

	parts := strings.Split(name, "/")
	for _, part := range parts {
		matched, _ := path.Match(pattern, part)
		if matched {
			return true
		}
	}
	return false
}

func matchPathOrParent(pattern, name string) bool {
	for candidate := name; candidate != ""; {
		matched, _ := path.Match(pattern, candidate)
		if matched {
			return true
		}
		index := strings.LastIndexByte(candidate, '/')
		if index < 0 {
			break
		}
		candidate = candidate[:index]
	}
	return false
}
