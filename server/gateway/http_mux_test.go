package gateway

import (
	"context"
    "bufio"
    "fmt"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"relayproxy/internal/cert"
	"relayproxy/internal/deviceidentity"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
	"relayproxy/server/session"
)

func TestSharedTCPPortServesHTTPAndRelayTunnel(t *testing.T) {
	certificate, err := cert.EnsureCertificate("", "", "localhost")
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.NewManager()
	gateway := NewGateway(GatewayConfig{
		TCPAddr:   "127.0.0.1:0",
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{certificate}},
		PublicHTTPHandler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.URL.Path != "/api/v1/push/test" {
				http.NotFound(w, req)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		}),
		HandshakeTimeout:      time.Second,
		ServerInstanceID:      "shared-port-test",
		AllowLegacyDeviceAuth: true,
		AuthorizeDevice: func(string, protocol.DeviceHello) (DeviceAuthorization, error) {
			return DeviceAuthorization{
				State:                "approved",
				DeviceID:             "shared-client",
				ApprovedCapabilities: []string{protocol.CapabilityProxyClient},
			}, nil
		},
		RecheckDevice: func(string, string) bool { return true },
	}, sessions, NewStreamRouter(sessions, nil, nil, nil))
	if err := gateway.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gateway.Close() })

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13},
	}
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	response, err := client.Get("https://" + gateway.TCPAddr().String() + "/api/v1/push/test")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	transport.CloseIdleConnections()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != `{"ok":true}` {
		t.Fatalf("unexpected shared-port HTTP response: status=%d body=%q", response.StatusCode, body)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	relay, err := tunnel.DialTLS(ctx, gateway.TCPAddr().String(), &tls.Config{InsecureSkipVerify: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	control, err := relay.OpenStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	writeControlHeader(t, control)
	identity, err := deviceidentity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	accepted := authenticateTestDevice(t, control, identity)
	if !accepted.Success || accepted.DeviceID != "shared-client" {
		t.Fatalf("relay tunnel failed on shared port: %+v", accepted)
	}
}

func TestSharedTCPFallbackSniffPreservesOldRelayBytes(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = client.Write([]byte{0, 1, 2, 3})
	}()

	classified, isHTTP, err := classifySharedTCP(server, "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if isHTTP {
		t.Fatal("binary relay prefix was classified as HTTP")
	}
	var first [1]byte
	if _, err := io.ReadFull(classified, first[:]); err != nil {
		t.Fatal(err)
	}
	if first[0] != 0 {
		t.Fatalf("sniff consumed relay prefix: got %d", first[0])
	}
	<-done
}

func TestSharedTCPFallbackSniffRecognizesHTTP(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	go func() {
		_, _ = client.Write([]byte("GET /api/v1/push/test HTTP/1.1\r\n\r\n"))
	}()

	classified, isHTTP, err := classifySharedTCP(server, "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !isHTTP {
		t.Fatal("HTTP request was classified as relay traffic")
	}
	reader := make([]byte, 4)
	if _, err := io.ReadFull(classified, reader); err != nil {
		t.Fatal(err)
	}
	if string(reader) != "GET " {
		t.Fatalf("sniff consumed HTTP prefix: %q", reader)
	}
}

func TestSharedTCPPortWebsocketUpgradeAndCONNECTHijack(t *testing.T) {
    certificate,err:=cert.EnsureCertificate("","","localhost")
    if err!=nil{t.Fatal(err)}
    handler:=http.HandlerFunc(func(w http.ResponseWriter,req *http.Request){
        if req.TLS==nil { http.Error(w,"TLS state missing",http.StatusUpgradeRequired);return }
        if req.URL.Path=="/ws"{
            conn,err:=websocket.Accept(w,req,&websocket.AcceptOptions{InsecureSkipVerify:true})
            if err!=nil{return}
            defer conn.CloseNow()
            ctx,cancel:=context.WithTimeout(req.Context(),3*time.Second)
            defer cancel()
            typ,data,err:=conn.Read(ctx)
            if err==nil { _=conn.Write(ctx,typ,data) }
            return
        }
        if req.Method==http.MethodConnect{
            if req.Host!="example.com:443" { http.Error(w,"wrong host",403);return }
            hijacker,ok:=w.(http.Hijacker)
            if !ok {http.Error(w,"hijack unsupported",500);return}
            conn,buffer,err:=hijacker.Hijack()
            if err!=nil{return}
            defer conn.Close()
            _,_=fmt.Fprint(conn,"HTTP/1.1 200 Connection Established\r\n\r\n")
            data:=make([]byte,4)
            if _,err=io.ReadFull(buffer,data);err==nil {_,_=conn.Write(data)}
            return
        }
        http.NotFound(w,req)
    })
    g:=NewGateway(GatewayConfig{
        TCPAddr:"127.0.0.1:0",
        TLSConfig:&tls.Config{Certificates:[]tls.Certificate{certificate}},
        PublicHTTPHandler:handler,
        HandshakeTimeout:2*time.Second,
    },session.NewManager(),NewStreamRouter(session.NewManager(),nil,nil,nil))
    if err:=g.Start();err!=nil{t.Fatal(err)}
    defer g.Close()
    addr:=g.TCPAddr().String()
    client:=&http.Client{Transport:&http.Transport{
        TLSClientConfig:&tls.Config{InsecureSkipVerify:true,MinVersion:tls.VersionTLS13},
    },Timeout:4*time.Second}
    ctx,cancel:=context.WithTimeout(context.Background(),4*time.Second)
    defer cancel()
    ws,_,err:=websocket.Dial(ctx,"wss://"+addr+"/ws",&websocket.DialOptions{HTTPClient:client})
    if err!=nil { t.Fatal(err) }
    if err:=ws.Write(ctx,websocket.MessageText,[]byte("ping"));err!=nil{t.Fatal(err)}
    _,value,err:=ws.Read(ctx)
    if err!=nil || string(value)!="ping" {t.Fatalf("websocket failed: %v %q",err,value)}
    _=ws.CloseNow()

    raw,err:=tls.DialWithDialer(&net.Dialer{Timeout:3*time.Second},"tcp",addr,
        &tls.Config{InsecureSkipVerify:true,MinVersion:tls.VersionTLS13,NextProtos:[]string{"http/1.1"}})
    if err!=nil{t.Fatal(err)}
    defer raw.Close()
    _=raw.SetDeadline(time.Now().Add(3*time.Second))
    if _,err=fmt.Fprint(raw,"CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n");err!=nil{t.Fatal(err)}
    reader:=bufio.NewReader(raw)
    response,err:=http.ReadResponse(reader,&http.Request{Method:http.MethodConnect})
    if err!=nil{t.Fatal(err)}
    if response.StatusCode!=200{t.Fatalf("CONNECT returned %d",response.StatusCode)}
    if _,err=raw.Write([]byte("test"));err!=nil{t.Fatal(err)}
    echoed:=make([]byte,4)
    if _,err=io.ReadFull(reader,echoed);err!=nil||string(echoed)!="test"{
        t.Fatalf("CONNECT hijacked stream failed: %v %q",err,echoed)
    }
}
