package croninit

import (
	"errors"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

type cronLogHook struct {
	entries []*logrus.Entry
}

func (h *cronLogHook) Levels() []logrus.Level {
	return logrus.AllLevels
}

func (h *cronLogHook) Fire(entry *logrus.Entry) error {
	h.entries = append(h.entries, entry)
	return nil
}

func TestRunAutomationCronTaskLogsReturnedErrorAtErrorLevel(t *testing.T) {
	logger := logrus.StandardLogger()
	previousHooks := logger.ReplaceHooks(make(logrus.LevelHooks))
	t.Cleanup(func() { logger.ReplaceHooks(previousHooks) })
	hook := &cronLogHook{}
	logger.AddHook(hook)
	taskErr := errors.New("task failed")

	runAutomationCronTask("test task", func() error { return taskErr })

	require.Len(t, hook.entries, 1)
	require.Equal(t, logrus.ErrorLevel, hook.entries[0].Level)
	require.ErrorIs(t, hook.entries[0].Data[logrus.ErrorKey].(error), taskErr)
}
