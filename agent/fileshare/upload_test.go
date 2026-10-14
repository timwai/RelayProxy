package fileshare

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"relayproxy/internal/protocol"
)

type uploadDuplex struct {
	r *io.PipeReader
	w *io.PipeWriter
}
func (s *uploadDuplex) Read(b []byte) (int, error) { return s.r.Read(b) }
func (s *uploadDuplex) Write(b []byte) (int, error) { return s.w.Write(b) }
func (s *uploadDuplex) Close() error { _ = s.r.Close(); return s.w.Close() }
func (s *uploadDuplex) CloseWrite() error { return s.w.Close() }
func (s *uploadDuplex) SetDeadline(time.Time) error { return nil }
func (s *uploadDuplex) SetReadDeadline(time.Time) error { return nil }
func (s *uploadDuplex) SetWriteDeadline(time.Time) error { return nil }

func uploadPair() (*uploadDuplex,*uploadDuplex) {
	clientReader, hostWriter := io.Pipe()
	hostReader, clientWriter := io.Pipe()
	return &uploadDuplex{clientReader,clientWriter}, &uploadDuplex{hostReader,hostWriter}
}

func uploadFixture(t *testing.T) (*FileStore, string, string) {
	t.Helper()
	dir:=t.TempDir()
	if err:=os.Mkdir(filepath.Join(dir,"shared"),0700);err!=nil{t.Fatal(err)}
	store,_:=New(filepath.Join(dir,"agent.yaml"))
	state,_:=store.Load()
	state.Enabled=true
	state.Shares=[]Share{{Name:"shared",RootPath:filepath.Join(dir,"shared"),Enabled:true,
		Permissions:map[string]Permission{"writer":PermissionReadWrite,"reader":PermissionRead}}}
	saved,err:=store.Save(state);if err!=nil{t.Fatal(err)}
	return store,saved.Shares[0].ID,saved.Shares[0].RootPath
}

func fileUploadRequest(t *testing.T, store *FileStore, from string, req protocol.FileShareRequest, contents []byte, revokeBeforeData bool) (protocol.FileShareResponse,protocol.FileShareResponse) {
	t.Helper()
	client,host:=uploadPair()
	defer client.Close()
	ctx,cancel:=context.WithTimeout(context.Background(),5*time.Second)
	defer cancel()
	done:=make(chan struct{})
	go func(){defer close(done);store.Serve(ctx,host,from)}()
	if err:=protocol.WriteJSON(client,req);err!=nil{t.Fatal(err)}
	var ready protocol.FileShareResponse
	if err:=protocol.ReadJSON(client,&ready);err!=nil{t.Fatal(err)}
	var final protocol.FileShareResponse
	if ready.Success {
		if revokeBeforeData {
			state,err:=store.Load();if err!=nil{t.Fatal(err)}
			delete(state.Shares[0].Permissions,from)
			if _,err:=store.Save(state);err!=nil{t.Fatal(err)}
		}
		if !revokeBeforeData { if _,err:=client.Write(contents);err!=nil{t.Fatal(err)} }
		if err:=client.CloseWrite();err!=nil{t.Fatal(err)}
		if err:=protocol.ReadJSON(client,&final);err!=nil{t.Fatal(err)}
	}
	<-done
	return ready,final
}

func TestUploadsAreAtomicAndOnlyAllowAuthorizedWriters(t *testing.T) {
	store,id,dir:=uploadFixture(t)
	req:=protocol.FileShareRequest{Action:protocol.FileShareUpload,ShareID:id,Path:"report.txt",Size:5}
	ready,final:=fileUploadRequest(t,store,"reader",req,nil,false)
	if ready.Success{t.Fatal("read-only device started upload")}
	ready,final=fileUploadRequest(t,store,"writer",req,[]byte("hello"),false)
	if !ready.Success||!final.Success||final.Size!=5{t.Fatalf("upload ack: %+v %+v",ready,final)}
	data,err:=os.ReadFile(filepath.Join(dir,"report.txt"))
	if err!=nil||string(data)!="hello"{t.Fatalf("data: %q err=%v",data,err)}
	// A second upload cannot replace any pre-existing file.
	ready,final=fileUploadRequest(t,store,"writer",req,[]byte("other"),false)
	if !ready.Success||final.Success||!strings.Contains(final.Error,"exists"){
		t.Fatalf("expected no-overwrite, got: %+v %+v",ready,final)
	}
	data,err=os.ReadFile(filepath.Join(dir,"report.txt"))
	if err!=nil||string(data)!="hello"{t.Fatalf("existing file changed: %q %v",data,err)}
}

func TestUploadRejectsTraversalAndMissingData(t *testing.T) {
	store,id,dir:=uploadFixture(t)
	for _,p:=range []string{"../escape.txt","/etc/passwd","a/../../file.txt"}{
		ready,_:=fileUploadRequest(t,store,"writer",protocol.FileShareRequest{
			Action:protocol.FileShareUpload,ShareID:id,Path:p,Size:0},nil,false)
		if ready.Success{t.Fatalf("accepted traversal: %q",p)}
	}
	req:=protocol.FileShareRequest{Action:protocol.FileShareUpload,ShareID:id,Path:"partial.txt",Size:10}
	ready,final:=fileUploadRequest(t,store,"writer",req,[]byte("short"),false)
	if !ready.Success||final.Success{t.Fatalf("partial upload accepted: %+v %+v",ready,final)}
	if _,err:=os.Stat(filepath.Join(dir,"partial.txt"));!os.IsNotExist(err){t.Fatalf("partial published: %v",err)}
	files,err:=os.ReadDir(dir)
	if err!=nil{t.Fatal(err)}
	for _,f:=range files{if strings.HasPrefix(f.Name(),".relayproxy-upload-"){t.Fatalf("staging file leaked: %s",f.Name())}}
}

func TestUploadRevocationBeforeDataDoesNotPublish(t *testing.T) {
	store,id,dir:=uploadFixture(t)
	req:=protocol.FileShareRequest{Action:protocol.FileShareUpload,ShareID:id,Path:"revoked.txt",Size:4}
	ready,final:=fileUploadRequest(t,store,"writer",req,[]byte("test"),true)
	if !ready.Success||final.Success{t.Fatalf("revoked upload accepted: %+v %+v",ready,final)}
	if _,err:=os.Stat(filepath.Join(dir,"revoked.txt"));!os.IsNotExist(err){t.Fatalf("revoked file published: %v",err)}
}
