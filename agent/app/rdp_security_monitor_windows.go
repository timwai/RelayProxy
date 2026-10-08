//go:build windows

package app

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"sort"
	"time"

	"relayproxy/agent/rdp"
	"relayproxy/internal/protocol"
)

// monitorWindowsRDPAuthFailures reads the local Windows Security audit log.
// A Windows policy enabling Audit Logon Failure, and permission to read the
// Security log, are prerequisites. Failure is best-effort and never stops RDP.
func monitorWindowsRDPAuthFailures(ctx context.Context, report func(context.Context,protocol.RDPHostAuthFailure) error) {
	ticker:=time.NewTicker(5*time.Second)
	defer ticker.Stop()
	seen:=make(map[uint64]time.Time)
	lastError:=time.Time{}
	query:=func() {
		// /uni:true makes output UTF-16; decoder handles its XML declaration.
		// /e:Events asks wevtutil to wrap multi-event output in one root.
		queryCtx,cancel:=context.WithTimeout(ctx,8*time.Second)
		defer cancel()
		arg := "/q:*[System[(EventID=4625) and TimeCreated[timediff(@SystemTime) <= 30000]]]"
		raw,err:=exec.CommandContext(queryCtx,"wevtutil.exe","qe","Security",arg,"/f:xml","/rd:true","/c:256","/e:Events","/uni:true").Output()
		if err!=nil {
			if ctx.Err()==nil && time.Since(lastError)>5*time.Minute {
				log.Printf("[RDP Security] Cannot read Windows Security 4625 events (enable Audit Logon Failure and grant Security log access): %v",err)
				lastError=time.Now()
			}
			return
		}
		events,err:=rdp.DecodeWindowsRDP4625(raw)
		if err!=nil {
			if time.Since(lastError)>5*time.Minute {
				log.Printf("[RDP Security] Invalid Windows Security XML: %v",err)
				lastError=time.Now()
			}
			return
		}
		now:=time.Now()
		for record,observed:=range seen {
			if now.Sub(observed)>2*time.Minute { delete(seen,record) }
		}
		if len(seen)>4096 { clear(seen) }
		sort.Slice(events,func(i,j int) bool { return events[i].ObservedAt.Before(events[j].ObservedAt) })
		for _,event:=range events {
			if _,exists:=seen[event.RecordID]; exists { continue }
			if event.ObservedAt.Before(now.Add(-35*time.Second)) || event.ObservedAt.After(now.Add(5*time.Second)) { continue }
			sendCtx,sendCancel:=context.WithTimeout(ctx,4*time.Second)
			err:=report(sendCtx,event)
			sendCancel()
			if err!=nil {
				if time.Since(lastError)>time.Minute {
					log.Printf("[RDP Security] Unable to report Windows login failure: %v",err)
					lastError=time.Now()
				}
				continue // retry during the short observation window
			}
			seen[event.RecordID]=now
		}
	}
	for {
		select {
		case <-ctx.Done(): return
		case <-ticker.C: query()
		}
	}
}

// This function is kept here rather than in the agent/rdp transport package
// because it is tied to the lifetime of an authenticated Agent session.
var _ = fmt.Sprintf
