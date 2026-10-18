package systemproxy

import (
 "encoding/json"
 "errors"
 "fmt"
 "os"
 "path/filepath"
 "reflect"
 "sync"
 "time"
)

// Settings is opt-in. Absent booleans default to true, while Mode defaults to off.
type Settings struct {
 Mode string `yaml:"mode" json:"mode"`
 AutoFollow *bool `yaml:"auto_follow" json:"autoFollow,omitempty"`
 HTTP *bool `yaml:"http" json:"http,omitempty"`
 SOCKS5 *bool `yaml:"socks5" json:"socks5,omitempty"`
}
func on(p *bool) bool { return p == nil || *p }
func (s Settings) Follow() bool { return on(s.AutoFollow) }
func (s Settings) UseHTTP() bool { return on(s.HTTP) }
func (s Settings) UseSOCKS5() bool { return on(s.SOCKS5) }
func (s Settings) Enabled() bool { return s.Mode == "system" }

type ProxyState struct {
 Enabled bool `json:"enabled"`
 Host string `json:"host"`
 Port int `json:"port"`
 Authenticated bool `json:"authenticated"`
}
type entry struct {
 Service string `json:"service"`
 Kind string `json:"kind"`
 Before ProxyState `json:"before"`
 Applied ProxyState `json:"applied"`
}
type journal struct {
 Version int `json:"version"`
 Entries []entry `json:"entries"`
}
type backend interface {
 CurrentService() (string,error)
 Read(service, kind string) (ProxyState,error)
 Write(service,kind string,state ProxyState) error
 Validate(service string) error
}
type Status struct {
 Enabled bool `json:"enabled"`
 Service string `json:"service,omitempty"`
 Applied int `json:"applied"`
 PendingRecovery int `json:"pendingRecovery"`
 LastError string `json:"lastError,omitempty"`
}
type Manager struct {
 mu sync.Mutex
 backend backend
 path string
 desired Settings
 httpPort int
 socksPort int
 records []entry
 activeService string
 lastError string
 stop chan struct{}
 stopped chan struct{}
 closed bool
}

