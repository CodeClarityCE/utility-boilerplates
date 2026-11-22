package boilerplates

import (
	"io"
	"testing"

	"github.com/sirupsen/logrus"
)

func TestASecondPluginBaseExportsTheSameHealthGauge(t *testing.T) {
	logger := logrus.New()
	logger.SetOutput(io.Discard)

	first := registerHealthGauge(logger)
	second := registerHealthGauge(logger)
	if first != second {
		t.Fatal("the second registration made a gauge that is never exported")
	}
}
