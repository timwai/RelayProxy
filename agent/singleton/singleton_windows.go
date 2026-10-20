//go:build windows

package singleton

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var queryMutant = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtQueryMutant")

type Mutex struct {
	handle  windows.Handle
	release chan struct{}
	done    chan struct{}
	once    sync.Once
}

// Acquire rejects duplicate instances without replacing the existing process.
func Acquire(name string) (*Mutex, error) { return acquire(name, false, 0, nil) }

// AcquireForStartup replaces the existing Windows Agent, including older
// releases which only created the named mutex and published no PID metadata.
// prepare runs before terminating any predecessor, with replacing indicating
// whether one exists. If preparation fails, the predecessor remains running.
func AcquireForStartup(name string, prepare func(replacing bool) error) (*Mutex, error) {
	return acquire(name, true, 10*time.Second, prepare)
}

func acquire(name string, replace bool, timeout time.Duration, prepare func(bool) error) (*Mutex, error) {
	type result struct {
		mutex *Mutex
		err   error
	}
	ready := make(chan result, 1)
	go func() {
		// Windows mutex ownership belongs to an OS thread. Keep that thread alive
		// until Release so later launches can identify the owning process.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		mutex, err := acquireOnThread(name, replace, timeout, prepare)
		ready <- result{mutex, err}
		if err != nil {
			return
		}
		<-mutex.release
		_ = windows.ReleaseMutex(mutex.handle)
		_ = windows.CloseHandle(mutex.handle)
		close(mutex.done)
	}()
	r := <-ready
	return r.mutex, r.err
}

func acquireOnThread(name string, replace bool, timeout time.Duration, prepare func(bool) error) (*Mutex, error) {
	namePtr, err := windows.UTF16PtrFromString("Global\\" + name)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateMutex(nil, true, namePtr)
	alreadyExists := errors.Is(err, windows.ERROR_ALREADY_EXISTS)
	needsElevation := replace && errors.Is(err, windows.ERROR_ACCESS_DENIED)
	if err != nil && !alreadyExists && !needsElevation {
		return nil, fmt.Errorf("创建客户端单实例锁失败: %w", err)
	}
	// Prepare services and obtain any required UAC consent while the old client
	// still owns its process and listeners, including when its mutex denies us
	// access. Never let an elevated replacement helper run before this step.
	if prepare != nil {
		if prepareErr := prepare(alreadyExists || needsElevation); prepareErr != nil {
			if handle != 0 {
				if !alreadyExists {
					_ = windows.ReleaseMutex(handle)
				}
				_ = windows.CloseHandle(handle)
			}
			return nil, prepareErr
		}
	}
	elevatedReplacement := false
	if needsElevation {
		// An elevated predecessor may also deny opening its mutex with the
		// all-access rights requested by CreateMutex. The helper handles this
		// case before the ordinary startup process retries acquiring the lock.
		if err := runElevatedReplacement(name, 0); err != nil {
			return nil, err
		}
		elevatedReplacement = true
		handle, err = windows.CreateMutex(nil, true, namePtr)
	}
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return nil, fmt.Errorf("创建客户端单实例锁失败: %w", err)
	}
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		if !replace {
			_ = windows.CloseHandle(handle)
			return nil, fmt.Errorf("another instance of %s is already running", name)
		}
		var replaceErr error
		if elevatedReplacement {
			replaceErr = waitForReplacementObject(handle, time.Now().Add(timeout))
		} else {
			replaceErr = replaceMutexOwner(name, handle, timeout)
		}
		if replaceErr != nil {
			_ = windows.CloseHandle(handle)
			return nil, replaceErr
		}
	}
	return &Mutex{handle: handle, release: make(chan struct{}), done: make(chan struct{})}, nil
}

// mutantOwnerPID uses MutantOwnerInformation (CLIENT_ID: process, thread).
// Querying the original mutex also works with old clients and renamed binaries;
// neither the Network Service nor unrelated processes are replacement targets.
// Native layout: https://github.com/winsiderss/phnt/blob/master/ntexapi.h
func mutantOwnerPID(handle windows.Handle) (uint32, error) {
	if err := queryMutant.Find(); err != nil {
		return 0, err
	}
	var owner struct{ process, thread uintptr }
	status, _, _ := queryMutant.Call(uintptr(handle), 1, uintptr(unsafe.Pointer(&owner)), unsafe.Sizeof(owner), 0)
	if int32(status) < 0 {
		return 0, windows.NTStatus(uint32(status))
	}
	return uint32(owner.process), nil
}

func replaceMutexOwner(name string, handle windows.Handle, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	pid, err := mutantOwnerPID(handle)
	if err != nil {
		return fmt.Errorf("无法识别旧客户端进程: %w", err)
	}
	if pid == uint32(os.Getpid()) {
		return errors.New("当前进程已经持有客户端单实例锁")
	}
	if pid != 0 {
		process, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
		if err != nil {
			if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
				if err := runElevatedReplacement(name, pid); err != nil {
					return err
				}
				// UAC consent may take longer than the process-exit deadline.
				return waitForReplacementObject(handle, time.Now().Add(timeout))
			}
			// The owner may have exited between the query and OpenProcess.
			if current, queryErr := mutantOwnerPID(handle); queryErr != nil || current == pid {
				return fmt.Errorf("无法结束旧客户端（PID %d），请检查旧进程权限: %w", pid, err)
			}
		} else {
			defer windows.CloseHandle(process)
			created, err := processCreationTime(process)
			if err != nil {
				return err
			}
			started, err := processCreationTime(windows.CurrentProcess())
			if err != nil {
				return err
			}
			if created >= started {
				return errors.New("较新的客户端已启动，本次启动已停止")
			}
			// Revalidate ownership after opening the process handle. If the old
			// process exited and its PID was reused, do not kill the new process.
			current, err := mutantOwnerPID(handle)
			if err != nil {
				return fmt.Errorf("复核旧客户端进程失败: %w", err)
			}
			if current == pid {
				if err := windows.TerminateProcess(process, 0); err != nil {
					if state, _ := windows.WaitForSingleObject(process, 0); state != windows.WAIT_OBJECT_0 {
						if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
							if err := runElevatedReplacement(name, pid); err != nil {
								return err
							}
							return waitForReplacementObject(handle, time.Now().Add(timeout))
						}
						return fmt.Errorf("结束旧客户端（PID %d）失败: %w", pid, err)
					}
				}
				// TerminateProcess is asynchronous. Ports and capture handles must
				// be released before the replacement opens local services.
				if err := waitForReplacementObject(process, deadline); err != nil {
					return fmt.Errorf("等待旧客户端（PID %d）退出失败: %w", pid, err)
				}
			}
		}
	}
	// Keep the original handle and acquire the abandoned/released mutex. Racing
	// launchers wait for this same object instead of repeatedly killing winners.
	if err := waitForReplacementObject(handle, deadline); err != nil {
		return fmt.Errorf("等待客户端单实例锁释放失败: %w", err)
	}
	return nil
}

func waitForReplacementObject(handle windows.Handle, deadline time.Time) error {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return errors.New("等待旧客户端退出超时")
	}
	state, err := windows.WaitForSingleObject(handle, uint32((remaining+time.Millisecond-1)/time.Millisecond))
	if err != nil {
		return err
	}
	if state != windows.WAIT_OBJECT_0 && state != windows.WAIT_ABANDONED {
		return fmt.Errorf("等待旧客户端退出超时（状态 %d）", state)
	}
	return nil
}

func (m *Mutex) Release() {
	if m != nil {
		m.once.Do(func() { close(m.release) })
		<-m.done
	}
}