func New(configPath string) (*Manager,error) {
 if configPath == "" { return nil, errors.New("empty configuration path") }
 m:=&Manager{path:filepath.Join(filepath.Dir(configPath),"macos-system-proxy-recovery.json"),backend:newBackend(),stop:make(chan struct{}),stopped:make(chan struct{})}
 if err:=m.readJournal();err!=nil{return nil,err}
 // Keep the journal until recovery succeeds. Never overwrite an unknown prior snapshot.
 if err:=m.restore();err!=nil { m.lastError=err.Error() }
 go m.monitor()
 return m,nil
}
func (m *Manager) monitor() {
 defer close(m.stopped)
 ticker:=time.NewTicker(7*time.Second);defer ticker.Stop()
 for {select {case <-m.stop:return;case <-ticker.C:
  m.mu.Lock()
  if !m.closed && m.desired.Enabled() && m.desired.Follow() {
   service,err:=m.backend.CurrentService()
   if err!=nil {m.lastError=err.Error()} else if service!=m.activeService {
    if err=m.restore();err==nil {err=m.activate(service)}
    if err!=nil {m.lastError=err.Error()} else {m.lastError=""}
   }
  }
  m.mu.Unlock()
 }}
}
func (m *Manager) Status() Status {
 m.mu.Lock();defer m.mu.Unlock()
 return Status{Enabled:m.desired.Enabled(),Service:m.activeService,Applied:len(m.records),PendingRecovery:len(m.records),LastError:m.lastError}
}
func (m *Manager) Apply(s Settings,httpPort,socksPort int) error {
 m.mu.Lock();defer m.mu.Unlock()
 if m.closed {return errors.New("system proxy manager closed")}
 if s.Mode!=""&&s.Mode!="system" {return fmt.Errorf("invalid system proxy mode %q",s.Mode)}
 if reflect.DeepEqual(m.desired,s)&&m.httpPort==httpPort&&m.socksPort==socksPort&&(!s.Enabled()||m.activeService!="") {return nil}
 if err:=m.restore();err!=nil {m.lastError=err.Error();return err}
 m.desired=s
 m.httpPort=httpPort;m.socksPort=socksPort
 if !s.Enabled() {m.lastError="";return nil}
 if !s.UseHTTP()&&!s.UseSOCKS5() {return errors.New("system proxy requires HTTP or SOCKS5")}
 service,err:=m.backend.CurrentService()
 if err==nil {err=m.activate(service)}
 if err!=nil {m.lastError=err.Error();return err}
 m.lastError=""
 return nil
}
func (m *Manager) activate(service string) error {
 if service=="" {return errors.New("no active macOS network service")}
 if err:=m.backend.Validate(service);err!=nil{return err}
 targets:=[]struct{kind string;enabled bool;port int}{
  {"web",m.desired.UseHTTP(),m.httpPort},
  {"secureweb",m.desired.UseHTTP(),m.httpPort},
  {"socks",m.desired.UseSOCKS5(),m.socksPort},
 }
 // Preflight all enabled types before mutating any system settings.
 for _,t:=range targets {if t.enabled {
  if t.port<1||t.port>65535{return fmt.Errorf("invalid %s proxy port",t.kind)}
  original,err:=m.backend.Read(service,t.kind)
  if err!=nil{return err}
  if original.Authenticated{return fmt.Errorf("%s uses authenticated proxy; refusing to overwrite credentials",t.kind)}
 }}
 for _,t:=range targets {if !t.enabled {continue}
  original,err:=m.backend.Read(service,t.kind)
  if err!=nil{return err}
  desired:=ProxyState{Enabled:true,Host:"127.0.0.1",Port:t.port}
  if reflect.DeepEqual(original,desired) {continue}
  // Write-ahead journal is durable before the first privileged mutation.
  m.records=append(m.records,entry{Service:service,Kind:t.kind,Before:original,Applied:desired})
  if err=m.saveJournal();err!=nil {m.records=m.records[:len(m.records)-1];return err}
  if err=m.backend.Write(service,t.kind,desired);err!=nil {
   // A partial networksetup mutation is possible: keep the recovery journal.
   return fmt.Errorf("set %s proxy for %s: %w",t.kind,service,err)
  }
 }
 m.activeService=service
 return nil
}
func (m *Manager) restore() error {
 if len(m.records)==0 {m.activeService="";return nil}
 remaining:=make([]entry,0,len(m.records))
 var problems []error
 for i:=len(m.records)-1;i>=0;i--{
  e:=m.records[i]
  current,err:=m.backend.Read(e.Service,e.Kind)
  if err!=nil {remaining=append(remaining,e);problems=append(problems,fmt.Errorf("read %s/%s: %w",e.Service,e.Kind,err));continue}
  intermediate:=e.Applied;intermediate.Enabled=e.Before.Enabled
  if reflect.DeepEqual(current,e.Applied)||reflect.DeepEqual(current,intermediate) {
   if err=m.backend.Write(e.Service,e.Kind,e.Before);err!=nil {
    remaining=append(remaining,e);problems=append(problems,fmt.Errorf("restore %s/%s: %w",e.Service,e.Kind,err))
   }
  } else if !reflect.DeepEqual(current,e.Before) {
   // Another app/user changed this proxy. Never overwrite their newer value.
   problems=append(problems,fmt.Errorf("%s/%s changed externally: preserved current value",e.Service,e.Kind))
  }
 }
 for i,j:=0,len(remaining)-1;i<j;i,j=i+1,j-1 {remaining[i],remaining[j]=remaining[j],remaining[i]}
 m.records=remaining
 if err:=m.saveJournal();err!=nil {return err}
 m.activeService=""
 // External changes are warnings, but they must not prevent using another service.
 if len(remaining)>0{return errors.Join(problems...)}
 if len(problems)>0 {m.lastError=errors.Join(problems...).Error()}
 return nil
}
func (m *Manager) readJournal() error {
 b,err:=os.ReadFile(m.path)
 if errors.Is(err,os.ErrNotExist){return nil};if err!=nil{return err}
 var j journal
 if err=json.Unmarshal(b,&j);err!=nil{return fmt.Errorf("invalid recovery journal: %w",err)}
 if j.Version!=1{return fmt.Errorf("unsupported system proxy recovery journal version %d",j.Version)}
 m.records=j.Entries
 return nil
}
func (m *Manager) saveJournal() error {
 if len(m.records)==0 {if err:=os.Remove(m.path);err!=nil&&!errors.Is(err,os.ErrNotExist){return err};return nil}
 data,err:=json.MarshalIndent(journal{Version:1,Entries:m.records},"","  ")
 if err!=nil{return err}
 if err:=os.MkdirAll(filepath.Dir(m.path),0700);err!=nil{return err}
 f,err:=os.CreateTemp(filepath.Dir(m.path),".relayproxy-proxy-*")
 if err!=nil{return err}
 defer os.Remove(f.Name())
 if err=f.Chmod(0600);err!=nil {f.Close();return err}
 if _,err=f.Write(data);err!=nil {f.Close();return err}
 if err=f.Sync();err!=nil {f.Close();return err}
 if err=f.Close();err!=nil{return err}
 return os.Rename(f.Name(),m.path)
}
func (m *Manager) Close() error {
 m.mu.Lock()
 if m.closed {m.mu.Unlock();return nil}
 m.closed=true
 close(m.stop)
 m.mu.Unlock()
 <-m.stopped
 m.mu.Lock();defer m.mu.Unlock()
 err:=m.restore()
 if err!=nil{m.lastError=err.Error()}
 return err
}
