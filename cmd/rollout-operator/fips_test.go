package main

import (
	"crypto/fips140"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFIPSMode(t *testing.T) {
	if os.Getenv("ROLLOUT_OPERATOR_FIPS_PROBE") == "1" {
		fmt.Printf("FIPS enabled: %t\n", fips140.Enabled())
		return
	}

	for _, tc := range []struct {
		name    string
		godebug string
		enabled bool
	}{
		{name: "default"},
		{name: "enabled", godebug: "fips140=on", enabled: true},
		{name: "disabled", godebug: "fips140=off"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// FIPS mode is fixed at process startup, so each setting needs a fresh process.
			cmd := exec.Command(os.Args[0], "-test.run=^TestFIPSMode$")
			for _, env := range os.Environ() {
				if !strings.HasPrefix(env, "GODEBUG=") && !strings.HasPrefix(env, "ROLLOUT_OPERATOR_FIPS_PROBE=") {
					cmd.Env = append(cmd.Env, env)
				}
			}
			cmd.Env = append(cmd.Env, "ROLLOUT_OPERATOR_FIPS_PROBE=1")
			if tc.godebug != "" {
				cmd.Env = append(cmd.Env, "GODEBUG="+tc.godebug)
			}
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", output)
			require.Contains(t, string(output), fmt.Sprintf("FIPS enabled: %t", tc.enabled))
		})
	}
}
