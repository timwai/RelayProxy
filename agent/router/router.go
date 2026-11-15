package router

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log"
	"net"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"relayproxy/agent/exit"
	"relayproxy/internal/acl"
	"relayproxy/internal/deviceidentity"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

type Config struct {
	ServerAddress string
	ServerName string
	TCPPort int
	IdentityID string
	DeviceName string
	Identity *deviceidentity.Identity
	TLSRootCAs *x509.CertPool
	ConnectTimeout time.Duration
	MaxConnections int
}

type Router struct {
	cfg Config
	handler *exit.Handler
	running atomic.Bool
}

func New(cfg Config) (*Router,error) {
	cfg.ServerAddress=strings.TrimSpace(cfg.ServerAddress)
	cfg.ServerName=strings.TrimSpace(cfg.ServerName)
	cfg.IdentityID=strings.ToLower(strings.TrimSpace(cfg.IdentityID))
	cfg.DeviceName=strings.TrimSpace(cfg.DeviceName)
	if cfg.ServerAddress=="" || cfg.IdentityID=="" || cfg.DeviceName=="" {
		return nil,errors.New("server address, identity ID and device name are required")
	}
	if cfg.Identity==nil || len(cfg.Identity.PublicKey)==0 || len(cfg.Identity.PrivateKey)==0 || cfg.Identity.InstallationID=="" {
		return nil,errors.New("device identity is incomplete")
	}
	if cfg.TCPPort<1 || cfg.TCPPort>65535 { return nil,fmt.Errorf("invalid TCP port %d",cfg.TCPPort) }
	if cfg.ConnectTimeout<=0 { cfg.ConnectTimeout=10*time.Second }
	if cfg.MaxConnections<=0 { cfg.MaxConnections=64 }
	if cfg.MaxConnections>256 { return nil,errors.New("max connections must be between 1 and 256") }
	if cfg.ServerName=="" { cfg.ServerName=cfg.ServerAddress }
	if net.ParseIP(cfg.ServerAddress)!=nil && cfg.ServerName==cfg.ServerAddress {
		return nil,errors.New("TLS server name is required when the Relay Server address is an IP")
	}
	checker,err:=acl.NewChecker(acl.Policy{
		ID:"router_exit_policy",Name:"Router Exit Policy",
		AllowInternet:true,AllowPrivateNetwork:true,AllowLoopback:false,
	})
	if err!=nil { return nil,fmt.Errorf("initialize Router ACL: %w",err) }
	handler:=exit.NewRouterHandler(exit.HandlerConfig{
		ACLChecker:checker,ConnectTimeout:cfg.ConnectTimeout,
		SystemDNSAllowed:func()bool{return true},
		ResumeEnabled:false,AllowBrowserNetwork:false,
	})
	return &Router{cfg:cfg,handler:handler},nil
}

// Capabilities returns the deliberately small Router protocol profile.
func Capabilities() (requested, transport []string) {
	return []string{protocol.CapabilityProxyExit},[]string{
		"tcp","tls",protocol.UDPModeStream,
		protocol.CapabilityTargetACL,protocol.CapabilityExitSystemDNS,
	}
}

func (r *Router) Run(ctx context.Context) error {
	if !r.running.CompareAndSwap(false,true) { return errors.New("Router is already running") }
	defer r.running.Store(false)
	backoffs:=[]time.Duration{time.Second,2*time.Second,4*time.Second,8*time.Second,16*time.Second,30*time.Second}
	attempt:=0
	for {
		if err:=ctx.Err(); err!=nil { return nil }
		addr:=net.JoinHostPort(r.cfg.ServerAddress,fmt.Sprint(r.cfg.TCPPort))
		tlsConfig:=&tls.Config{MinVersion:tls.VersionTLS13,ServerName:r.cfg.ServerName,RootCAs:r.cfg.TLSRootCAs}
		dialCtx,cancel:=context.WithTimeout(ctx,r.cfg.ConnectTimeout)
		sess,err:=tunnel.DialTLS(dialCtx,addr,tlsConfig,nil)
		cancel()
		if err==nil {
			log.Printf("[Router] TLS session established to %s",addr)
			attempt=0
			err=r.serveSession(ctx,sess)
			_ = sess.Close()
		}
		if ctx.Err()!=nil { return nil }
		if err==nil { err=errors.New("Relay session ended") }
		log.Printf("[Router] Relay session ended: %v",err)
		delay:=backoffs[min(attempt,len(backoffs)-1)]
		if attempt<len(backoffs)-1 { attempt++ }
		timer:=time.NewTimer(delay)
		select {
		case <-ctx.Done(): timer.Stop(); return nil
		case <-timer.C:
		}
	}
}

