package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildChangelog_ReplaceExistingEntry(t *testing.T) {
	changelogLines := []string{
		"# Changelog",
		"",
		"## main / unreleased",
		"",
		"* [FEATURE] feature",
		"* [ENHANCEMENT] Updated dependencies, including: #123",
		"  * `github.com/example/foo` from `v1.0.0` to `v1.1.0`",
		"  * `github.com/example/bar` from `v2.0.0` to `v2.1.0`",
		"* [ENHANCEMENT] enhancement",
		"",
		"## v1.0.0",
		"",
		"* Initial release",
	}

	newEntry := []string{
		"* [ENHANCEMENT] Updated dependencies, including: #456",
		"  * `github.com/example/foo` from `v1.0.0` to `v2.0.0`",
		"  * `github.com/example/bar` from `v2.1.0` to `v2.2.0`",
		"  * `github.com/example/baz` from `v3.0.0` to `v4.0.0`",
	}

	expectedLines := []string{
		"# Changelog",
		"",
		"## main / unreleased",
		"",
		"* [FEATURE] feature",
		"* [ENHANCEMENT] Updated dependencies, including: #123 #456",
		"  * `github.com/example/foo` from `v1.0.0` to `v2.0.0`",
		"  * `github.com/example/bar` from `v2.1.0` to `v2.2.0`",
		"  * `github.com/example/baz` from `v3.0.0` to `v4.0.0`",
		"* [ENHANCEMENT] enhancement",
		"",
		"## v1.0.0",
		"",
		"* Initial release",
	}

	actual, err := buildChangelog(changelogLines, newEntry, " #456")
	require.NoError(t, err)

	expected := strings.Join(expectedLines, "\n") + "\n"
	require.Equal(t, expected, string(actual))
}

func TestBuildChangelog_NoChanges(t *testing.T) {
	changelogLines := []string{
		"# Changelog",
		"",
		"## main / unreleased",
		"",
		"* [FEATURE] feature",
		"* [ENHANCEMENT] Updated dependencies, including: #123",
		"  * `github.com/example/foo` from `v1.0.0` to `v1.1.0`",
		"  * `github.com/example/bar` from `v2.0.0` to `v2.1.0`",
		"* [ENHANCEMENT] enhancement",
		"",
		"## v1.0.0",
		"",
		"* Initial release",
	}

	newEntry := []string{
		"* [ENHANCEMENT] Updated dependencies, including: #124",
		"  * `github.com/example/foo` from `v1.0.0` to `v1.1.0`",
		"  * `github.com/example/bar` from `v2.0.0` to `v2.1.0`",
	}

	actual, err := buildChangelog(changelogLines, newEntry, " #124")
	require.NoError(t, err)
	require.Nil(t, actual)
}

func TestBuildChangelog_InsertNewEntry(t *testing.T) {
	changelogLines := []string{
		"# Changelog",
		"",
		"## main / unreleased",
		"",
		"## v1.0.0",
		"",
		"* Initial release",
	}

	newEntry := []string{
		"* [ENHANCEMENT] Updated dependencies, including: #123",
		"  * `github.com/example/pkg` from `v1.0.0` to `v2.0.0`",
		"  * `github.com/another/lib` from `v3.0.0` to `v3.1.0`",
	}

	expectedLines := []string{
		"# Changelog",
		"",
		"## main / unreleased",
		"",
		"* [ENHANCEMENT] Updated dependencies, including: #123",
		"  * `github.com/example/pkg` from `v1.0.0` to `v2.0.0`",
		"  * `github.com/another/lib` from `v3.0.0` to `v3.1.0`",
		"",
		"## v1.0.0",
		"",
		"* Initial release",
	}

	actual, err := buildChangelog(changelogLines, newEntry, " #123")
	require.NoError(t, err)

	expected := strings.Join(expectedLines, "\n") + "\n"
	require.Equal(t, expected, string(actual))
}
