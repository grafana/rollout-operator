package k3t

import (
	"strings"
	"testing"

	"github.com/k3d-io/k3d/v5/pkg/logger"
	"github.com/sirupsen/logrus"
)

// ConfigureLogger configures k3d logger to use testing.T's Log method instead of stdout.
// This funciton is called from NewCluster, unless disabled by an option.
// Note that this configures a global config, so it's not safe to configure from tests running in parallel.
// Note that testing.T automatically logs the name of this file, hence the short but still descriptive filename.
func ConfigureLogger(t *testing.T) {
	logger.Logger.SetOutput(testingTWriter{t})
	logger.Logger.SetFormatter(&logrus.TextFormatter{
		DisableColors:   true,
		FullTimestamp:   false,
		TimestampFormat: "15:04:05.000", // short, and equally useful for a test
	})
}

type testingTWriter struct{ t *testing.T }

func (tw testingTWriter) Write(p []byte) (n int, err error) {
	tw.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}
