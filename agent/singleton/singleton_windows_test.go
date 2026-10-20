//go:build windows

package singleton

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

type instanceHelperReady struct {
	PID        int
	Executable string
	Address    string
	Error      string
}

func TestWindowsInstanceLockRetainsOwnerAndRejectsSelfReplacement(t *testing.T) {
	name := fmt.Sprintf("RelayProxyAgent_test_%d_%d", os.Getpid(), time.Now().UnixNano())
	first, err := Acquire(name)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	runtime.GC()
	pid, err := mutantOwnerPID(first.handle)
	if err != nil || pid != uint32(os.Getpid()) {
		t.Fatalf("mutex owner=%d, err=%v", pid, err)
	}
	if second, err := Acquire(name); err == nil {
		second.Release()
		t.Fatal("duplicate acquisition succeeded")
	}
	if second, err := AcquireForStartup(name, nil); err == nil {
		second.Release()
		t.Fatal("process tried to replace itself")
	}
	first.Release()
	first.Release()
	again, err := Acquire(name)
	if err != nil {
		t.Fatalf("instance lock not released: %v", err)
	}
	again.Release()
}

func TestWindowsInstanceStartupKillsOldProcessBeforeOpeningPorts(t *testing.T) {
	for _, oldMode := range []string{"legacy", "current"} {
		t.Run(oldMode, func(t *testing.T) {
			name := fmt.Sprintf("RelayProxyAgent_test_%d_%d", os.Getpid(), time.Now().UnixNano())
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			// Distinct filenames/paths exercise manual update launches, including
			// clients renamed by the user. Discovery must use the original mutex.
			oldExecutable := copyInstanceTestExecutable(t, executable, "旧版客户端.exe")
			newExecutable := copyInstanceTestExecutable(t, executable, "新版客户端.exe")
			old, oldReady := startInstanceTestHelper(t, oldExecutable, oldMode, name, "127.0.0.1:0")
			_, current := startInstanceTestHelper(t, newExecutable, "replace", name, oldReady.Address)
			if current.PID == oldReady.PID || current.Executable != newExecutable || current.Address != oldReady.Address {
				t.Fatalf("replacement did not take over old port with the new executable: old=%+v current=%+v", oldReady, current)
			}
			select {
			case err := <-old:
				if err != nil {
					t.Fatalf("old process exit=%v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("replacement started before the old process exited")
			}
		})
	}
}

func TestWindowsInstanceStartupDoesNotKillNewerLaunch(t *testing.T) {
	name := fmt.Sprintf("RelayProxyAgent_test_%d_%d", os.Getpid(), time.Now().UnixNano())
	executable, _ := os.Executable()
	done, _ := startInstanceTestHelper(t, executable, "current", name, "127.0.0.1:0")
	if mutex, err := AcquireForStartup(name, nil); err == nil {
		mutex.Release()
		t.Fatal("an older launch replaced a newer client")
	}
	select {
	case <-done:
		t.Fatal("newer client was killed")
	default:
	}
}

func TestWindowsInstancePreparationFailurePreservesOldClientAndPort(t *testing.T) {
	for _, oldMode := range []string{"legacy", "current"} {
		t.Run(oldMode, func(t *testing.T) {
			name := fmt.Sprintf("RelayProxyAgent_test_%d_%d", os.Getpid(), time.Now().UnixNano())
			executable, _ := os.Executable()
			old, previous := startInstanceTestHelper(t, executable, oldMode, name, "127.0.0.1:0")
			startInstanceTestHelper(t, executable, "cancel-preparation", name, previous.Address)
			select {
			case <-old:
				t.Fatal("old process exited after preparation was cancelled")
			default:
			}
			connection, err := net.DialTimeout("tcp4", previous.Address, time.Second)
			if err != nil {
				t.Fatalf("old client stopped serving its port: %v", err)
			}
			connection.Close()
		})
	}
}

func TestWindowsInstanceReplacementWaitIsBounded(t *testing.T) {
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(event)
	if err := waitForReplacementObject(event, time.Now().Add(30*time.Millisecond)); err == nil {
		t.Fatal("unsignaled object bypassed replacement timeout")
	}
}

func TestWindowsInstanceElevationArgumentsKeepRequestIntact(t *testing.T) {
	request := replacementRequest{Name: AgentInstanceName, PID: 42, Requester: 100, Created: 1234}
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	argument := "--" + ReplacementHelperFlagName() + "=" + string(payload)
	args, err := windows.DecomposeCommandLine("helper.exe " + windows.EscapeArg(argument))
	if err != nil || len(args) != 2 || args[1] != argument {
		t.Fatalf("elevation parameters corrupted: %q, %v", args, err)
	}
}

func copyInstanceTestExecutable(t *testing.T, source, name string) string {
	t.Helper()
	in, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	path := filepath.Join(t.TempDir(), name)
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		t.Fatal(err)
	}
	return path
}

func startInstanceTestHelper(t *testing.T, executable, mode, name, address string) (<-chan error, instanceHelperReady) {
	t.Helper()
	command := exec.Command(executable, "-test.run=^TestWindowsInstanceHelperProcess$")
	command.Env = append(os.Environ(), "RELAYPROXY_INSTANCE_TEST_MODE="+mode, "RELAYPROXY_INSTANCE_TEST_NAME="+name, "RELAYPROXY_INSTANCE_TEST_ADDRESS="+address)
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done, waited := make(chan error, 1), make(chan struct{})
	go func() { done <- command.Wait(); close(waited) }()
	t.Cleanup(func() {
		_ = command.Process.Kill()
		select {
		case <-waited:
		case <-time.After(2 * time.Second):
			t.Error("instance test process did not exit")
		}
	})
	ready := make(chan instanceHelperReady, 1)
	go func() {
		var message instanceHelperReady
		if err := json.NewDecoder(bufio.NewReader(output)).Decode(&message); err != nil {
			message.Error = err.Error()
		}
		ready <- message
	}()
	select {
	case message := <-ready:
		if message.Error != "" {
			t.Fatalf("helper startup failed: %s", message.Error)
		}
		return done, message
	case <-time.After(15 * time.Second):
		t.Fatal("instance helper startup timed out")
		return nil, instanceHelperReady{}
	}
}

func TestWindowsInstanceHelperProcess(t *testing.T) {
	mode := os.Getenv("RELAYPROXY_INSTANCE_TEST_MODE")
	if mode == "" {
		return
	}
	name := os.Getenv("RELAYPROXY_INSTANCE_TEST_NAME")
	var err error
	switch mode {
	case "legacy":
		// Match releases predating takeover: no PID file or control endpoint.
		runtime.LockOSThread()
		namePtr, _ := windows.UTF16PtrFromString("Global\\" + name)
		_, err = windows.CreateMutex(nil, true, namePtr)
	case "current":
		_, err = Acquire(name)
	case "replace", "cancel-preparation":
		cancelled := errors.New("simulated UAC cancellation")
		_, err = AcquireForStartup(name, func(replacing bool) error {
			if !replacing {
				return errors.New("preparation did not detect the old client")
			}
			// The predecessor must still own its listener during permission
			// preparation, on successful replacement and on cancellation.
			connection, err := net.DialTimeout("tcp4", os.Getenv("RELAYPROXY_INSTANCE_TEST_ADDRESS"), time.Second)
			if err != nil {
				return fmt.Errorf("old listener closed before preparation: %w", err)
			}
			connection.Close()
			if mode == "cancel-preparation" {
				return cancelled
			}
			return nil
		})
		if mode == "cancel-preparation" && errors.Is(err, cancelled) {
			_ = json.NewEncoder(os.Stdout).Encode(instanceHelperReady{PID: os.Getpid()})
			return
		}
	default:
		err = fmt.Errorf("unknown helper mode %q", mode)
	}
	message := instanceHelperReady{PID: os.Getpid()}
	message.Executable, _ = os.Executable()
	var listener net.Listener
	if err == nil {
		listener, err = net.Listen("tcp4", os.Getenv("RELAYPROXY_INSTANCE_TEST_ADDRESS"))
	}
	if err != nil {
		message.Error = err.Error()
	} else {
		message.Address = listener.Addr().String()
	}
	_ = json.NewEncoder(os.Stdout).Encode(message)
	if err != nil {
		os.Exit(1)
	}
	defer listener.Close()
	time.Sleep(5 * time.Minute)
}
