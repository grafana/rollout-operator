package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"golang.org/x/mod/modfile"
)

func main() {
	var pullRequestNum int
	flag.IntVar(&pullRequestNum, "pull-request-number", 0, "The pull request number of the most recent go dependency update")
	flag.Parse()

	appendPullRequestNum := ""
	if pullRequestNum > 0 {
		appendPullRequestNum = fmt.Sprintf(" #%d", pullRequestNum)
	}

	currentBytes, err := os.ReadFile("go.mod")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading go.mod: %v\n", err)
		os.Exit(1)
	}

	current, err := modfile.Parse("go.mod", currentBytes, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing current go.mod: %v\n", err)
		os.Exit(1)
	}

	previousBytes, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading previous go.mod from stdin: %v\n", err)
		os.Exit(1)
	}

	previous, err := modfile.Parse("go.mod", previousBytes, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing previous go.mod from stdin: %v\n", err)
		os.Exit(1)
	}

	// Build map of direct dependencies from previous release
	versions := make(map[string]string)
	for _, req := range previous.Require {
		if req.Indirect {
			continue
		}
		versions[req.Mod.Path] = req.Mod.Version
	}

	// Find updates
	var changes []string
	for _, req := range current.Require {
		if req.Indirect {
			continue
		}
		if oldVer, exists := versions[req.Mod.Path]; exists && oldVer != req.Mod.Version {
			changes = append(changes, fmt.Sprintf("  * `%s` from `%s` to `%s`",
				req.Mod.Path, oldVer, req.Mod.Version))
		}
	}

	if len(changes) == 0 {
		return
	}

	// Sort changes for consistent output
	sort.Strings(changes)

	newEntry := []string{"* [ENHANCEMENT] Updated dependencies, including:" + appendPullRequestNum}
	newEntry = append(newEntry, changes...)

	changelogLines, err := readChangelog()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading changelog: %v\n", err)
		os.Exit(1)
	}

	content, err := buildChangelog(changelogLines, newEntry, appendPullRequestNum)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error building changelog: %v\n", err)
		os.Exit(1)
	}

	err = os.WriteFile("CHANGELOG.md", content, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error writing changelog: %v\n", err)
		os.Exit(1)
	}
}

func readChangelog() ([]string, error) {
	file, err := os.Open("CHANGELOG.md")
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return lines, nil
}

func buildChangelog(changelogLines []string, newEntry []string, appendPullRequestNum string) ([]byte, error) {
	// Find main section and any existing deps entry
	mainIdx := -1
	mainEndIdx := -1
	depsStartIdx := -1
	depsEndIdx := -1

	for i, line := range changelogLines {
		if strings.HasPrefix(line, "## main / unreleased") {
			mainIdx = i
			continue
		}
		if mainIdx >= 0 && mainEndIdx == -1 && strings.HasPrefix(line, "* [ENHANCEMENT] Updated dependencies") {
			depsStartIdx = i
			continue
		}
		if depsStartIdx >= 0 && depsEndIdx == -1 && !strings.HasPrefix(line, "  *") {
			// depsEndIdx might be the same as mainEndIdx if there is no padding blank line, so there is no continue here
			depsEndIdx = i - 1
		}
		if mainIdx >= 0 && mainEndIdx == -1 && strings.HasPrefix(line, "##") {
			mainEndIdx = i
			break
		}
	}

	if mainIdx == -1 || mainEndIdx == -1 {
		return nil, fmt.Errorf("could not find main section")
	}
	if depsStartIdx >= 0 && depsEndIdx == -1 {
		return nil, fmt.Errorf("could not find end of existing dependency entry")
	}

	var result []string

	if depsStartIdx >= 0 {
		// If there was an existing entry append the new PR number to it
		// The one exception here is if that pull request number already existed (protects against reruns)
		if !strings.HasSuffix(changelogLines[depsStartIdx], appendPullRequestNum) {
			newEntry[0] = changelogLines[depsStartIdx] + appendPullRequestNum
		}
		// Replace existing entry to reduce diff
		result = append(result, changelogLines[:depsStartIdx]...)
		result = append(result, newEntry...)
		result = append(result, changelogLines[depsEndIdx+1:]...)
	} else {
		// Insert new entry at the end of main section header (ENHANCEMENT ordering)
		result = append(result, changelogLines[:mainIdx+1]...)
		result = append(result, "") // ensure a padding blank line at the start of the section
		for i := mainIdx + 1; i < mainEndIdx; i++ {
			if changelogLines[i] != "" { // skip over any other blank lines if they existed
				result = append(result, changelogLines[i])
			}
		}
		result = append(result, newEntry...)
		result = append(result, "") // ensure a padding blank line at the end of the section
		result = append(result, changelogLines[mainEndIdx:]...)
	}

	return []byte(strings.Join(result, "\n") + "\n"), nil
}