func (r *Router) Close() error { return nil }

func (r *Router) serveSession(parent context.Context,sess tunnel.TunnelSession) error {
	ctx,cancel:=context.WithCancel(parent)
	defer cancel()
	stopClose:=context.AfterFunc(ctx,func(){_ = sess.Close()})
	defer stopClose()

	openCtx,openCancel:=context.WithTimeout(ctx,r.cfg.ConnectTimeout)
	ctrl,err:=sess.OpenStream(openCtx)
	openCancel()
	if err!=nil { return fmt.Errorf("open control stream: %w",err) }
	defer ctrl.Close()
	if err:=ctrl.SetDeadline(time.Now().Add(r.cfg.ConnectTimeout)); err!=nil { return err }
	if err:=protocol.WriteStreamHeader(ctrl,&protocol.StreamHeader{Magic:protocol.MagicHeader,Version:protocol.CurrentVersion,Type:protocol.FrameTypeControl}); err!=nil {
		return fmt.Errorf("write control header: %w",err)
	}
	requested,transport:=Capabilities()
	nonce:=make([]byte,32)
	if _,err:=rand.Read(nonce); err!=nil { return fmt.Errorf("generate client nonce: %w",err) }
	hello:=protocol.DeviceHello{
		ProtocolVersion:protocol.IdentityDeviceProtocolVersion,
		IdentityID:r.cfg.IdentityID,InstallationID:r.cfg.Identity.InstallationID,
		PublicKey:append([]byte(nil),r.cfg.Identity.PublicKey...),ClientNonce:nonce,
		DeviceName:r.cfg.DeviceName,Platform:runtime.GOOS,Arch:runtime.GOARCH,
		ClientVersion:"2.0.0",RequestedCapabilities:requested,TransportCapabilities:transport,
	}
	if err:=protocol.WriteJSON(ctrl,hello); err!=nil { return fmt.Errorf("send device hello: %w",err) }
	var challenge protocol.AuthChallenge
	if err:=protocol.ReadJSON(ctrl,&challenge); err!=nil { return fmt.Errorf("read authentication challenge: %w",err) }
	if challenge.ProtocolVersion!=protocol.IdentityDeviceProtocolVersion || challenge.ChallengeID=="" ||
		challenge.ServerInstanceID=="" || len(challenge.ServerNonce)!=32 || time.Now().Unix()>challenge.ExpiresAt {
		return errors.New("server returned an invalid authentication challenge")
	}
	proof:=protocol.AuthProof{ChallengeID:challenge.ChallengeID,Signature:r.cfg.Identity.Sign(protocol.DeviceAuthPayload(hello,challenge))}
	if err:=protocol.WriteJSON(ctrl,proof); err!=nil { return fmt.Errorf("send authentication proof: %w",err) }
	var accepted protocol.DeviceAccepted
	if err:=protocol.ReadJSON(ctrl,&accepted); err!=nil { return fmt.Errorf("read device approval: %w",err) }
	if !accepted.Success { return fmt.Errorf("server rejected connection: [%s] %s",accepted.ErrorCode,accepted.ErrorMessage) }
	if accepted.State!="approved" || accepted.DeviceID=="" { return errors.New("server returned incomplete device approval") }
	if err:=ctrl.SetDeadline(time.Time{}); err!=nil { return err }
	allowExit:=false
	for _,capability:=range accepted.ApprovedCapabilities {
		if capability==protocol.CapabilityProxyExit { allowExit=true; break }
	}
	if !allowExit { log.Printf("[Router] device approved, but proxy.exit capability is not granted yet") }
	log.Printf("[Router] approved device=%s identity=%s heartbeat=%ds exit=%v",accepted.DeviceID,r.cfg.IdentityID,accepted.HeartbeatSec,allowExit)

	maxStreams:=r.cfg.MaxConnections
	if accepted.MaxConnections>0 && accepted.MaxConnections<maxStreams { maxStreams=accepted.MaxConnections }
	if accepted.HeartbeatSec<=0 { accepted.HeartbeatSec=15 }
	loopErrors:=make(chan error,2)
	var loops sync.WaitGroup
	loops.Add(2)
	go func(){
		defer loops.Done()
		if err:=r.acceptStreams(ctx,sess,maxStreams,allowExit,&workers); err!=nil && ctx.Err()==nil {
			select { case loopErrors<-err: default: }
		}
	}()
	go func(){
		defer loops.Done()
		if err:=r.heartbeat(ctx,ctrl,sess,time.Duration(accepted.HeartbeatSec)*time.Second); err!=nil && ctx.Err()==nil {
			select { case loopErrors<-err: default: }
		}
	}()
	var result error
	select {
	case <-ctx.Done():
	case <-sess.Done():
		result=errors.New("Relay transport closed")
	case result=<-loopErrors:
	}
	cancel()
	_ = sess.Close()
	_ = ctrl.Close()
	loops.Wait()
	workers.Wait()
	return result
}

