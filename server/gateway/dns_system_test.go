package gateway

import (
	"context"
	"net"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"relayproxy/internal/acl"
	"relayproxy/internal/protocol"
	"relayproxy/server/session"
)

func relayDNSQuery(t *testing.T) []byte {
	t.Helper()
	name,err:=dnsmessage.NewName("example.com.")
	if err!=nil {t.Fatal(err)}
	query,err:=(dnsmessage.Message{Header:dnsmessage.Header{ID:12,RecursionDesired:true},Questions:[]dnsmessage.Question{{Name:name,Type:dnsmessage.TypeA,Class:dnsmessage.ClassINET}}}).Pack()
	if err!=nil{t.Fatal(err)}
	return query
}

func TestRelayExitSystemDNSAuthorizesAndOverwritesForgedPolicy(t *testing.T) {
	for _,scenario:=range []string{"allowed","no-client-grant","legacy-exit","no-access"} {
		t.Run(scenario,func(t *testing.T){
			checker,err:=acl.NewChecker(acl.Policy{ID:"relay-dns",AllowInternet:true,AccessMode:acl.AccessModeAllow,AccessHosts:[]string{"example.com"}})
			if err!=nil{t.Fatal(err)}
			manager:=session.NewManager()
			upstream,exitPeer:=net.Pipe()
			defer upstream.Close()
			defer exitPeer.Close()
			sess:=&policyTestSession{stream:&policyTestStream{upstream}}
			caps:=[]string{"exit",protocol.CapabilityTargetACL}
			if scenario=="legacy-exit" {caps=[]string{"exit"}}
			manager.Register(&session.DeviceSession{DeviceID:"exit",Mode:"EXIT",Grants:[]string{protocol.CapabilityProxyExit},Capabilities:caps,Tunnel:sess})
			router:=NewStreamRouter(manager,checker,func(string,string)(bool,error){return scenario!="no-access",nil},nil)
			left,right:=net.Pipe()
			defer left.Close()
			defer right.Close()
			_ = right.SetDeadline(time.Now().Add(2*time.Second))
			ch:=make(chan *acl.Policy,1)
			query:=relayDNSQuery(t)
			if scenario=="allowed"{
				go func(){
					header,err:=protocol.ReadStreamHeader(exitPeer)
					if err!=nil || header.Type!=protocol.FrameTypeExitSystemDNS {return}
					var req protocol.ExitDNSRequest
					if protocol.ReadJSON(exitPeer,&req)!=nil {return}
					ch<-req.RelayPolicy
					msg,_,err:=protocol.ValidateExitDNSQuestion(req.Question)
					if err!=nil{return}
					answer,_:=(dnsmessage.Message{Header:dnsmessage.Header{ID:msg.ID,Response:true},Questions:msg.Questions}).Pack()
					_ = protocol.WriteJSON(exitPeer,protocol.ExitDNSResponse{RequestID:req.RequestID,Success:true,Answer:answer})
				}()
			}
			grants:=[]string{protocol.CapabilityProxyClient}
			if scenario=="no-client-grant" {grants=nil}
			done:=make(chan struct{})
			go func(){
				router.HandleClientStream(context.Background(),&policyTestStream{left},&session.DeviceSession{DeviceID:"client",Grants:grants})
				close(done)
			}()
			if err:=protocol.WriteStreamHeader(right,&protocol.StreamHeader{Type:protocol.FrameTypeExitSystemDNS,ExitDeviceID:"exit"});err!=nil{t.Fatal(err)}
			if err:=protocol.WriteJSON(right,protocol.ExitDNSRequest{
				RequestID:"dns-1",Question:query,RelayPolicy:&acl.Policy{ID:"forged",AllowInternet:true,AllowPrivateNetwork:true,AllowLoopback:true},
			});err!=nil{t.Fatal(err)}
			var resp protocol.ExitDNSResponse
			if err:=protocol.ReadJSON(right,&resp);err!=nil{t.Fatal(err)}
			if scenario=="allowed" {
				if !resp.Success {t.Fatalf("authorized DNS request failed: %+v",resp)}
				policy:=<-ch
				if policy==nil || policy.ID!="relay-dns" || policy.AllowPrivateNetwork || policy.AllowLoopback {
					t.Fatalf("forged Relay ACL reached exit: %+v",policy)
				}
			}else{
				if resp.Success || sess.opens.Load()!=0 {t.Fatalf("unauthorized DNS reached exit: %+v",resp)}
			}
			select {case <-done: case <-time.After(2*time.Second):t.Fatal("relay DNS stream did not close")}
		})
	}
}
