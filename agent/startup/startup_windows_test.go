//go:build windows

package startup

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	ole "github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
	"github.com/google/uuid"
	"golang.org/x/sys/windows"
)

type memoryAutoStartStore struct {
	state autoStartState
	fail  string
	calls []string
}

func (s *memoryAutoStartStore) read() (autoStartState, error) { return s.state, nil }
func (s *memoryAutoStartStore) operation(name string) error {
	s.calls = append(s.calls, name)
	if s.fail == name {
		s.fail = ""
		return errors.New("simulated " + name + " failure")
	}
	return nil
}
func (s *memoryAutoStartStore) writeCommand(command string) error {
	operation := "writeRun"
	if command == "" {
		operation = "deleteRun"
	}
	if err := s.operation(operation); err != nil {
		return err
	}
	s.state.command = command
	return nil
}
func (s *memoryAutoStartStore) writeTask(definition string) error {
	if err := s.operation("writeTask"); err != nil {
		return err
	}
	s.state.taskXML = definition
	return nil
}
func (s *memoryAutoStartStore) deleteTask() error {
	if err := s.operation("deleteTask"); err != nil {
		return err
	}
	s.state.taskXML = ""
	return nil
}

func testAutoStartState(t *testing.T, enabled, elevated bool) autoStartState {
	t.Helper()
	state, err := desiredAutoStart(`C:\应用 & tools\relay-agent-gui.exe`, `D:\配置文件\agent settings.yaml`, "S-1-5-21-111-222-333-1001", enabled, elevated)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestElevatedAutoStartUsesInteractiveCurrentUser(t *testing.T) {
	state := testAutoStartState(t, true, true)
	definition, err := parseTaskDefinition(state.taskXML)
	if err != nil {
		t.Fatal(err)
	}
	if len(definition.Principals.Principal) != 1 || len(definition.Triggers.Logon) != 1 || len(definition.Actions.Exec) != 1 {
		t.Fatal("task must have one logon trigger, user and executable")
	}
	principal, trigger, action := definition.Principals.Principal[0], definition.Triggers.Logon[0], definition.Actions.Exec[0]
	if principal.UserID != trigger.UserID || principal.UserID != "S-1-5-21-111-222-333-1001" ||
		principal.LogonType != "InteractiveToken" || principal.RunLevel != "HighestAvailable" || !trigger.Enabled {
		t.Fatal("task would not start with the current user's elevated interactive token")
	}
	if action.Command != `C:\应用 & tools\relay-agent-gui.exe` || action.WorkingDirectory != `C:\应用 & tools` ||
		action.Arguments != `--gui --minimized --config "D:\配置文件\agent settings.yaml"` {
		t.Fatalf("paths/arguments changed: %+v", action)
	}
	if !definition.Settings.Enabled || definition.Settings.ExecutionTimeLimit != "PT0S" ||
		definition.Settings.DisallowStartIfOnBatteries || definition.Settings.StopIfGoingOnBatteries ||
		definition.Settings.MultipleInstancesPolicy != "IgnoreNew" || definition.Settings.RestartOnFailure.Count == 0 {
		t.Fatal("task would stop a long-running proxy or fail to retry startup")
	}
	if autoStartCommand(state) != autoStartCommand(testAutoStartState(t, true, false)) {
		t.Fatal("elevated and ordinary startup lost executable/configuration arguments")
	}
}

func TestAutoStartMigrationRequiresElevationAndKeepsOneEntry(t *testing.T) {
	ordinary, elevated := testAutoStartState(t, true, false), testAutoStartState(t, true, true)
	store := &memoryAutoStartStore{state: ordinary}
	if _, err := applyAutoStart(store, ordinary, elevated, false); err == nil || len(store.calls) != 0 {
		t.Fatal("unprivileged migration changed login entries")
	}
	rollback, err := applyAutoStart(store, ordinary, elevated, true)
	if err != nil || rollback == nil || !sameAutoStart(store.state, elevated) {
		t.Fatalf("migration failed: %+v %v", store.state, err)
	}
	if !reflect.DeepEqual(store.calls, []string{"writeTask", "deleteRun"}) {
		t.Fatalf("old startup was removed before its replacement existed: %v", store.calls)
	}
	if err := rollback(); err != nil || !sameAutoStart(store.state, ordinary) {
		t.Fatalf("rollback did not restore original command: %v", err)
	}
}

func TestAutoStartDowngradeAndDisableRemoveElevatedTask(t *testing.T) {
	elevated := testAutoStartState(t, true, true)
	for _, enabled := range []bool{true, false} {
		store := &memoryAutoStartStore{state: elevated}
		desired := testAutoStartState(t, enabled, false)
		if _, err := applyAutoStart(store, elevated, desired, true); err != nil || !sameAutoStart(store.state, desired) {
			t.Fatalf("elevated task survived mode change/disable: %v", err)
		}
	}
}

func TestAutoStartFailuresPreservePreviousRegistration(t *testing.T) {
	ordinary, elevated := testAutoStartState(t, true, false), testAutoStartState(t, true, true)
	for _, tc := range []struct {
		operation       string
		before, desired autoStartState
	}{
		{"writeTask", ordinary, elevated},
		{"deleteRun", ordinary, elevated},
		{"deleteTask", elevated, ordinary},
	} {
		t.Run(tc.operation, func(t *testing.T) {
			store := &memoryAutoStartStore{state: tc.before, fail: tc.operation}
			if _, err := applyAutoStart(store, tc.before, tc.desired, true); err == nil {
				t.Fatal("startup update failure was swallowed")
			}
			if !sameAutoStart(store.state, tc.before) {
				t.Fatal("failed migration changed the previous registration")
			}
		})
	}
}

func TestMatchingAutoStartNeedsNoPrivilegeOrMutation(t *testing.T) {
	state := testAutoStartState(t, true, true)
	store := &memoryAutoStartStore{state: state}
	rollback, err := applyAutoStart(store, state, state, false)
	if err != nil || rollback != nil || len(store.calls) != 0 {
		t.Fatalf("matching task was rewritten: %v", err)
	}
	state.taskXML = strings.Replace(state.taskXML, "<Enabled>true</Enabled>", "<Enabled>false</Enabled>", 1)
	if autoStartCommand(state) != "" {
		t.Fatal("disabled scheduled task was advertised as enabled")
	}
}

// This uses an unregistered ITaskDefinition. It validates the real Windows XML
// parser and canonicalization without creating a task or requiring elevation.
func TestTaskSchedulerAcceptsDefinitionWithoutRegistering(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	state, err := desiredAutoStart(`C:\应用 & tools\relay-agent-gui.exe`, `D:\配置文件\agent.yaml`, user.User.Sid.String(), true, true)
	if err != nil {
		t.Fatal(err)
	}
	validationName := "RelayProxy-Validate-" + uuid.NewString()
	err = withTaskScheduler(func(service, folder *ole.IDispatch) error {
		definition, err := oleutil.CallMethod(service, "NewTask", 0)
		if definition != nil {
			defer definition.Clear()
		}
		if err != nil {
			return err
		}
		result, err := oleutil.PutProperty(definition.ToIDispatch(), "XmlText", state.taskXML)
		if result != nil {
			defer result.Clear()
		}
		if err != nil {
			return err
		}
		canonical, err := oleutil.GetProperty(definition.ToIDispatch(), "XmlText")
		if canonical != nil {
			defer canonical.Clear()
		}
		if err != nil {
			return err
		}
		if !sameTaskDefinition(canonical.ToString(), state.taskXML) {
			t.Errorf("native task normalization changed startup semantics:\n%s", canonical.ToString())
		}
		// TASK_VALIDATE_ONLY performs the actual registration API's validation
		// without adding/changing a task, even when tests run as administrator.
		return registerLogonTask(folder, validationName, user.User.Sid.String(), state.taskXML, taskValidateOnly)
	})
	if err != nil {
		t.Fatalf("Task Scheduler rejected task definition: %v (HRESULT %#x)", err, oleErrorCode(err))
	}
	if registered, err := readLogonTask(validationName); err != nil || registered != "" {
		t.Fatalf("validation-only call unexpectedly registered a task: %v", err)
	}
}

func TestMissingScheduledStartupIsReadOnlyAndNotEnabled(t *testing.T) {
	store, err := newWindowsAutoStartStore("RelayProxy-Test-" + uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.read()
	if err != nil || state != (autoStartState{}) {
		t.Fatalf("missing startup query failed: %+v %v (HRESULT %#x)", state, err, oleErrorCode(err))
	}
}