func (r *Router) heartbeat(ctx context.Context,ctrl tunnel.TunnelStream,sess tunnel.TunnelSession,interval time.Duration) error {
	ticker:=time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done(): return ctx.Err()
		case <-sess.Done(): return errors.New("Relay transport closed during heartbeat")
		case <-ticker.C:
			deadline:=time.Now().Add(max(5*time.Second,2*interval))
			if err:=ctrl.SetDeadline(deadline); err!=nil { _=sess.Close(); return err }
			ping:=protocol.PingMessage{Timestamp:time.Now().UnixMilli()}
			if err:=protocol.WriteJSON(ctrl,ping); err!=nil { _=sess.Close(); return fmt.Errorf("send heartbeat: %w",err) }
			var pong protocol.PongMessage
			if err:=protocol.ReadJSON(ctrl,&pong); err!=nil { _=sess.Close(); return fmt.Errorf("read heartbeat: %w",err) }
			if err:=ctrl.SetDeadline(time.Time{}); err!=nil { _=sess.Close(); return err }
		}
	}
}

func (r *Router) acceptStreams(ctx context.Context,sess tunnel.TunnelSession,maxStreams int,allowExit bool,workers *sync.WaitGroup) error {
	ctx=exit.WithRelayOrigin(ctx)
	if maxStreams<=0 { maxStreams=r.cfg.MaxConnections }
	sem:=make(chan struct{},maxStreams)
	for {
		stream,err:=sess.AcceptStream(ctx)
		if err!=nil { return fmt.Errorf("accept incoming stream: %w",err) }
		select {
		case sem<-struct{}{}:
		default:
			_ = stream.Close()
			continue
		}
		_ = stream.SetDeadline(time.Now().Add(15*time.Second))
		stop:=tunnel.InterruptOnCancel(ctx,stream)
		header,err:=protocol.ReadStreamHeader(stream)
		stop()
		if err!=nil {
			<-sem
			_ = stream.Close()
			continue
		}
		if !allowExit || (header.Type!=protocol.FrameTypeOpenTCP && header.Type!=protocol.FrameTypeOpenUDP && header.Type!=protocol.FrameTypeExitSystemDNS) {
			<-sem
			_ = stream.Close()
			continue
		}
		workers.Add(1)
		go func(){
			defer workers.Done()
			defer func(){<-sem}()
			r.handler.HandleRouterStreamWithHeader(ctx,stream,header)
		}()
	}
}
