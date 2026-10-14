package fileshare

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"sync"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

const uploadResumeLifetime = 30 * time.Minute

type partialFileUpload struct {
	id string
	client string
	shareID string
	rootPath string
	relativePath string
	size int64
	tempPath string
	busy bool
	touched time.Time
	expires *time.Timer
	mu sync.Mutex
}

// expireStagedUpload cannot remove an actively writing file. The unguessable
// upload ID is also bound to both the Server-verified device and share path.
func (s *FileStore) expireStagedUpload(id string) {
	s.uploadMu.Lock()
	defer s.uploadMu.Unlock()
	item := s.uploads[id]
	if item == nil { return }
	if item.busy || time.Since(item.touched) < uploadResumeLifetime {
		item.expires.Reset(uploadResumeLifetime)
		return
	}
	delete(s.uploads,id)
	if root, err := os.OpenRoot(item.rootPath); err == nil {
		_ = root.Remove(filepath.FromSlash(item.tempPath))
		_ = root.Close()
	}
}

func (s *FileStore) acquireStagedUpload(req protocol.FileShareRequest, share Share, deviceID string) (*partialFileUpload,error) {
	s.uploadMu.Lock()
	defer s.uploadMu.Unlock()
	if s.uploads == nil { s.uploads = make(map[string]*partialFileUpload) }
	if req.UploadID != "" {
		item := s.uploads[req.UploadID]
		if item == nil || item.client != deviceID || item.shareID != share.ID ||
			item.rootPath != share.RootPath || item.relativePath != req.Path || item.size != req.Size ||
			item.busy || time.Since(item.touched) > uploadResumeLifetime {
			return nil, errors.New("upload resume token is unavailable or expired")
		}
		item.busy = true
		item.touched = time.Now()
		item.expires.Stop()
		return item,nil
	}
	if len(s.uploads) >= 16 { return nil, errors.New("too many pending host uploads") }
	var random [24]byte
	if _,err:=rand.Read(random[:]);err!=nil{return nil,err}
	id:=hex.EncodeToString(random[:])
	temp:=path.Join(path.Dir(req.Path),".relayproxy-upload-"+id+".partial")
	root,err:=os.OpenRoot(share.RootPath)
	if err!=nil{return nil,err}
	f,err:=root.OpenFile(filepath.FromSlash(temp),os.O_RDWR|os.O_CREATE|os.O_EXCL,0600)
	_ = root.Close()
	if err!=nil{return nil,err}
	_ = f.Close()
	item:=&partialFileUpload{
		id:id,client:deviceID,shareID:share.ID,rootPath:share.RootPath,
		relativePath:req.Path,size:req.Size,tempPath:temp,busy:true,touched:time.Now(),
	}
	s.uploads[id]=item
	return item,nil
}

func (s *FileStore) releaseStagedUpload(item *partialFileUpload, committed bool) {
	s.uploadMu.Lock()
	defer s.uploadMu.Unlock()
	current:=s.uploads[item.id]
	if current!=item{return}
	item.busy=false
	item.touched=time.Now()
	if committed {
		delete(s.uploads,item.id)
		if item.expires != nil { item.expires.Stop() }
		return
	}
	if item.expires == nil {
		item.expires=time.AfterFunc(uploadResumeLifetime,func(){s.expireStagedUpload(item.id)})
	} else {
		item.expires.Reset(uploadResumeLifetime)
	}
}

