package service

import (
	"context"
	"errors"
	"testing"

	"project/internal/model"

	"github.com/stretchr/testify/require"
)

func TestAutomateStatusConditionRejectsNonStringValue(t *testing.T) {
	deviceName := "socket"
	triggerSource := "config-1"
	triggerType := model.TRIGGER_PARAM_TYPE_STATUS
	triggerParam := "ON-LINE"
	automate := &Automate{
		device: &model.Device{Name: &deviceName},
		formExt: AutomateFromExt{TriggerValues: map[string]interface{}{
			"login": 1,
		}},
	}

	ok, _, err := automate.automateConditionCheckWithDevice(model.DeviceTriggerCondition{
		TriggerConditionType: model.DEVICE_TRIGGER_CONDITION_TYPE_MULTIPLE,
		TriggerSource:        &triggerSource,
		TriggerParamType:     &triggerType,
		TriggerParam:         &triggerParam,
	}, "device-1")

	require.False(t, ok)
	require.ErrorContains(t, err, "has type int, want string")
}

func TestAutomateTimeConditionReturnsMalformedValueError(t *testing.T) {
	_, _, err := (&Automate{}).AutomateConditionCheckWithGroupOne(model.DeviceTriggerCondition{
		TriggerConditionType: model.DEVICE_TRIGGER_CONDITION_TYPE_TIME,
		TriggerValue:         "x|not-a-time|also-not-a-time",
	}, "device-1")

	require.ErrorContains(t, err, "invalid weekday")
}

func TestSceneAutomateExecuteReturnsActionAndLogErrors(t *testing.T) {
	actionErr := errors.New("command failed")
	logErr := errors.New("log insert failed")
	automate := &Automate{
		sceneAutomationTenant: func(context.Context, string) (string, error) {
			return "tenant-1", nil
		},
		actionExecute: func(executionKey string, _ []string, _ []model.ActionInfo, tenantID string) (string, error) {
			require.Equal(t, "execution-1", executionKey)
			require.Equal(t, "tenant-1", tenantID)
			return "command detail", actionErr
		},
		sceneExecutionLogSave: func(sceneID, tenantID, details string, executeErr error) error {
			require.Equal(t, "scene-1", sceneID)
			require.Equal(t, "tenant-1", tenantID)
			require.Equal(t, "command detail", details)
			require.ErrorIs(t, executeErr, actionErr)
			return logErr
		},
	}

	err := automate.SceneAutomateExecuteWithKey("scene-1", nil, nil, "execution-1")

	require.ErrorIs(t, err, actionErr)
	require.ErrorIs(t, err, logErr)
}

func TestSceneAutomateExecuteStopsOnTenantQueryFailure(t *testing.T) {
	tenantErr := errors.New("tenant query failed")
	actionCalled := false
	logCalled := false
	automate := &Automate{
		sceneAutomationTenant: func(context.Context, string) (string, error) {
			return "", tenantErr
		},
		actionExecute: func(string, []string, []model.ActionInfo, string) (string, error) {
			actionCalled = true
			return "", nil
		},
		sceneExecutionLogSave: func(string, string, string, error) error {
			logCalled = true
			return nil
		},
	}

	err := automate.SceneAutomateExecute("scene-1", nil, nil)

	require.ErrorIs(t, err, tenantErr)
	require.False(t, actionCalled)
	require.False(t, logCalled)
}

func TestActionDecorationPanicIsReturned(t *testing.T) {
	previous := actionAfterDecoration
	actionAfterDecoration = []ActionAfterFunc{
		func([]model.ActionInfo, string, error) error {
			panic("broken decoration")
		},
	}
	t.Cleanup(func() { actionAfterDecoration = previous })

	err := (&Automate{}).actionAfterDecorationRun(nil, "device-1", nil)

	require.ErrorContains(t, err, "action decoration panic: broken decoration")
}
