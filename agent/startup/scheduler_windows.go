//go:build windows

package startup

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"

	ole "github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

const (
	taskNamespace             = "http://schemas.microsoft.com/windows/2004/02/mit/task"
	taskValidateOnly          = 1
	taskCreateOrUpdate        = 6
	taskLogonInteractiveToken = 3
)

type taskLogonTrigger struct {
	Enabled bool   `xml:"Enabled"`
	Delay   string `xml:"Delay"`
	UserID  string `xml:"UserId"`
}

type taskPrincipal struct {
	ID        string `xml:"id,attr"`
	UserID    string `xml:"UserId"`
	LogonType string `xml:"LogonType"`
	RunLevel  string `xml:"RunLevel"`
}

type taskAction struct {
	Command          string `xml:"Command"`
	Arguments        string `xml:"Arguments"`
	WorkingDirectory string `xml:"WorkingDirectory"`
}

type taskDefinition struct {
	XMLName   xml.Name `xml:"Task"`
	Version   string   `xml:"version,attr"`
	Namespace string   `xml:"xmlns,attr"`
	Triggers  struct {
		Logon []taskLogonTrigger `xml:"LogonTrigger"`
	} `xml:"Triggers"`
	Principals struct {
		Principal []taskPrincipal `xml:"Principal"`
	} `xml:"Principals"`
	Settings struct {
		MultipleInstancesPolicy    string `xml:"MultipleInstancesPolicy"`
		DisallowStartIfOnBatteries bool   `xml:"DisallowStartIfOnBatteries"`
		StopIfGoingOnBatteries     bool   `xml:"StopIfGoingOnBatteries"`
		StartWhenAvailable         bool   `xml:"StartWhenAvailable"`
		Enabled                    bool   `xml:"Enabled"`
		ExecutionTimeLimit         string `xml:"ExecutionTimeLimit"`
		RestartOnFailure           struct {
			Interval string `xml:"Interval"`
			Count    int    `xml:"Count"`
		} `xml:"RestartOnFailure"`
	} `xml:"Settings"`
	Actions struct {
		Context string       `xml:"Context,attr"`
		Exec    []taskAction `xml:"Exec"`
	} `xml:"Actions"`
}

func elevatedTaskXML(executable, arguments, userSID string) (string, error) {
	var task taskDefinition
	task.Version, task.Namespace = "1.2", taskNamespace
	task.Triggers.Logon = []taskLogonTrigger{{true, "PT10S", userSID}}
	task.Principals.Principal = []taskPrincipal{{"Agent", userSID, "InteractiveToken", "HighestAvailable"}}
	task.Settings.MultipleInstancesPolicy = "IgnoreNew"
	task.Settings.StartWhenAvailable, task.Settings.Enabled = true, true
	task.Settings.ExecutionTimeLimit = "PT0S"
	// Startup can fail transiently while DNS/the network comes up at logon.
	task.Settings.RestartOnFailure.Interval, task.Settings.RestartOnFailure.Count = "PT1M", 3
	task.Actions.Context = "Agent"
	task.Actions.Exec = []taskAction{{executable, arguments, filepath.Dir(executable)}}
	data, err := xml.MarshalIndent(task, "", "  ")
	return string(data), err
}

func parseTaskDefinition(definition string) (taskDefinition, error) {
	var task taskDefinition
	decoder := xml.NewDecoder(strings.NewReader(definition))
	// COM has already decoded the BSTR into Go UTF-8, even if the stored XML
	// still declares UTF-16. No disk bytes or external character sets are read.
	decoder.CharsetReader = func(charset string, input io.Reader) (io.Reader, error) {
		if strings.EqualFold(charset, "UTF-16") {
			return input, nil
		}
		return nil, fmt.Errorf("unsupported task XML character set: %s", charset)
	}
	err := decoder.Decode(&task)
	return task, err
}

func sameTaskDefinition(left, right string) bool {
	a, aErr := parseTaskDefinition(left)
	b, bErr := parseTaskDefinition(right)
	return aErr == nil && bErr == nil && reflect.DeepEqual(a, b)
}

// Task Scheduler COM avoids command shells, temporary privileged XML files,
// stored passwords and repeated console processes when displaying UI state.
func withTaskScheduler(fn func(service, folder *ole.IDispatch) error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED)
	switch oleErrorCode(err) {
	case 0, 1: // S_OK and S_FALSE both acquire a COM initialization reference.
		defer ole.CoUninitialize()
	case 0x80010106: // Already initialized in another apartment model.
	default:
		return err
	}
	object, err := oleutil.CreateObject("Schedule.Service")
	if err != nil {
		return err
	}
	defer object.Release()
	service, err := object.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return err
	}
	defer service.Release()
	connected, err := oleutil.CallMethod(service, "Connect")
	if connected != nil {
		defer connected.Clear()
	}
	if err != nil {
		return err
	}
	root, err := oleutil.CallMethod(service, "GetFolder", `\`)
	if root != nil {
		defer root.Clear()
	}
	if err != nil {
		return err
	}
	return fn(service, root.ToIDispatch())
}

func oleErrorCode(err error) uint32 {
	if err == nil {
		return 0
	}
	var native *ole.OleError
	if errors.As(err, &native) {
		if exception, ok := native.SubError().(interface{ SCODE() uint32 }); ok && exception.SCODE() != 0 {
			return exception.SCODE()
		}
		return uint32(native.Code())
	}
	return 0xffffffff
}

func readLogonTask(name string) (string, error) {
	var definition string
	err := withTaskScheduler(func(_ *ole.IDispatch, folder *ole.IDispatch) error {
		task, err := oleutil.CallMethod(folder, "GetTask", name)
		if task != nil {
			defer task.Clear()
		}
		if oleErrorCode(err) == 0x80070002 {
			return nil
		}
		if err != nil {
			return err
		}
		value, err := oleutil.GetProperty(task.ToIDispatch(), "Xml")
		if value != nil {
			defer value.Clear()
		}
		if err != nil {
			return err
		}
		definition = value.ToString()
		return nil
	})
	return definition, err
}

func writeLogonTask(name, userSID, definition string) error {
	return withTaskScheduler(func(_ *ole.IDispatch, folder *ole.IDispatch) error {
		return registerLogonTask(folder, name, userSID, definition, taskCreateOrUpdate)
	})
}

func registerLogonTask(folder *ole.IDispatch, name, userSID, definition string, flags int) error {
	task, err := oleutil.CallMethod(folder, "RegisterTask", name, definition, flags, userSID, nil, taskLogonInteractiveToken, nil)
	if task != nil {
		defer task.Clear()
	}
	return err
}

func deleteLogonTask(name string) error {
	return withTaskScheduler(func(_ *ole.IDispatch, folder *ole.IDispatch) error {
		result, err := oleutil.CallMethod(folder, "DeleteTask", name, 0)
		if result != nil {
			defer result.Clear()
		}
		if oleErrorCode(err) == 0x80070002 {
			return nil
		}
		return err
	})
}