// serveResumableUpload keeps an uncommitted scoped temp file for at most 30
// idle minutes after a connection failure. It never publishes until all bytes,
// the disk sync, and the current directory ACL have succeeded.
func (s *FileStore) serveResumableUpload(ctx context.Context, stream tunnel.TunnelStream, req protocol.FileShareRequest, share Share, deviceID string) {
	fail:=func(msg string){_ = protocol.WriteJSON(stream,protocol.FileShareResponse{Success:false,Error:msg})}
	if share.Permissions[deviceID]!=PermissionReadWrite ||
		req.Size<0 || req.Size>MaxUploadSize ||
		req.Path=="" || req.Path=="." || !safeUploadRelativePath(req.Path) {
		fail("upload not authorized or path invalid")
		return
	}
	if req.UploadID!="" && len(req.UploadID)!=48 {
		fail("invalid upload resume token")
		return
	}
	root,err:=os.OpenRoot(share.RootPath)
	if err!=nil {fail("shared folder unavailable");return}
	defer root.Close()
	parent:=path.Dir(req.Path)
	info,err:=root.Stat(filepath.FromSlash(parent))
	if err!=nil || !info.IsDir(){fail("destination folder unavailable");return}
	if _,err:=root.Lstat(filepath.FromSlash(req.Path));err==nil {
		fail("destination already exists");return
	} else if !errors.Is(err,os.ErrNotExist){
		fail("destination cannot be inspected");return
	}
	item,err:=s.acquireStagedUpload(req,share,deviceID)
	if err!=nil{fail(err.Error());return}
	committed:=false
	defer func(){s.releaseStagedUpload(item,committed)}()
	f,err:=root.OpenFile(filepath.FromSlash(item.tempPath),os.O_RDWR,0600)
	if err!=nil{fail("staged upload unavailable");return}
	defer f.Close()
	stat,err:=f.Stat()
	if err!=nil || stat.Size()<0 || stat.Size()>req.Size {fail("staged upload corrupted");return}
	offset:=stat.Size()
	digest:=sha256.New()
	if _,err=io.CopyN(digest,f,offset);err!=nil{fail("staged upload unreadable");return}
	prefix:=hex.EncodeToString(digest.Sum(nil))
	if err:=protocol.WriteJSON(stream,protocol.FileShareResponse{
		Success:true,Size:req.Size,Offset:offset,UploadID:item.id,PrefixSHA256:prefix,
	});err!=nil{return}
	_ = stream.SetDeadline(time.Time{})
	permissionReader:=&sharePermissionReader{
		input:stream,store:s,shareID:share.ID,rootPath:share.RootPath,deviceID:deviceID,
	}
	n,err:=io.CopyN(io.MultiWriter(f,digest),permissionReader,req.Size-offset)
	if err!=nil || n!=req.Size-offset || ctx.Err()!=nil {return}
	if err:=permissionReader.authorize();err!=nil{fail("upload ACL revoked");return}
	if err:=f.Sync();err!=nil{fail("unable to sync upload");return}
	if err:=f.Close();err!=nil{fail("unable to finalize upload");return}
	if err:=permissionReader.authorize();err!=nil{fail("upload ACL revoked");return}
	if err:=root.Link(filepath.FromSlash(item.tempPath),filepath.FromSlash(req.Path));err!=nil{
		if errors.Is(err,os.ErrExist){fail("destination already exists")} else {fail("unable to publish uploaded file")}
		return
	}
	_ = root.Remove(filepath.FromSlash(item.tempPath))
	committed=true
	_ = protocol.WriteJSON(stream,protocol.FileShareResponse{
		Success:true,Size:req.Size,SHA256:hex.EncodeToString(digest.Sum(nil)),
	})
}

func safeUploadRelativePath(p string) bool {
	if !filepath.IsLocal(filepath.FromSlash(p)) || path.IsAbs(p) {return false}
	if p=="." || p=="" {return false}
	for _,part:=range splitUploadPath(p){
		if part==".." || part=="." || part=="" || len(part)>=19 && part[:19]==".relayproxy-upload-" {return false}
	}
	return true
}

func splitUploadPath(p string) []string {
	parts:=[]string{}
	for _,segment:=range []byte(p){
		_ = segment
	}
	// Slash is the only legal path separator in the wire format.
	start:=0
	for i:=0;i<len(p);i++ {
		if p[i]=='/' {
			parts=append(parts,p[start:i])
			start=i+1
		}
		if p[i]=='\\' {return []string{".."}}
	}
	return append(parts,p[start:])
}
